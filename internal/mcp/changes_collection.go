package mcp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/meaningfulchanges"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// changesCollection is what get_changes says about Radar's collection of the
// custom kind a query names. An empty answer for a kind Radar isn't watching
// would otherwise read the same as no changes. coverage is the coverage
// list_resources reports for the kind: what Radar collects now, not how much
// change history it holds.
type changesCollection struct {
	coverage *resourceapi.Coverage
	hints    []string
	// since says when collection started inside the window; sinceAt is that
	// time. The handler keeps the sentence unless the response's own
	// historyCoverage already states a boundary that close.
	since   string
	sinceAt time.Time
	// clusterScoped names the kind when it is cluster-scoped and its
	// collection was checked.
	clusterScoped string
}

// sinceHint is the collection-start sentence unless history reports a
// boundary within collectionStartSlack of it: then the two say the same.
func (c changesCollection) sinceHint(history *issuesapi.HistoryCoverage) string {
	if c.since == "" {
		return ""
	}
	// A sentence with no start (per-namespace watches) is never covered.
	if history != nil && !history.CompleteSince.IsZero() && !c.sinceAt.IsZero() && c.sinceAt.Sub(history.CompleteSince) < collectionStartSlack {
		return ""
	}
	return c.since
}

// changesKindCollection checks Radar's collection of kind the way
// list_resources does, without reading rows, and may start collecting it as
// that read does. Built-in kinds get nothing: their history is complete from
// Radar's start, which historyCoverage already reports.
func changesKindCollection(ctx context.Context, kind, namespace string, since time.Duration) (changesCollection, error) {
	var out changesCollection
	started := currentReadCache()
	kind = meaningfulchanges.CanonicalKind(kind)
	ids := kindIdentities(kind)
	if len(ids) == 0 {
		refreshDiscoveryForKind(strings.ToLower(kind), "")
		ids = kindIdentities(kind)
	}
	// Native Helm history is listed as HelmRelease too, and Radar reads it
	// from release records, not a watch.
	helm := issues.KindFilterIncludes([]string{kind}, "HelmRelease", "helmreleases")
	_, builtin := resourceid.BuiltinGroup(kind)
	custom := slices.DeleteFunc(slices.Clone(ids), func(id k8score.APIResource) bool { return k8score.IsBuiltInAPIGroup(id.Group) })
	switch {
	case len(ids) == 0 && !helm && !builtin:
		out.hints = append(out.hints, readErrorHint(errKindUnknown(kind, "", nil)))
		return out, nil
	case len(custom) == 0:
		return out, nil
	case len(custom) > 1:
		groups := make([]string, 0, len(custom))
		for _, id := range custom {
			groups = append(groups, id.Group)
		}
		out.hints = append(out.hints, fmt.Sprintf("%s names custom kinds in more than one API group (%s), and these changes may include any of them, so Radar can't say which it collects here. list_resources with kind and group says for each.", kind, strings.Join(groups, ", ")))
		return out, nil
	}
	id := custom[0]
	// Beside a built-in kind or Helm history of the same name, the answer
	// mixes sources: the custom kind is named by group and gets no coverage
	// field, which would read as describing every row.
	shared := len(ids) > 1 || helm || builtin
	label := id.Name
	if shared {
		label = id.Name + "." + id.Group
	}
	clusterScoped := !id.Namespaced
	if clusterScoped && namespace != "" {
		out.hints = append(out.hints, fmt.Sprintf("%s are cluster-scoped, so their changes have no namespace; omit namespace to see them.", label))
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
			return out, collectionError(&out, err, shared, label)
		}
		// Change rows need list on the kind itself, not just namespace
		// access, so coverage checks the same.
		if readable, gaps, err = narrowByExactAccess(ctx, target.namespaces, id.Group, id.Name, "list"); err != nil {
			return out, collectionError(&out, withListCoverage(ctx, err, id.Name, target, clusterScoped), shared, label)
		}
	}
	r, err := readDynamicList(ctx, started, id.Name, id.Group, target, readable, gaps, clusterScoped, false)
	if err != nil {
		return out, collectionError(&out, err, shared, label)
	}
	cov := r.coverage(ctx, clusterScoped)
	if !shared {
		out.coverage = &cov
	}
	if sentence := collectionSentence(ctx, label, cov); sentence != "" {
		out.hints = append(out.hints, sentence)
	}
	out.since, out.sinceAt = collectionSinceSentence(label, id.Name, id.Group, since)
	if clusterScoped {
		out.clusterScoped = label
	}
	if currentReadCache() != started {
		return changesCollection{}, errConnectionChanged()
	}
	return out, nil
}

