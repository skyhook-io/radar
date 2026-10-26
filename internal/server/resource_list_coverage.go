package server

import (
	"fmt"
	"net/http"
	"slices"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/readplan"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
)

// listTarget is what a list is about: the namespaces the request named, or
// what remained of every namespace after the --namespace-scope pin and the
// caller's access narrowed it. A named namespace stays
// in scope even when it can't be read, so what's missing is a gap; implicit
// narrowing is declared instead. gaps are the caller's own: named namespaces
// they can't see, and namespaces they can see where exact kind authorization
// denies the kind.
type listTarget struct {
	clusterScoped bool
	// explicit: the request named its namespaces.
	explicit bool
	// namespaces is what may be read; named, on an explicit request, is every
	// namespace it named, including ones the caller can't see or the pin
	// excludes: those stay in scope, as gaps.
	namespaces, named []string
	narrowedBy        []resourceapi.Narrowing
	gaps              []resourceapi.Gap
}

func (t listTarget) scope() resourceapi.Scope {
	switch {
	case t.clusterScoped:
		return resourceapi.Scope{Type: resourceapi.ScopeCluster}
	case t.explicit:
		return resourceapi.Scope{Type: resourceapi.ScopeNamespaces, Namespaces: resourceapi.Names(t.named, resourceapi.RESTSetLimit)}
	case t.namespaces == nil:
		return resourceapi.Scope{Type: resourceapi.ScopeAllNamespaces}
	default:
		return resourceapi.Scope{Type: resourceapi.ScopeNamespaces, Namespaces: resourceapi.Names(t.namespaces, resourceapi.RESTSetLimit)}
	}
}

// coverage is a successful list's coverage: read is the namespaces read, or
// nil when Radar read the whole scope.
func (t listTarget) coverage(read []string, gaps []resourceapi.Gap) resourceapi.Coverage {
	gaps = t.placeGaps(append(slices.Clone(t.gaps), gaps...), read)
	cov := resourceapi.Coverage{Status: resourceapi.CoverageComplete, Scope: t.scope(), NarrowedBy: t.narrowedBy, Gaps: gaps}
	readScope := cov.Scope
	switch {
	case t.clusterScoped:
	case read != nil:
		readScope = resourceapi.Scope{Type: resourceapi.ScopeNamespaces, Namespaces: resourceapi.Names(read, resourceapi.RESTSetLimit)}
	case t.namespaces != nil:
		// The whole readable target was read, which never includes named
		// namespaces the caller can't see or the pin excludes.
		readScope = resourceapi.Scope{Type: resourceapi.ScopeNamespaces, Namespaces: resourceapi.Names(t.namespaces, resourceapi.RESTSetLimit)}
	}
	cov.Read = &readScope
	if len(gaps) > 0 {
		cov.Status = resourceapi.CoveragePartial
	}
	if cov.Gaps == nil {
		cov.Gaps = []resourceapi.Gap{}
	}
	return cov
}

// resourceListTarget resolves a list's target from the request and the gaps
// exact kind authorization withholds of it.
func (s *Server) resourceListTarget(r *http.Request, cache *k8s.ResourceCache, kindGaps []resourceapi.Gap, clusterScoped bool) listTarget {
	if clusterScoped {
		return listTarget{clusterScoped: true}
	}
	return s.requestListTarget(r, cache).withGaps(kindGaps)
}

// requestListTarget is the part of a list's target that depends only on the
// request, not on the kind.
func (s *Server) requestListTarget(r *http.Request, cache *k8s.ResourceCache) listTarget {
	selection, _ := s.requestNamespaceSelection(r, cache, parseNamespaces(r.URL.Query()))
	t := listTarget{namespaces: selection.Namespaces, named: selection.Named, explicit: selection.Named != nil, narrowedBy: selection.NarrowedBy}
	for _, gap := range selection.Gaps {
		t.gaps = append(t.gaps, gapOf(gap.Code, gap.Namespaces))
	}
	return t
}

// readCache is the typed cache a read would use now. A handler captures it
// before authorizing and checks it again before answering, so authorization,
// coverage and rows all come from the connection the request started on.
func readCache() *k8s.ResourceCache {
	active, syncing := k8s.SnapshotCaches()
	if active != nil {
		return active
	}
	return syncing
}

// withGaps adds the caller's own gaps within namespaces they can see: exact
// exact kind authorization denying the kind there, or a
// check that couldn't complete. Naming them leaks nothing.
func (t listTarget) withGaps(gaps []resourceapi.Gap) listTarget {
	// A namespace already a gap of the target (one that doesn't exist, is
	// hidden or is outside the pin) stays in that one gap.
	claimed := map[string]bool{}
	for _, gap := range t.gaps {
		if gap.Namespaces != nil {
			for _, ns := range gap.Namespaces.Names {
				claimed[ns] = true
			}
		}
	}
	out := slices.Clone(t.gaps)
	for _, gap := range gaps {
		if gap.Namespaces == nil || gap.Namespaces.Extent != "" || len(gap.Namespaces.Names) != gap.Namespaces.Count {
			out = append(out, gap)
			continue
		}
		rest := slices.DeleteFunc(slices.Clone(gap.Namespaces.Names), func(ns string) bool { return claimed[ns] })
		if len(rest) > 0 {
			out = append(out, gapOf(gap.Code, rest))
		}
	}
	t.gaps = out
	return t
}

