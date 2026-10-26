// Package search provides cluster-wide free-text search over typed and
// dynamic-cached resources. It walks the in-memory radar cache, scores
// each object against the parsed query, and returns ranked hits with
// optional minified summaries or detail payloads.
//
// Search is O(N) per kind: we scan each lister rather than maintaining
// inverted indexes. For radar's typical cluster sizes (≤50K objects)
// this stays well under a second per query and avoids any cache-update
// invalidation bookkeeping.
package search

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/readplan"
	"github.com/skyhook-io/radar/internal/readscope"
	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// SummaryBuilderFunc, when supplied via Options.SummaryBuilder, is
// invoked once per returned hit after sorting and limiting to produce the
// compact SummaryContext attached to the hit's summaryContext field. Exactly
// one of obj/u will be non-nil — typed kinds pass obj, dynamic CRDs pass u. Returning nil
// is fine (the field is omitempty); callers use it to gate context
// emission per request (context=none opts out by passing nil here).
//
// group is the candidate's API group (already known to the search
// walker — typed kinds via typedKinds, CRDs via gvr.Group). Threading
// it through lets the builder distinguish CRDs that share
// kind+namespace+name across groups (e.g. Knative Service vs corev1
// Service) in its per-resource issue index.
type SummaryBuilderFunc func(obj runtime.Object, u *unstructured.Unstructured, group, kind, namespace, name string) *resourcecontext.ResourceSummaryContext

// Provider supplies captured cache handles for one search. Planning, scope
// observations and listing must agree about those handles.
// Returned objects are borrowed and read-only: callers must not mutate or retain
// them beyond the search. Methods do not warm caches except WarmDynamic; plans
// describe observations, while successful lists establish actual read evidence.
type Provider interface {
	// ListTyped accepts a resource lookup key (e.g. "pods", "hpas"), unlike
	// PlanTyped's singular Kind. Nil namespaces means all; non-nil empty means none.
	ListTyped(resource string, namespaces []string) ([]runtime.Object, error)
	// ListDynamic reads authoritative stores without warming. "" means the
	// whole scope; an unavailable or changed serving source must fail the list.
	ListDynamic(ctx context.Context, gvr schema.GroupVersionResource, namespace string) ([]*unstructured.Unstructured, error)
	// DynamicResources lists the dynamic kinds to search. A non-nil error
	// means the list may be incomplete; any kinds returned are still searched.
	DynamicResources() ([]schema.GroupVersionResource, error)
	PlanTyped(kind string, namespaces []string) readplan.Plan
	PlanDynamic(gvr schema.GroupVersionResource, namespaces []string, clusterScoped bool) readplan.Plan
	DynamicCacheStopped() bool
	// PrepareDynamicObservations indexes informer scopes for this search.
	PrepareDynamicObservations(gvrs []schema.GroupVersionResource)
	// NamespaceObservation reports the kind's observation, with readiness taken
	// from the informer serving namespace; "" means the whole kind.
	NamespaceObservation(gvr schema.GroupVersionResource, namespace string) k8score.DynamicResourceObservation
	// WarmDynamic may start a watch and wait for sync, bounded by ctx and two
	// seconds. "" requests the whole scope. Refresh observations and plans afterward.
	WarmDynamic(ctx context.Context, gvr schema.GroupVersionResource, namespace string) error
	// KindForGVR returns "" when discovery cannot resolve the Kind.
	KindForGVR(gvr schema.GroupVersionResource) string
	// KnownMissingNamespaces is called only for callers who can see every namespace.
	KnownMissingNamespaces(names []string) []string
}

type dynamicScopeProvider interface {
	// NamespacedForGVR returns known=false when discovery cannot resolve scope;
	// namespaced is then unusable, rather than evidence of cluster scope.
	NamespacedForGVR(gvr schema.GroupVersionResource) (namespaced, known bool)
}