// kindIdentities returns every resource discovery serves under kind,
// matched by Kind or plural name. Changes are filtered by Kind alone, so all
// of them can appear in one answer.
func kindIdentities(kind string) []k8score.APIResource {
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

// collectionError keeps a failed collection check as an explanation, so the
// changes still return. A changed connection fails the answer instead: its
// rows and its coverage may describe different clusters.
func collectionError(out *changesCollection, err error, shared bool, label string) error {
	var re *readError
	if !errors.As(err, &re) {
		return err
	}
	if re.code == readreason.ConnectionChanged {
		return err
	}
	if !shared {
		out.coverage = re.coverage
	}
	hint := readErrorHint(err)
	if shared {
		// Rows of the same name from other sources may still be listed.
		hint = "For " + label + ": " + hint
	}
	out.hints = append(out.hints, hint)
	return nil
}

// readErrorHint is a failed read's explanation and what to do next. The
// read's own next steps name list_resources and its arguments, so only the
// generic ones are kept.
func readErrorHint(err error) string {
	var re *readError
	if !errors.As(err, &re) {
		return err.Error()
	}
	next := ""
	switch {
	case re.code == readreason.KindUnknown:
		next = "Check the spelling; list_resources with kind=customresourcedefinitions lists the custom kinds installed here."
	case re.coverage != nil:
		next = retrySteps(*re.coverage)
	}
	if next == "" && re.code != readreason.KindUnknown {
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
// was inside the window, and that time. Only a cluster-wide watch has one
// start for every namespace; per-namespace watches start separately, so then
// the start is not established. A cluster-wide watch still loading is a gap
// in coverage instead.
func collectionSinceSentence(resource, name, group string, since time.Duration) (string, time.Time) {
	dynamicCache := k8s.GetDynamicResourceCache()
	gvr, ok := discoveredGVR(name, group)
	if dynamicCache == nil || !ok {
		return "", time.Time{}
	}
	switch dynamicCache.Observation(gvr).Scope {
	case k8score.DynamicObservationScopeCluster:
	case k8score.DynamicObservationScopeExplicitNamespaces:
		return fmt.Sprintf("Radar watches %s per namespace here, so it can't establish since when it has collected them; changes early in the window may be missing.", resource), time.Time{}
	default:
		return "", time.Time{}
	}
	watched, ok := dynamicCache.WatchedSince(gvr)
	if !ok || !watched.After(time.Now().Add(-since)) {
		return "", time.Time{}
	}
	return fmt.Sprintf("Radar has watched %s only since %s; earlier changes in the window are unknown.", resource, watched.UTC().Format(time.RFC3339)), watched
}

// collectionStartSlack is how long after Radar starts its startup watches
// may take to begin.
const collectionStartSlack = time.Minute

// readableClusterScopedGroups returns the API groups of kind's cluster-scoped
// resources the caller may list.
func readableClusterScopedGroups(ctx context.Context, kind string) []string {
	kind = meaningfulchanges.CanonicalKind(kind)
	var ids []k8score.APIResource
	// Built-in cluster-scoped kinds resolve without discovery, whose typed
	// informers keep recording while it fails.
	if group, resource, ok := k8s.ClusterOnlyKindGVR(kind); ok {
		ids = append(ids, k8score.APIResource{Group: group, Name: resource})
	}
	ids = append(ids, kindIdentities(kind)...)
	// Change rows name a kind and group, not a resource, so a group counts
	// only when the caller may list every resource of that kind in it.
	allowed := map[string]bool{}
	var groups []string
	for _, id := range ids {
		if id.Namespaced {
			continue
		}
		granted, _ := canReadInNamespaceDecision(ctx, id.Group, id.Name, "", "list")
		if seen, ok := allowed[id.Group]; ok {
			allowed[id.Group] = seen && granted
			continue
		}
		allowed[id.Group] = granted
		groups = append(groups, id.Group)
	}
	return slices.DeleteFunc(groups, func(g string) bool { return !allowed[g] })
}