// typedRead is what a typed kind's cache can serve of a target now.
type typedRead struct {
	// read: the namespaces to read; nil means the whole target.
	read []string
	// wholeKindReady: the kind is ready as a whole, so its listers serve the read; a
	// kind still loading elsewhere is read from its namespaces' own stores.
	wholeKindReady bool
	gaps           []resourceapi.Gap
	// collected, when non-nil, is the part of an implicit target Radar
	// collects the kind in: the read was narrowed to it. uncovered is what it
	// left out of a finite target.
	collected, uncovered []string
}

// narrowedByCollection narrows t to what Radar collects of it: collected when
// t is every namespace, else t without uncovered.
func (t listTarget) narrowedByCollection(collected, uncovered []string) listTarget {
	t.namespaces = (readplan.CollectionScope{Namespaces: collected, Outside: uncovered}).Narrow(t.namespaces)
	if !slices.Contains(t.narrowedBy, resourceapi.NarrowedByNamespaceCollection) {
		t.narrowedBy = append(slices.Clone(t.narrowedBy), resourceapi.NarrowedByNamespaceCollection)
	}
	return t
}

// typedReadCoverage assesses what of namespaces (nil: every namespace) a typed
// kind can be read in now, and why the rest can't. It fails only when nothing
// can be read. Namespaces Radar doesn't collect are gaps when the request
// named them (explicit); otherwise they narrow the read.
func typedReadCoverage(cache *k8s.ResourceCache, resource string, namespaces []string, clusterScoped, explicit bool) (typedRead, error) {
	return typedReadCoverageFrom(cache, cache.PermissionResult(), resource, namespaces, clusterScoped, explicit)
}

func typedReadCoverageFrom(cache *k8s.ResourceCache, permissions *k8s.PermissionCheckResult, resource string, namespaces []string, clusterScoped, explicit bool) (typedRead, error) {
	facts := readplan.Typed(cache, permissions, resource, namespaces, clusterScoped)
	inventory := readplan.ForInventory(facts, namespaces, explicit, clusterScoped)
	plan := typedRead{wholeKindReady: inventory.WholeKindReady}
	if inventory.CanReadTarget && !inventory.All {
		plan.read = inventory.Namespaces
	}
	if inventory.Collection != nil {
		plan.collected, plan.uncovered = inventory.Collection.Namespaces, inventory.Collection.Outside
	}
	for _, part := range inventory.Gaps {
		gap := partitionGap(part)
		if part.Verdict.State == readplan.OutsideCollection {
			at := 0
			for at < len(plan.gaps) && plan.gaps[at].Code == readreason.RadarCredentialsRejected {
				at++
			}
			plan.gaps = slices.Insert(plan.gaps, at, gap)
		} else {
			plan.gaps = append(plan.gaps, gap)
		}
	}
	if inventory.Collection != nil && len(plan.collected) == 0 {
		plan.gaps = append(plan.gaps, gapOf(readreason.RadarNamespaceNotCollected, plan.uncovered))
	}
	if inventory.CanReadTarget {
		return plan, nil
	}
	codes := make([]readreason.Code, 0, len(plan.gaps))
	for _, gap := range plan.gaps {
		codes = append(codes, gap.Code)
	}
	code := readreason.Primary(codes)
	message := fmt.Sprintf("Radar's collector does not cover the requested scope for %s", resource)
	switch code {
	case readreason.RadarForbidden:
		message = fmt.Sprintf("Radar's collector cannot list/watch %s", resource)
	case readreason.RadarKindNotCollected:
		message = fmt.Sprintf("Radar is not watching %s", resource)
	case readreason.RadarCredentialsRejected:
		message = fmt.Sprintf("Kubernetes rejected Radar's collector credentials when it checked its access to %s", resource)
		if cache.KindReadinessFor(resource) == k8s.KindUnavailable {
			message = "Kubernetes rejected Radar's collector credentials"
		}
	case readreason.KindSyncPending:
		message = fmt.Sprintf("%s are still loading, please retry shortly", resource)
	case readreason.KindSyncTimedOut:
		message = fmt.Sprintf("%s did not finish loading within the sync deadline; Radar keeps trying, so retry later", resource)
	}
	if cache.KindReadinessFor(resource) == k8s.KindUnavailable {
		plan.gaps = nil
	}
	return plan, &resourceReadFailure{code: code, message: message, gaps: plan.gaps}
}

