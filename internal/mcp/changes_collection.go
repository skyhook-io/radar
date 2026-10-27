package mcp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
)

// changesCollection is what get_changes says about Radar's collection of the
// custom kind a query names. An empty answer for a kind Radar isn't watching
// would otherwise read the same as no changes. coverage is the coverage
// list_resources reports for the kind: what Radar collects now, not how much
// change history it holds.
type changesCollection struct {
	coverage *resourceapi.Coverage
	hints    []string
}

// changesKindCollection checks Radar's collection of kind the way
// list_resources does, without reading rows, and may start collecting it as
// that read does. Built-in kinds get nothing: their history is complete from
// Radar's start, which historyCoverage already reports.
func changesKindCollection(ctx context.Context, kind, namespace string, since time.Duration) (changesCollection, error) {
	var out changesCollection
	started := currentReadCache()
	ids := customKindIdentities(kind)
	if len(ids) == 0 {
		refreshDiscoveryForKind(strings.ToLower(kind), "")
		ids = customKindIdentities(kind)
	}
	switch {
	case len(ids) == 0:
		out.hints = append(out.hints, readErrorHint(errKindUnknown(kind, "", nil)))
		return out, nil
	case !slices.ContainsFunc(ids, func(id k8score.APIResource) bool { return !k8score.IsBuiltInAPIGroup(id.Group) }):
		return out, nil
	case len(ids) > 1:
		groups := make([]string, 0, len(ids))
		for _, id := range ids {
			groups = append(groups, groupLabel(id.Group))
		}
		out.hints = append(out.hints, fmt.Sprintf("%s names a kind in more than one API group (%s), and these changes may include any of them, so Radar can't say which it collects here. list_resources with kind and group says for each.", kind, strings.Join(groups, ", ")))
		return out, nil
	}
	id := ids[0]
	clusterScoped := !id.Namespaced
	if clusterScoped && namespace != "" {
		out.hints = append(out.hints, fmt.Sprintf("%s are cluster-scoped, so their changes have no namespace; omit namespace to see them.", id.Name))
		return out, nil
	}

	var target readTarget
	var readable []string
	var gaps gapSet
	if clusterScoped {
		if granted, checked := canReadClusterScopedKindDecision(ctx, id.Name, id.Group, "list"); !granted {
			out.hints = append(out.hints, readErrorHint(resourcePermissionError(ctx, checked, "list", id.Group, id.Name, "")))
			return out, nil
		}
	} else {
		var requested []string
		if namespace != "" {
			requested = []string{namespace}
		}
		var err error
		if target, err = resolveReadTargetFrom(ctx, started, requested); err != nil {
			return out, collectionError(&out, err)
		}
		// Change rows need list on the kind itself, not just namespace
		// access, so coverage checks the same.
		if readable, gaps, err = narrowByExactAccess(ctx, target.namespaces, id.Group, id.Name, "list"); err != nil {
			return out, collectionError(&out, withListCoverage(ctx, err, id.Name, target, clusterScoped))
		}
	}
	r, err := readDynamicList(ctx, started, id.Name, id.Group, target, readable, gaps, clusterScoped, false)
	if err != nil {
		return out, collectionError(&out, err)
	}
	cov := r.coverage(ctx, clusterScoped)
	out.coverage = &cov
	if sentence := collectionSentence(ctx, r.resource, cov); sentence != "" {
		out.hints = append(out.hints, sentence)
	}
	if sentence := collectionSinceSentence(r.resource, id.Name, id.Group, since); sentence != "" {
		out.hints = append(out.hints, sentence)
	}
	if currentReadCache() != started {
		return changesCollection{}, errConnectionChanged()
	}
	return out, nil
}

// customKindIdentities returns every resource discovery serves under kind,
// matched by Kind or plural name. Changes are filtered by Kind alone, so all
// of them can appear in one answer.
func customKindIdentities(kind string) []k8score.APIResource {
	discovery := k8s.GetResourceDiscovery()
	if discovery == nil {
		return nil
	}
	resources, _ := discovery.GetAPIResources()
	var out []k8score.APIResource
	for _, r := range resources {
		if strings.EqualFold(r.Kind, kind) || strings.EqualFold(r.Name, kind) {
			out = append(out, r)
		}
	}
	return out
}

func groupLabel(group string) string {
	if group == "" {
		return "core"
	}
	return group
}

// collectionError keeps a failed collection check as an explanation, so the
// changes still return. A changed connection fails the answer instead: its
// rows and its coverage may describe different clusters.
func collectionError(out *changesCollection, err error) error {
	var re *readError
	if !errors.As(err, &re) {
		return err
	}
	if re.code == readreason.ConnectionChanged {
		return err
	}
	out.coverage = re.coverage
	out.hints = append(out.hints, readErrorHint(err))
	return nil
}

// readErrorHint is a failed read's explanation and what to do next.
func readErrorHint(err error) string {
	var re *readError
	if !errors.As(err, &re) {
		return err.Error()
	}
	next := re.next
	if next == "" && re.coverage != nil {
		next = retrySteps(*re.coverage)
	}
	if next == "" {
		next = errorNext(re.code)
	}
	return strings.TrimSpace(re.err.Error() + " " + next)
}

// collectionSentence says what part of the requested scope Radar collects
// the kind in, when that is less than all of it. A narrowed scope reads
// complete, yet changes outside it are unknown, so it is stated too.
func collectionSentence(ctx context.Context, resource string, cov resourceapi.Coverage) string {
	narrowed := len(cov.NarrowedBy) > 0 && cov.Scope.Namespaces != nil
	if cov.Status != resourceapi.CoveragePartial && !narrowed {
		return ""
	}
	s := []string{fmt.Sprintf("Changes to %s are known only where Radar collects them.", resource)}
	for _, gap := range cov.Gaps {
		s = append(s, gapSentence(ctx, resource, gap))
	}
	if narrowed {
		s = append(s, otherNamespacesUnknown(ctx, resource, cov))
	}
	if retry := retrySteps(cov); retry != "" {
		s = append(s, retry)
	}
	return strings.Join(s, " ")
}

// collectionSinceSentence says when collection of the kind started, if that
// was inside the window. Only a cluster-wide watch has one start for every
// namespace; per-namespace watches start separately, so then the start is
// not established.
func collectionSinceSentence(resource, name, group string, since time.Duration) string {
	dynamicCache := k8s.GetDynamicResourceCache()
	gvr, ok := discoveredGVR(name, group)
	if dynamicCache == nil || !ok {
		return ""
	}
	if !dynamicCache.IsClusterWideSynced(gvr) {
		if dynamicCache.IsSynced(gvr) {
			return fmt.Sprintf("Radar watches %s per namespace here, so it can't establish since when it has collected them; changes early in the window may be missing.", resource)
		}
		return ""
	}
	watched, ok := dynamicCache.WatchedSince(gvr)
	if !ok || !watched.After(time.Now().Add(-since)) {
		return ""
	}
	return fmt.Sprintf("Radar has watched %s only since %s; earlier changes in the window are unknown.", resource, watched.UTC().Format(time.RFC3339))
}