// typedKinds is the set of typed kinds we walk for unfiltered queries.
// Order is intentional: we scan workloads first (they're what users
// usually ask about) so partial-result truncation favors them.
//
// Events are excluded — they're high-volume diagnostic data, not
// resources users want to find by name. A query with kind:Event still
// scans them because the kind filter overrides the default skip-set.
var typedKinds = []struct {
	Kind   string // singular Kind name for display ("Pod")
	Plural string // lowercase plural for fetch.go ("pods")
	Group  string
}{
	{"Pod", "pods", ""},
	{"Service", "services", ""},
	{"Deployment", "deployments", "apps"},
	{"DaemonSet", "daemonsets", "apps"},
	{"StatefulSet", "statefulsets", "apps"},
	{"ReplicaSet", "replicasets", "apps"},
	{"Job", "jobs", "batch"},
	{"CronJob", "cronjobs", "batch"},
	{"Ingress", "ingresses", "networking.k8s.io"},
	{"NetworkPolicy", "networkpolicies", "networking.k8s.io"},
	{"ConfigMap", "configmaps", ""},
	{"Secret", "secrets", ""},
	{"PersistentVolumeClaim", "persistentvolumeclaims", ""},
	{"PersistentVolume", "persistentvolumes", ""},
	{"StorageClass", "storageclasses", "storage.k8s.io"},
	{"HorizontalPodAutoscaler", "hpas", "autoscaling"},
	{"PodDisruptionBudget", "poddisruptionbudgets", "policy"},
	{"Node", "nodes", ""},
	{"Namespace", "namespaces", ""},
	{"ResourceQuota", "resourcequotas", ""},
	{"ServiceAccount", "serviceaccounts", ""},
	{"IngressClass", "ingressclasses", "networking.k8s.io"},
	{"Event", "events", ""},
	{"Role", "roles", "rbac.authorization.k8s.io"},
	{"ClusterRole", "clusterroles", "rbac.authorization.k8s.io"},
	{"RoleBinding", "rolebindings", "rbac.authorization.k8s.io"},
	{"ClusterRoleBinding", "clusterrolebindings", "rbac.authorization.k8s.io"},
	{"LimitRange", "limitranges", ""},
}

// DynamicKindAccess answers dynamic exact-kind list permission for the caller.
// Both callbacks are required when the gate is non-nil. Both checks
// are handler-supplied SubjectAccessReviews; they are only asked about kinds
// search gates for exact access.
type DynamicKindAccess struct {
	// ClusterWide reports whether the caller may list the resource in every
	// namespace, and whether the check answered.
	ClusterWide func(group, resource string) (allowed, authoritative bool)
	// InNamespaces splits namespaces into those where the caller may list
	// the resource and those it could not decide (check budget spent, or the
	// check failed). Namespaces in neither are denied.
	InNamespaces func(group, resource string, namespaces []string) (allowed, unchecked []string)
}

// pendingHit pairs a Hit with the source object that produced it, so the
// SummaryBuilder (topology lookups, issue-index reads) can be deferred
// until AFTER the hits are sorted and truncated to opts.Limit. Lifecycle is
// strictly internal to Search — never escapes the function.
type pendingHit struct {
	hit Hit
	obj runtime.Object             // typed source (nil for CRD hits)
	u   *unstructured.Unstructured // unstructured source (nil for typed hits)
	c   candidate                  // for c.Group/Kind/Namespace/Name when invoking SummaryBuilder
}