// placeGaps gives each gap of t's answer its namespaces, since an axis that
// applies is always present. A gap built without namespaces reached whatever
// of the scope nothing else accounts for: all of it when it is the only part,
// the rest of a listed scope, or an unknown part when the rest can't be told.
// A cluster-scoped answer's gaps have no namespace axis. read is the
// namespaces read; nil when none were.
func (t listTarget) placeGaps(gaps []resourceapi.Gap, read []string) []resourceapi.Gap {
	out := make([]resourceapi.Gap, 0, len(gaps))
	if t.clusterScoped {
		for _, gap := range gaps {
			gap.Namespaces = nil
			out = append(out, gap)
		}
		return out
	}
	unplaced := func(gap resourceapi.Gap) bool { return gap.Namespaces == nil && gap.Objects == nil }
	claimed := map[string]bool{}
	for _, ns := range read {
		claimed[ns] = true
	}
	// The rest of the scope is known only when one gap is unplaced and every
	// other gap lists all its namespaces.
	unplacedCount, unlisted := 0, false
	for _, gap := range gaps {
		switch {
		case unplaced(gap):
			unplacedCount++
		case gap.Namespaces != nil:
			unlisted = unlisted || gap.Namespaces.Extent != "" || len(gap.Namespaces.Names) != gap.Namespaces.Count
			for _, ns := range gap.Namespaces.Names {
				claimed[ns] = true
			}
		}
	}
	remainderKnown := unplacedCount == 1 && !unlisted
	scope := t.namespaces
	if t.explicit {
		scope = t.named
	}
	for _, gap := range gaps {
		if !unplaced(gap) {
			gap.Namespaces = gap.Namespaces.Capped(resourceapi.RESTSetLimit)
			out = append(out, gap)
			continue
		}
		switch {
		case len(gaps) == 1 && len(read) == 0:
			gap.Namespaces = resourceapi.AllNames()
		case scope != nil && remainderKnown:
			rest := slices.DeleteFunc(slices.Clone(scope), func(ns string) bool { return claimed[ns] })
			if len(rest) == 0 {
				gap.Namespaces = resourceapi.UnknownNames()
			} else {
				gap.Namespaces = resourceapi.Names(rest, resourceapi.RESTSetLimit)
			}
		default:
			gap.Namespaces = resourceapi.UnknownNames()
		}
		out = append(out, gap)
	}
	return out
}

func gapOf(code readreason.Code, namespaces []string) resourceapi.Gap {
	gap := resourceapi.Gap{Code: code}
	if len(namespaces) > 0 {
		// Membership stays exact until placeGaps projects the response.
		gap.Namespaces = resourceapi.Names(namespaces, len(namespaces))
	}
	return gap
}

func partitionGap(part readplan.Partition) resourceapi.Gap {
	gap := gapOf(part.Verdict.Code, part.Namespaces)
	switch part.Extent {
	case readplan.UnknownNamespaces:
		gap.Namespaces = resourceapi.UnknownNames()
	case readplan.AllNamespaces:
		gap.Namespaces = resourceapi.AllNames()
	}
	return gap
}

func inventoryGaps(inventory readplan.Inventory) []resourceapi.Gap {
	var gaps []resourceapi.Gap
	for _, part := range inventory.Gaps {
		gaps = append(gaps, partitionGap(part))
	}
	if c := inventory.Collection; c != nil && len(c.Namespaces) == 0 && len(c.Outside) > 0 {
		gaps = append(gaps, gapOf(readreason.RadarNamespaceNotCollected, c.Outside))
	}
	return gaps
}

const resourceCountsNamespaceBudget = 2048

// boundCountCoverageDetails caps the namespace samples of a counts response,
// which carries a coverage per kind, so it can't grow with namespaces × kinds.
func boundCountCoverageDetails(coverage map[string]resourceapi.Coverage) {
	sets := 0
	for _, value := range coverage {
		if value.Scope.Namespaces != nil {
			sets++
		}
		if value.Read != nil && value.Read.Namespaces != nil {
			sets++
		}
		for _, gap := range value.Gaps {
			if gap.Namespaces != nil {
				sets++
			}
		}
	}
	if sets == 0 {
		return
	}
	limit := min(resourceapi.RESTSetLimit, resourceCountsNamespaceBudget/sets)
	for kind, value := range coverage {
		value.Scope.Namespaces = value.Scope.Namespaces.Capped(limit)
		if value.Read != nil {
			read := *value.Read
			read.Namespaces = read.Namespaces.Capped(limit)
			value.Read = &read
		}
		value.Gaps = slices.Clone(value.Gaps)
		for i := range value.Gaps {
			value.Gaps[i].Namespaces = value.Gaps[i].Namespaces.Capped(limit)
		}
		coverage[kind] = value
	}
}