// Search returns ranked hits within opts.Selection, resolved for this query
// and caller against p. It may warm explicitly named dynamic kinds and reports
// per-kind failures as partial coverage. Cancellation and stopped dynamic caches
// return errors. Before publishing, the caller must verify that its captured
// provider is still current (CacheProvider.Current).
func Search(ctx context.Context, p Provider, q Query, opts Options) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	findings := newFindingsScan(ctx)
	if opts.Limit <= 0 {
		opts.Limit = DefaultLimit
	}
	if opts.Limit > MaxLimit {
		opts.Limit = MaxLimit
	}

	warmCtx, cancelWarm := context.WithTimeout(ctx, 2*time.Second)
	defer cancelWarm()
	res := Result{}
	cov := newCoverage(q)
	covered := map[string]bool{}
	markKnown := func(kind, group, plural string) {
		for _, requested := range q.KindFilter {
			if kindMatches(kind, group, plural, []string{requested}) {
				covered[requested] = true
			}
		}
	}
	// namespaced reports the namespaces ns: terms named that the caller's RBAC
	// or the pin left out, and returns false when no namespace is left to scan
	// for kind.
	namespaced := func(kind, group string) bool {
		cov.searchedKinds++
		cov.namespaceKindSeen = map[readreason.Code]map[string]bool{}
		if opts.NamespaceCheckFailed {
			if len(q.KindFilter) > 0 {
				cov.kind(kind, group, readreason.UserPermissionCheckFailed)
			} else {
				cov.catalog(readreason.UserPermissionCheckFailed)
			}
			return false
		}
		for _, gap := range opts.Selection.Gaps {
			cov.namespaces(gap.Code, gap.Namespaces...)
		}
		return !readscope.NoNamespaceAccess(opts.Selection.Namespaces)
	}
	// A broad ns: query asks for objects in those namespaces, which no
	// cluster-scoped object is. A cluster-scoped kind named by kind: ignores
	// ns:, as kubectl ignores -n for it. Pin and RBAC narrowing
	// never live in q.NSFilter, so they never hide cluster-scoped objects.
	skipClusterScoped := len(q.NSFilter) > 0 && len(q.KindFilter) == 0
	clusterQuery := q
	clusterQuery.NSFilter = nil
	queryFor := func(clusterScoped bool) Query {
		if clusterScoped {
			return clusterQuery
		}
		return q
	}
	var findingsIncomplete []resourceapi.KindRef
	var filterFailed []resourceapi.ObjectRef
	recordFilterError := func(c candidate, err error) {
		res.FilterErrors++
		if res.FilterErrorSample == "" {
			res.FilterErrorSample = err.Error()
		}
		if len(filterFailed) < maxFilterFailedObjects {
			filterFailed = append(filterFailed, resourceapi.ObjectRef{Kind: c.Kind, Group: c.Group, Namespace: c.Namespace, Name: c.Name})
		}
	}
	// Buffer hits along with the source object so summaryBuilder (topology
	// lookups, issue-index reads) can run AFTER sort + truncate — without
	// this, broad queries pay topology lookups for thousands of matches
	// only to ship at most opts.Limit of them.
	var pending []pendingHit

	// Coverage order, for typed and dynamic kinds alike: a kind search can't
	// collect at all is reported without asking about the caller. Otherwise the
	// caller's own access is checked first, so a kind the caller can't read is
	// reported as theirs to fix (rbac_denied), never as Radar's denial, still
	// loading or failed.

	// Typed kinds.
	for _, tk := range typedKinds {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		if !shouldScanTyped(tk.Kind, tk.Group, tk.Plural, q) {
			continue
		}
		markKnown(tk.Kind, tk.Group, tk.Plural)
		clusterScoped := isClusterScopedKind(tk.Kind)
		if clusterScoped && skipClusterScoped {
			continue
		}
		if !clusterScoped && !namespaced(tk.Kind, tk.Group) {
			continue
		}
		plan := p.PlanTyped(tk.Kind, opts.Selection.Namespaces)
		sensitive := opts.TypedKindAccess != nil && k8s.RequiresExactNamespaceAccess(schema.GroupVersionResource{Group: tk.Group, Resource: tk.Plural})
		if !sensitive && len(plan.Partitions) == 1 && plan.Partitions[0].Verdict.State == readplan.Gap && cov.quiet(plan.Partitions[0].Verdict.Code) {
			continue
		}
		listNs := opts.Selection.Namespaces
		narrowed := false
		scopeNarrowed := false
		if clusterScoped {
			if opts.CanReadClusterScoped != nil {
				if allowed, authoritative := opts.CanReadClusterScoped(tk.Kind, tk.Group, tk.Plural); !allowed {
					cov.kind(tk.Kind, tk.Group, deniedReason(authoritative))
					continue
				}
			}
			listNs = nil
		}
		if sensitive {
			allowed, denied, unchecked := opts.TypedKindAccess(listNs, tk.Group, tk.Plural)
			if len(denied) > 0 {
				cov.kindGap(tk.Kind, tk.Group, readreason.UserForbidden, scopeNarrowed || len(allowed) > 0 || len(unchecked) > 0)
			}
			if len(unchecked) > 0 {
				cov.kindGap(tk.Kind, tk.Group, readreason.UserPermissionCheckFailed, scopeNarrowed || len(allowed) > 0 || len(denied) > 0)
			}
			if len(denied) > 0 || len(unchecked) > 0 {
				listNs, narrowed, scopeNarrowed = allowed, true, true
				if len(listNs) == 0 {
					continue
				}
			}
		}
		if narrowed {
			plan = p.PlanTyped(tk.Kind, listNs)
		}
		if plan.CollectionLimited && !clusterScoped {
			cov.narrow(readreason.RadarNamespaceNotCollected)
		}
		readable := []string{}
		hasGap := false
		for _, part := range plan.Partitions {
			if err := ctx.Err(); err != nil {
				return Result{}, err
			}
			switch part.Verdict.State {
			case readplan.Readable:
				readable = append(readable, part.Namespaces...)
			case readplan.OutsideCollection:
				hasGap = true
				cov.kindNamespaces(readreason.RadarNamespaceNotCollected, part.Namespaces...)
			case readplan.Gap:
				hasGap = true
				cov.kindGap(tk.Kind, tk.Group, part.Verdict.Code, scopeNarrowed || partOfScope(plan, part.Verdict.Code))
			}
		}
		if hasGap {
			listNs = readableWithin(listNs, readable)
			if len(listNs) == 0 {
				continue
			}
		}
		objs, err := p.ListTyped(tk.Plural, listNs)
		// Partial results are better than blanking the whole search; an
		// explicit kind: filter is told what wasn't read.
		if err != nil {
			cov.kindGap(tk.Kind, tk.Group, listErrorReason(err), scopeNarrowed || hasGap || plan.CollectionLimited)
			continue
		}
		res.Searched += len(objs)
		inScope := namespaceSet(listNs)
		for _, obj := range objs {
			if err := ctx.Err(); err != nil {
				return Result{}, err
			}
			c, ok := fromObject(obj, tk.Kind)
			if !ok || (inScope != nil && !inScope[c.Namespace]) {
				continue
			}
			c.Group = tk.Group
			c.Resource = tk.Plural
			score, matched, snippets, ok := match(queryFor(clusterScoped), c)
			if !ok {
				continue
			}
			if opts.Filter != nil {
				act, err := objectActivation(obj, tk.Kind)
				if err != nil {
					recordFilterError(c, fmt.Errorf("activation: %w", err))
					continue
				}
				ok, err := opts.Filter.Match(act)
				if err != nil {
					recordFilterError(c, err)
					continue
				}
				if !ok {
					continue
				}
			}
			pending = append(pending, pendingHit{
				hit: buildHit(score, matched, snippets, c, opts.Include, obj, nil),
				obj: obj,
				c:   c,
			})
		}
	}

	// Dynamic kinds (CRDs and built-ins without typed listers).
	gvrs, catalogErr := p.DynamicResources()
	incompleteDiscovery := errors.Is(catalogErr, errIncompleteDiscovery)
	if catalogErr != nil && !incompleteDiscovery {
		cov.catalog(listErrorReason(catalogErr))
	} else if incompleteDiscovery && len(q.KindFilter) == 0 {
		cov.narrowed[resourceapi.NarrowedByKindCollection] = true
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	p.PrepareDynamicObservations(gvrs)
	for _, gvr := range gvrs {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		kind := p.KindForGVR(gvr)
		if kind == "" || !shouldScanCRD(kind, gvr.Group, gvr.Resource, q) {
			continue
		}
		markKnown(kind, gvr.Group, gvr.Resource)
		if k8s.TypedKindOwnsGroup(kind, gvr.Group) || (k8score.IsBuiltInAPIGroup(gvr.Group) && slices.ContainsFunc(typedKinds, func(tk struct{ Kind, Plural, Group string }) bool { return tk.Kind == kind })) {
			continue
		}
		if p.DynamicCacheStopped() {
			return Result{}, ErrConnectionChanged
		}
		observation := p.NamespaceObservation(gvr, "")
		clusterScoped, gvrGroup, gvrResource := classifyDynamicScope(p, gvr, kind)
		probeGap := observation.ProbeRetrying || len(observation.UnansweredNamespaces) > 0 || observation.ReasonCode == "scope_probe_incomplete"
		clusterWideSynced := observation.Scope == k8score.DynamicObservationScopeCluster && observation.State == k8score.DynamicObservationSynced && !probeGap
		planNamespaces := opts.Selection.Namespaces
		if clusterWideSynced {
			planNamespaces = nil
		}
		plan := p.PlanDynamic(gvr, planNamespaces, clusterScoped)
		var codes []readreason.Code
		for _, part := range plan.Partitions {
			codes = append(codes, part.Verdict.Code)
		}
		reason := readreason.Primary(codes)
		warm := !observation.ProbeRetrying && len(q.KindFilter) > 0 && (probeGap || observation.State == k8score.DynamicObservationUnwatched || observation.State == k8score.DynamicObservationDeferred)
		perNamespace := probeGap || observation.Scope == k8score.DynamicObservationScopeExplicitNamespaces
		settled := reason != "" && !warm && !perNamespace
		gated := !clusterScoped && opts.DynamicKindAccess != nil && k8s.RequiresExactNamespaceAccess(gvr)
		if settled && !gated && cov.quiet(reason) {
			continue
		}
		if clusterScoped && skipClusterScoped {
			continue
		}
		if !clusterScoped && !namespaced(kind, gvr.Group) {
			continue
		}
		if reason == readreason.KindNotWatchable || reason == readreason.KindNotSearchable {
			cov.kind(kind, gvr.Group, reason)
			continue
		}
		if clusterScoped && opts.CanReadClusterScoped != nil {
			if allowed, authoritative := opts.CanReadClusterScoped(kind, gvrGroup, gvrResource); !allowed {
				cov.kind(kind, gvr.Group, deniedReason(authoritative))
				continue
			}
		}
		namespaces := opts.Selection.Namespaces
		if gated && len(q.NSFilter) > 0 {
			namespaces = readableWithin(namespaces, q.NSFilter)
		}
		accessNarrowed := false
		clusterAllowed, clusterAuthoritative := true, true
		if gated {
			clusterAllowed, clusterAuthoritative = opts.DynamicKindAccess.ClusterWide(gvr.Group, gvr.Resource)
		}
		checkItems := gated && !clusterAllowed
		unknownAccessScope := checkItems && namespaces == nil
		if checkItems {
			if namespaces == nil {
				// Namespace membership cannot enumerate a cluster-wide reader's
				// exact-kind scope. Only individually authorized items can be kept.
				cov.kindGap(kind, gvr.Group, deniedReason(clusterAuthoritative), true)
				accessNarrowed = true
			} else {
				allowed, unchecked := opts.DynamicKindAccess.InNamespaces(gvr.Group, gvr.Resource, namespaces)
				denied := len(namespaces) - len(allowed) - len(unchecked)
				if denied > 0 {
					cov.kindGap(kind, gvr.Group, readreason.UserForbidden, denied < len(namespaces))
				}
				if len(unchecked) > 0 {
					cov.kindGap(kind, gvr.Group, readreason.UserPermissionCheckFailed, len(unchecked) < len(namespaces))
				}
				accessNarrowed = len(allowed) < len(namespaces)
				namespaces = allowed
				if len(namespaces) == 0 {
					continue
				}
			}
		}
		kindFailures := map[readreason.Code]map[string]bool{}
		kindGap := func(ns string, reason readreason.Code) {
			if unknownAccessScope {
				if ns == "" {
					return
				}
				allowed, unchecked := opts.DynamicKindAccess.InNamespaces(gvr.Group, gvr.Resource, []string{ns})
				if len(unchecked) > 0 {
					reason = readreason.UserPermissionCheckFailed
				} else if len(allowed) == 0 {
					reason = readreason.UserForbidden
				}
			}
			if kindFailures[reason] == nil {
				kindFailures[reason] = map[string]bool{}
			}
			kindFailures[reason][ns] = true
		}
		recordKindFailures := func() {
			for reason, failed := range kindFailures {
				someNamespaces := accessNarrowed || plan.CollectionLimited || slices.ContainsFunc(plan.Partitions, func(part readplan.Partition) bool { return part.Extent == readplan.UnknownNamespaces })
				if !failed[""] {
					someNamespaces = someNamespaces || namespaces == nil || len(failed) != len(namespaces)
				}
				cov.kindGap(kind, gvr.Group, reason, someNamespaces)
			}
		}
		if settled {
			kindGap("", reason)
			recordKindFailures()
			continue
		}
		if clusterScoped || namespaces == nil {
			namespaces = []string{""}
		}
		byNamespace := map[string]readplan.Verdict{}
		for _, part := range plan.Partitions {
			for _, ns := range part.Namespaces {
				byNamespace[ns] = part.Verdict
			}
		}
		var items []*unstructured.Unstructured
		for _, ns := range namespaces {
			if err := ctx.Err(); err != nil {
				return Result{}, err
			}
			if p.DynamicCacheStopped() {
				return Result{}, ErrConnectionChanged
			}
			nsObservation := observation
			if !clusterWideSynced {
				nsObservation = p.NamespaceObservation(gvr, ns)
			}
			_, covered := slices.BinarySearch(nsObservation.Namespaces, ns)
			outsideScope := nsObservation.Scope == k8score.DynamicObservationScopeExplicitNamespaces && (ns == "" || !covered)
			var warmErr error
			warmed := false
			if len(q.KindFilter) > 0 && !unknownAccessScope && !nsObservation.ProbeRetrying && (warm || outsideScope) {
				warmed = true
				warmErr = p.WarmDynamic(warmCtx, gvr, ns)
				nsObservation = p.NamespaceObservation(gvr, ns)
				_, covered = slices.BinarySearch(nsObservation.Namespaces, ns)
				outsideScope = nsObservation.Scope == k8score.DynamicObservationScopeExplicitNamespaces && (ns == "" || !covered)
			}
			current := plan
			if verdict, ok := byNamespace[ns]; ok {
				current = readplan.Plan{Partitions: []readplan.Partition{{Extent: readplan.NamespaceSet, Namespaces: []string{ns}, Verdict: verdict}}}
			}
			if warmed {
				target := []string{ns}
				if ns == "" {
					target = nil
				}
				current = p.PlanDynamic(gvr, target, clusterScoped)
			}
			if ns == "" && current.CollectionLimited && !nsObservation.ProbeRetrying && len(nsObservation.UnansweredNamespaces) == 0 && nsObservation.ReasonCode != "scope_probe_incomplete" {
				cov.kindNamespaces(readreason.RadarNamespaceNotCollected, "")
			}
			if warmErr != nil {
				var codes []readreason.Code
				for _, part := range current.Partitions {
					codes = append(codes, part.Verdict.Code)
				}
				code := readreason.Primary(codes)
				if code == "" || code == readreason.RadarKindNotCollected || code == readreason.RadarNamespaceNotCollected {
					code = listErrorReason(warmErr)
				}
				if code == readreason.ConnectionChanged {
					return Result{}, ErrConnectionChanged
				}
				if outsideScope && code == readreason.RadarForbidden {
					cov.kindNamespaces(readreason.RadarNamespaceNotCollected, ns)
				} else {
					kindGap(ns, code)
				}
				if ns != "" || !outsideScope {
					continue
				}
			}
			for _, part := range current.Partitions {
				if err := ctx.Err(); err != nil {
					return Result{}, err
				}
				code := part.Verdict.Code
				state := part.Verdict.State
				if code == readreason.ConnectionChanged {
					return Result{}, ErrConnectionChanged
				}
				targets := part.Namespaces
				if part.Extent != readplan.NamespaceSet {
					targets = []string{ns}
				}
				for _, target := range targets {
					if err := ctx.Err(); err != nil {
						return Result{}, err
					}
					if p.DynamicCacheStopped() {
						return Result{}, ErrConnectionChanged
					}
					switch state {
					case readplan.OutsideCollection:
						cov.kindNamespaces(readreason.RadarNamespaceNotCollected, target)
					case readplan.Gap:
						if code == readreason.RadarForbidden && outsideScope && !slices.Contains(nsObservation.Namespaces, target) {
							cov.kindNamespaces(readreason.RadarNamespaceNotCollected, target)
							continue
						}
						gapNS := target
						if ns == "" && !slices.Contains(nsObservation.Namespaces, target) {
							gapNS = ""
						}
						kindGap(gapNS, code)
					case readplan.Readable:
						its, err := p.ListDynamic(ctx, gvr, target)
						if err != nil {
							kindGap(target, listErrorReason(err))
							continue
						}
						items = append(items, its...)
					}
				}
			}
		}
		recordKindFailures()
		if gated && len(q.NSFilter) > 0 {
			kept := make([]*unstructured.Unstructured, 0, len(items))
			for _, u := range items {
				if sliceContainsFold(q.NSFilter, u.GetNamespace()) {
					kept = append(kept, u)
				}
			}
			items = kept
		}
		if checkItems && !unknownAccessScope {
			items = itemsInNamespaces(items, namespaces)
		} else if checkItems && len(items) > 0 {
			allowed, unchecked := opts.DynamicKindAccess.InNamespaces(gvr.Group, gvr.Resource, namespacesOf(items))
			items = itemsInNamespaces(items, allowed)
			if len(unchecked) > 0 {
				cov.kindGap(kind, gvr.Group, readreason.UserPermissionCheckFailed, accessNarrowed || len(allowed) > 0)
			}
		}
		res.Searched += len(items)
		var inScope map[string]bool
		if !clusterScoped {
			inScope = namespaceSet(opts.Selection.Namespaces)
		}
		for _, u := range items {
			if err := ctx.Err(); err != nil {
				return Result{}, err
			}
			c := identityFromUnstructured(u, kind, gvr.Group)
			c.Resource = gvr.Resource
			if inScope != nil && !inScope[c.Namespace] {
				continue
			}
			// The hard filters read identity only, so an object they reject
			// never has its content (or a Trivy report's findings) built.
			query := queryFor(clusterScoped)
			if !matchesModifiers(query, c) {
				continue
			}
			if len(q.Tokens) > 0 {
				addUnstructuredContent(&c, u, findings)
			}
			score, matched, snippets, ok := match(query, c)
			if !ok {
				continue
			}
			if opts.Filter != nil {
				act, complete := unstructuredActivation(u, kind, gvr.Group, findings)
				if !complete {
					// The findings budget ran out before this report: it
					// is left out, and the kind reported below.
					continue
				}
				if act == nil {
					recordFilterError(c, fmt.Errorf("activation: object is unavailable"))
					continue
				}
				ok, err := opts.Filter.Match(act)
				if err != nil {
					recordFilterError(c, err)
					continue
				}
				if !ok {
					continue
				}
			}
			pending = append(pending, pendingHit{
				hit: buildHit(score, matched, snippets, c, opts.Include, nil, u),
				u:   u,
				c:   c,
			})
		}
		// Once the findings budget runs out, a report's findings are no
		// longer searched, so a CVE or package missing from the hits may be
		// in one this kind left out.
		if gvr.Group == trivyGroup && findings.exhausted() {
			findingsIncomplete = append(findingsIncomplete, resourceapi.KindRef{Kind: kind, Group: gvr.Group})
		}
	}

	// While the catalog is loading, a kind: term that matched nothing may
	// simply not be discovered yet, so it is not called unknown. When some API
	// groups failed discovery it is, but the failure is reported beside it:
	// the kind may live in one of them.
	if catalogErr == nil || incompleteDiscovery {
		unknownKinds := map[string]bool{}
		var unlistable []k8score.APIResource
		if u, ok := p.(unlistableKindProvider); ok && len(q.KindFilter) > 0 {
			unlistable = u.UnlistableKinds()
		}
		for _, requested := range q.KindFilter {
			if err := ctx.Err(); err != nil {
				return Result{}, err
			}
			key := strings.ToLower(requested)
			if !covered[requested] && !unknownKinds[key] {
				unknownKinds[key] = true
				// An installed API that can't be listed, such as a create-only
				// review, exists: it is just not something search can read.
				found := false
				for _, ar := range unlistable {
					if kindMatches(ar.Kind, ar.Group, ar.Name, []string{requested}) {
						cov.kind(ar.Kind, ar.Group, readreason.KindNotWatchable)
						found = true
					}
				}
				if found {
					continue
				}
				cov.kind(requested, "", readreason.KindUnknown)
				if incompleteDiscovery {
					cov.catalog(readreason.ReadError)
				}
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	gaps := cov.result()
	if len(findingsIncomplete) > 0 {
		gaps = append(gaps, resourceapi.Gap{Code: readreason.FindingsScanIncomplete, Kinds: resourceapi.Kinds(findingsIncomplete, len(findingsIncomplete)), Namespaces: resourceapi.UnknownNames()})
		sortGaps(gaps)
	}
	if res.FilterErrors > 0 {
		gaps = append(gaps, resourceapi.Gap{Code: readreason.FilterEvalFailed, Objects: resourceapi.Objects(filterFailed, res.FilterErrors, maxFilterFailedObjects)})
		sortGaps(gaps)
	}
	res.Coverage = resourceapi.SearchCoverage{Status: resourceapi.CoverageComplete, NarrowedBy: cov.narrowedBy(opts.Selection.NarrowedBy), Gaps: gaps}
	if len(gaps) > 0 {
		res.Coverage.Status = resourceapi.CoveragePartial
	}
	if res.FilterErrors > 0 {
		log.Printf("[search] CEL filter eval errors: %d rows; first=%s", res.FilterErrors, res.FilterErrorSample)
	}

	// Dedup before sorting. A resource can land in the pending slice
	// twice when it's reachable via both the typed loop (Deployment,
	// Pod, …) and the dynamic loop (an integration registered
	// Deployment/Pod as a watched GVR — e.g. a controller indexing
	// built-in workloads as dynamic resources). Without dedup the table
	// shows visible doubles. Keep the highest-scoring instance so the
	// typed-path match (which usually has richer per-kind scoring) wins
	// ties.
	if len(pending) > 1 {
		type hitKey struct{ kind, group, ns, name string }
		seen := make(map[hitKey]int, len(pending))
		out := pending[:0]
		for _, p := range pending {
			if err := ctx.Err(); err != nil {
				return Result{}, err
			}
			k := hitKey{p.hit.Kind, p.hit.Group, p.hit.Namespace, p.hit.Name}
			if idx, ok := seen[k]; ok {
				if p.hit.Score > out[idx].hit.Score {
					out[idx] = p
				}
				continue
			}
			seen[k] = len(out)
			out = append(out, p)
		}
		pending = out
	}

	sort.SliceStable(pending, func(i, j int) bool {
		if pending[i].hit.Score != pending[j].hit.Score {
			return pending[i].hit.Score > pending[j].hit.Score
		}
		if pending[i].hit.Kind != pending[j].hit.Kind {
			return pending[i].hit.Kind < pending[j].hit.Kind
		}
		if pending[i].hit.Namespace != pending[j].hit.Namespace {
			return pending[i].hit.Namespace < pending[j].hit.Namespace
		}
		return pending[i].hit.Name < pending[j].hit.Name
	})
	res.TotalMatched = len(pending)
	if len(pending) > opts.Limit {
		pending = pending[:opts.Limit]
	}

	// Summary attach happens HERE — after truncation — so the topology
	// lookups + issue-index reads only run for the hits we'll actually
	// ship. Skipped entirely when SummaryBuilder is nil (caller opted out
	// via context=none).
	hits := make([]Hit, len(pending))
	for i := range pending {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		hits[i] = pending[i].hit
		if opts.SummaryBuilder != nil {
			c := pending[i].c
			hits[i].SummaryContext = opts.SummaryBuilder(pending[i].obj, pending[i].u, c.Group, c.Kind, c.Namespace, c.Name)
		}
	}
	res.Hits = hits
	res.Total = len(hits)
	return res, nil
}

// deniedReason names a refused caller permission check: a denial, or a check
// that could not complete.
func deniedReason(authoritative bool) readreason.Code {
	if authoritative {
		return readreason.UserForbidden
	}
	return readreason.UserPermissionCheckFailed
}

func classifyDynamicScope(p Provider, gvr schema.GroupVersionResource, kind string) (bool, string, string) {
	if sp, ok := p.(dynamicScopeProvider); ok {
		if namespaced, known := sp.NamespacedForGVR(gvr); known {
			return !namespaced, gvr.Group, gvr.Resource
		}
	}
	return k8s.ClassifyKindScope(kind, gvr.Group)
}

func shouldScanTyped(kind, group, resource string, q Query) bool {
	if len(q.KindFilter) > 0 {
		return kindMatches(kind, group, resource, q.KindFilter)
	}
	// Default skip-list: events are high-volume diagnostic data, not
	// resources users find by name. Honored only when no explicit kind
	// filter is set.
	return strings.ToLower(kind) != "event"
}

func shouldScanCRD(kind, group, resource string, q Query) bool {
	if len(q.KindFilter) > 0 {
		return kindMatches(kind, group, resource, q.KindFilter)
	}
	return !strings.EqualFold(kind, "Event") || !k8score.IsBuiltInAPIGroup(group)
}

// namespaceSet returns nil for nil (every namespace) and a possibly empty set otherwise.
func namespaceSet(namespaces []string) map[string]bool {
	if namespaces == nil {
		return nil
	}
	set := make(map[string]bool, len(namespaces))
	for _, ns := range namespaces {
		set[ns] = true
	}
	return set
}

// isClusterScopedKind reports whether a kind in typedKinds exists outside
// any namespace. Used to bypass the namespace-list filter for them (a
// cluster-scoped lister rejects a non-empty namespace argument) and to gate
// them on CanReadClusterScoped, so it comes from the built-in catalogue
// rather than a hand-kept list.
func isClusterScopedKind(kind string) bool {
	b, ok := resourceid.BuiltinForKind(kind)
	return ok && !b.Namespaced
}

// buildHit assembles the response shape for a matched candidate. Exactly
// one of obj/u will be non-nil. minify-on-demand keeps the cost of
// IncludeNone (identity and match evidence) flat. SummaryContext attachment is NOT
// done here — it happens in Search's post-truncation loop so the
// expensive topology lookups + issue-index reads only run for the hits
// that survive sort + Limit truncation.
func buildHit(score int, matched []MatchedField, snippets []MatchSnippet, c candidate, mode IncludeMode, obj runtime.Object, u *unstructured.Unstructured) Hit {
	h := Hit{
		Score:     score,
		Kind:      c.Kind,
		Group:     c.Group,
		Namespace: c.Namespace,
		Name:      c.Name,
		Matched:   matched,
		Snippets:  snippets,
	}
	switch mode {
	case IncludeSummary:
		if obj != nil {
			k8s.SetTypeMeta(obj)
			if s, err := aicontext.Minify(obj, aicontext.LevelSummary); err == nil {
				h.Summary = s
			}
		} else if u != nil {
			h.Summary = aicontext.MinifyUnstructured(u, aicontext.LevelSummary)
		}
	case IncludeRaw:
		if obj != nil {
			k8s.SetTypeMeta(obj)
			if s, err := aicontext.Minify(obj, aicontext.LevelDetail); err == nil {
				h.Raw = s
			}
		} else if u != nil {
			h.Raw = aicontext.MinifyUnstructured(u, aicontext.LevelDetail)
		}
	}
	return h
}

func namespacesOf(items []*unstructured.Unstructured) []string {
	seen := make(map[string]bool)
	var out []string
	for _, u := range items {
		if ns := u.GetNamespace(); ns != "" && !seen[ns] {
			seen[ns] = true
			out = append(out, ns)
		}
	}
	sort.Strings(out)
	return out
}

func itemsInNamespaces(items []*unstructured.Unstructured, namespaces []string) []*unstructured.Unstructured {
	allowed := make(map[string]bool, len(namespaces))
	for _, ns := range namespaces {
		allowed[ns] = true
	}
	out := items[:0:0]
	for _, u := range items {
		if allowed[u.GetNamespace()] {
			out = append(out, u)
		}
	}
	return out
}
