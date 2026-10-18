// Package search provides cluster-wide free-text search over typed and
// dynamic-cached resources. It walks the in-memory radar cache, scores
// each object against the parsed query, and returns ranked hits with
// optional minified summaries or raw objects.
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
	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// SummaryBuilderFunc, when supplied via Options.SummaryBuilder, is
// invoked once per matched hit to produce the compact SummaryContext
// attached to the hit's summaryContext field. Exactly one of obj/u will be
// non-nil — typed kinds pass obj, dynamic CRDs pass u. Returning nil
// is fine (the field is omitempty); callers use it to gate context
// emission per request (context=none opts out by passing nil here).
//
// group is the candidate's API group (already known to the search
// walker — typed kinds via typedKinds, CRDs via gvr.Group). Threading
// it through lets the builder distinguish CRDs that share
// kind+namespace+name across groups (e.g. Knative Service vs corev1
// Service) in its per-resource issue index.
type SummaryBuilderFunc func(obj runtime.Object, u *unstructured.Unstructured, group, kind, namespace, name string) *resourcecontext.ResourceSummaryContext

// Provider abstracts the cache so tests can inject a fake.
type Provider interface {
	ListTyped(kind string, namespaces []string) ([]runtime.Object, error)
	ListDynamic(ctx context.Context, gvr schema.GroupVersionResource, namespace string) ([]*unstructured.Unstructured, error)
	// DynamicResources lists the dynamic kinds to search. A non-nil error
	// means the list may be incomplete; any kinds returned are still searched.
	DynamicResources() ([]schema.GroupVersionResource, error)
	TypedCoverage(kind string, namespaces []string) readreason.Code
	DynamicCacheStopped() bool
	// PrepareDynamicObservations indexes informer scopes for this search.
	PrepareDynamicObservations(gvrs []schema.GroupVersionResource)
	// DynamicObservation reports the kind's observation, with readiness taken
	// from the informer serving namespace; "" means the whole kind.
	DynamicObservation(gvr schema.GroupVersionResource, namespace string) k8score.DynamicResourceObservation
	WarmDynamic(ctx context.Context, gvr schema.GroupVersionResource, namespace string) error
	KindForGVR(gvr schema.GroupVersionResource) string
	// NamespaceExists reports whether a namespace exists on the cluster; known
	// is false when Radar can't tell (it doesn't watch Namespaces cluster-wide).
	NamespaceExists(name string) (exists, known bool)
}

// typedReadiness assesses a typed kind over namespaces: the reason it can't
// be read, if any, and whether Radar collects it in only some of them, which
// narrows the search while the rest is still searched. A kind watched
// namespace by namespace can be searched where its informers have synced
// while others still load, so readable names those namespaces.
func typedReadiness(p Provider, kind string, clusterScoped bool, namespaces []string) (readiness readreason.Code, limited bool, readable []string) {
	readiness = p.TypedCoverage(kind, namespaces)
	limited = readiness == readreason.RadarNamespaceNotCollected
	if limited {
		readiness = ""
	}
	if !clusterScoped && (readiness == readreason.KindSyncPending || readiness == readreason.KindSyncTimedOut) {
		if r, ok := p.(typedNamespaceReader); ok {
			readable = r.TypedReadable(kind, namespaces)
			limited = r.TypedCollectionLimited(kind, namespaces)
		}
	}
	return readiness, limited, readable
}

type dynamicScopeProvider interface {
	NamespacedForGVR(gvr schema.GroupVersionResource) (bool, bool)
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

// Options configures a Search call.
type Options struct {
	Limit   int
	Include IncludeMode
	// Namespaces, when non-nil, scopes typed/dynamic listers to those
	// namespaces, and namespaced hits must fall inside it. Handlers set it
	// through ScopeNamespaces: the caller's visible namespaces intersected
	// with any `ns:` modifier, so listers never read namespaces the user
	// can't see. Cluster-scoped kinds ignore this namespace list; SkipKinds
	// and CanReadClusterScoped below are the gates for those resources.
	Namespaces []string
	// SkipKinds suppresses typed kinds by name. Dynamic kinds with colliding
	// names are unaffected; their exact cluster-scope gate applies instead.
	SkipKinds map[string]bool
	// NamespacesByKind, when set for a typed kind, replaces Options.Namespaces
	// for that kind only. Use this when per-kind RBAC narrows access below
	// the namespace-discovery boundary (e.g. user can list pods cluster-wide
	// but secrets only in `team-a`). Cluster-scoped kinds and dynamic CRDs
	// ignore this map. nil entries fall back to Options.Namespaces.
	NamespacesByKind map[string][]string
	// NamespaceExcluded prevents namespaced scans when no requested namespace is visible.
	NamespaceExcluded bool
	// NamespaceCheckFailed means the caller's namespace access couldn't be
	// determined: namespaced kinds are not searched (fail closed), and the
	// result reports permission_check_failed rather than any namespace as
	// excluded.
	NamespaceCheckFailed bool
	// ExcludedNamespaces are namespaces ns: terms named that the caller's RBAC
	// leaves out; reported as namespace_not_visible.
	ExcludedNamespaces []string
	// MissingNamespaces are namespaces ns: terms named that don't exist,
	// found only for a caller who can see every namespace; reported as
	// namespace_not_found.
	MissingNamespaces []string
	// SelectionExcludedNamespaces are namespaces ns: terms named that the
	// caller may see but the saved namespace pick or the --namespace-scope pin
	// leaves out; reported as outside_namespace_selection, or
	// outside_namespace_scope when Pinned.
	SelectionExcludedNamespaces []string
	// Pinned says Radar runs with --namespace-scope, so a namespace selection
	// is the pin rather than the caller's saved pick. Handlers set it before
	// ScopeNamespaces.
	Pinned bool
	// namespaceNarrowing is how the caller's access, the pin or the saved
	// pick narrowed a query that named no namespace.
	namespaceNarrowing []resourceapi.Narrowing
	// seesAllNamespaces records that the caller's RBAC covers every namespace
	// (ScopeNamespaces' ceiling was nil).
	seesAllNamespaces bool
	// NamespacedRBAC gates sensitive typed kinds before their readiness is
	// read. Decisions are empty (allowed), override (scoped subset), skip
	// (denied), or permission_check_failed (non-authoritative permission
	// check). The scoped subset may accompany permission_check_failed to
	// preserve successfully authorized namespaces.
	NamespacedRBAC func(namespaces []string, group, resource string) (decision string, scoped []string)
	// CanReadClusterScoped authorizes cluster-scoped resources before the
	// cache walker scans them. Handlers provide a per-user SAR-backed
	// decision; authoritative distinguishes denial from check failure. nil
	// preserves auth-mode=none behavior, where collector RBAC is the only gate.
	CanReadClusterScoped func(kind, group, resource string) (allowed, authoritative bool)
	// Filter is an optional compiled CEL predicate. When set, each
	// candidate that passed the modifier+token match is also evaluated
	// against the filter; non-truthy results (including eval errors)
	// drop the candidate. Compile happens in the handler; this layer
	// just runs the program.
	Filter *CELFilter
	// SummaryBuilder, when non-nil, is invoked per matched hit to
	// attach the compact summaryContext (managedBy + health +
	// issueCount). Handlers provide a closure that wraps the
	// request-scoped topology + per-namespace issue index so the
	// per-row cost stays flat. Pass nil to opt out (context=none) —
	// the field is omitempty and consumers must tolerate its absence.
	SummaryBuilder SummaryBuilderFunc
}

// Search runs the parsed query against the provider and returns ranked hits.
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

func Search(ctx context.Context, p Provider, q Query, opts Options) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
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
	opts.excludeMissingNamespaces(p, q)
	covered := map[string]bool{}
	markKnown := func(kind, group, plural string) {
		for _, requested := range q.KindFilter {
			if kindMatches(kind, group, plural, []string{requested}) {
				covered[requested] = true
			}
		}
	}
	// namespaced reports the namespaces ns: terms named that the caller's RBAC
	// or the namespace selection left out, and returns false when no namespace
	// is left to scan for kind.
	namespaced := func(kind, group string) bool {
		if opts.NamespaceCheckFailed {
			if len(q.KindFilter) > 0 {
				cov.kind(kind, group, readreason.UserPermissionCheckFailed)
			} else {
				cov.catalog(readreason.UserPermissionCheckFailed)
			}
			return false
		}
		cov.namespaces(readreason.UserNamespaceNotVisible, opts.ExcludedNamespaces...)
		cov.namespaces(readreason.NamespaceNotFound, opts.MissingNamespaces...)
		selection := readreason.OutsideNamespaceSelection
		if opts.Pinned {
			selection = readreason.OutsideNamespacePin
		}
		cov.namespaces(selection, opts.SelectionExcludedNamespaces...)
		return !opts.NamespaceExcluded
	}
	// A broad ns: query asks for objects in those namespaces, which no
	// cluster-scoped object is. A cluster-scoped kind named by kind: ignores
	// ns:, as kubectl ignores -n for it. Selection, scope and RBAC narrowing
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
		readiness, limited, readable := typedReadiness(p, tk.Kind, clusterScoped, opts.Namespaces)
		if readiness != "" && cov.quiet(readiness) {
			continue
		}
		if opts.SkipKinds[tk.Kind] {
			cov.kind(tk.Kind, tk.Group, readreason.UserForbidden)
			continue
		}
		// Cluster-scoped kinds ignore the namespace constraint — they're
		// orthogonal to namespace RBAC. Namespaced kinds may have a per-kind
		// override (e.g. user has list-secrets only in a subset of their
		// allowed namespaces); fall back to Options.Namespaces otherwise.
		listNs := opts.Namespaces
		narrowed := false
		if clusterScoped {
			if opts.CanReadClusterScoped != nil {
				if allowed, authoritative := opts.CanReadClusterScoped(tk.Kind, tk.Group, tk.Plural); !allowed {
					cov.kind(tk.Kind, tk.Group, deniedReason(authoritative))
					continue
				}
			}
			listNs = nil
		} else if override, ok := opts.NamespacesByKind[tk.Kind]; ok && override != nil {
			// nil overrides fall back to Options.Namespaces (per doc): without
			// this guard a nil entry would set listNs=nil and trigger a
			// cluster-wide list — silent bypass of the namespace constraint
			// in security-sensitive code.
			if len(override) < len(opts.Namespaces) {
				cov.kind(tk.Kind, tk.Group, readreason.UserForbidden)
			}
			listNs, narrowed = override, true
			if len(listNs) == 0 {
				cov.kind(tk.Kind, tk.Group, readreason.UserForbidden)
				continue
			}
		}
		if opts.NamespacedRBAC != nil && slices.ContainsFunc(NamespacedSearchKinds, func(k struct{ Kind, Group, Resource string }) bool { return k.Kind == tk.Kind }) {
			decision, scoped := opts.NamespacedRBAC(listNs, tk.Group, tk.Plural)
			switch decision {
			case "skip":
				cov.kind(tk.Kind, tk.Group, readreason.UserForbidden)
				continue
			case "override", "permission_check_failed":
				reason := readreason.UserForbidden
				if decision == "permission_check_failed" {
					reason = readreason.UserPermissionCheckFailed
				}
				cov.kind(tk.Kind, tk.Group, reason)
				listNs, narrowed = scoped, true
				if len(listNs) == 0 {
					continue
				}
			}
		}
		// Namespaces the caller can't list this kind in say nothing about it.
		if narrowed {
			readiness, limited, readable = typedReadiness(p, tk.Kind, clusterScoped, listNs)
			if readiness != "" && cov.quiet(readiness) {
				continue
			}
		}
		// A namespace the query names that Radar doesn't collect is a gap;
		// otherwise what Radar doesn't collect narrowed the search.
		if !clusterScoped && len(q.NSFilter) > 0 {
			for _, ns := range listNs {
				if err := ctx.Err(); err != nil {
					return Result{}, err
				}
				if p.TypedCoverage(tk.Kind, []string{ns}) == readreason.RadarNamespaceNotCollected {
					cov.namespaces(readreason.RadarNamespaceNotCollected, ns)
				}
			}
		} else if !clusterScoped && limited {
			cov.narrow(readreason.RadarNamespaceNotCollected)
		}
		if readiness != "" {
			cov.kind(tk.Kind, tk.Group, readiness)
			if listNs = readableWithin(listNs, readable); len(listNs) == 0 {
				continue
			}
		}
		objs, err := p.ListTyped(tk.Plural, listNs)
		// Partial results are better than blanking the whole search; an
		// explicit kind: filter is told what wasn't read.
		if err != nil {
			cov.kind(tk.Kind, tk.Group, listErrorReason(err))
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
		observation := p.DynamicObservation(gvr, "")
		reason := dynamicObservationReason(observation)
		// A kind: term warms a kind Radar isn't watching, including one whose
		// deferred probe or watch start failed, instead of reporting it.
		probeGap := observation.ProbeRetrying || len(observation.UnansweredNamespaces) > 0 || observation.ReasonCode == "scope_probe_incomplete"
		warm := !observation.ProbeRetrying && len(q.KindFilter) > 0 && (probeGap || reason == readreason.RadarKindNotCollected || observation.State == k8score.DynamicObservationDeferred)
		// Namespace informers sync independently, so while one is still
		// loading the others can answer: their readiness is judged per
		// namespace below.
		perNamespace := probeGap || (observation.State == k8score.DynamicObservationSyncing || observation.State == k8score.DynamicObservationDenied) && observation.Scope == k8score.DynamicObservationScopeExplicitNamespaces
		settled := reason != "" && !warm && !perNamespace
		if settled && cov.quiet(reason) {
			continue
		}
		clusterScoped, gvrGroup, gvrResource := classifyDynamicScope(p, gvr, kind)
		if clusterScoped && skipClusterScoped {
			continue
		}
		if !clusterScoped && !namespaced(kind, gvr.Group) {
			continue
		}
		if settled && reason == readreason.KindNotWatchable {
			cov.kind(kind, gvr.Group, reason)
			continue
		}
		if clusterScoped && opts.CanReadClusterScoped != nil {
			if allowed, authoritative := opts.CanReadClusterScoped(kind, gvrGroup, gvrResource); !allowed {
				cov.kind(kind, gvr.Group, deniedReason(authoritative))
				continue
			}
		}
		if settled {
			cov.kind(kind, gvr.Group, reason)
			continue
		}
		namespaces := opts.Namespaces
		if clusterScoped || len(namespaces) == 0 {
			namespaces = []string{""}
		}
		var items []*unstructured.Unstructured
		read := func(ns string, nsObservation k8score.DynamicResourceObservation) {
			if reason := dynamicObservationReason(nsObservation); reason != "" {
				cov.kind(kind, gvr.Group, reason)
				return
			}
			its, err := p.ListDynamic(ctx, gvr, ns)
			if err != nil {
				cov.kind(kind, gvr.Group, listErrorReason(err))
				return
			}
			items = append(items, its...)
		}
		outside := func(ns string, nsObservation k8score.DynamicResourceObservation) bool {
			_, covered := slices.BinarySearch(nsObservation.Namespaces, ns)
			return nsObservation.Scope == k8score.DynamicObservationScopeExplicitNamespaces && (ns == "" || !covered)
		}
		// A synced cluster-wide informer covers every namespace and cannot be
		// replaced by namespace informers. Probe gaps still need namespace-specific answers.
		clusterWideSynced := observation.Scope == k8score.DynamicObservationScopeCluster && observation.State == k8score.DynamicObservationSynced && !probeGap
		for _, ns := range namespaces {
			if err := ctx.Err(); err != nil {
				return Result{}, err
			}
			if clusterWideSynced && p.DynamicCacheStopped() {
				return Result{}, ErrConnectionChanged
			}
			nsObservation := observation
			if !clusterWideSynced {
				nsObservation = p.DynamicObservation(gvr, ns)
			}
			outsideScope := outside(ns, nsObservation)
			if len(q.KindFilter) > 0 && !nsObservation.ProbeRetrying && (warm || outsideScope) {
				err := p.WarmDynamic(warmCtx, gvr, ns)
				nsObservation = p.DynamicObservation(gvr, ns)
				outsideScope = outside(ns, nsObservation)
				if err != nil {
					reason := dynamicObservationReason(nsObservation)
					if reason == "" || reason == readreason.RadarKindNotCollected {
						reason = listErrorReason(err)
					}
					// Radar's collector being refused one more namespace of a
					// kind it already watches elsewhere is its collection
					// scope, not a denial of the kind.
					if outsideScope && reason == readreason.RadarForbidden {
						cov.namespaces(readreason.RadarNamespaceNotCollected, ns)
					} else {
						cov.kind(kind, gvr.Group, reason)
					}
					if ns != "" || !outsideScope {
						continue
					}
				}
			}
			if nsObservation.ProbeRetrying || len(nsObservation.UnansweredNamespaces) > 0 || nsObservation.ReasonCode == "scope_probe_incomplete" {
				cov.kind(kind, gvr.Group, dynamicObservationReason(nsObservation))
				if ns != "" || !outsideScope {
					continue
				}
				for _, watchedNS := range nsObservation.Namespaces {
					if err := ctx.Err(); err != nil {
						return Result{}, err
					}
					read(watchedNS, p.DynamicObservation(gvr, watchedNS))
				}
				continue
			}
			if !outsideScope {
				read(ns, nsObservation)
				continue
			}
			cov.namespaces(readreason.RadarNamespaceNotCollected, ns)
			if ns == "" {
				for _, watchedNS := range nsObservation.Namespaces {
					if err := ctx.Err(); err != nil {
						return Result{}, err
					}
					read(watchedNS, p.DynamicObservation(gvr, watchedNS))
				}
			}
		}
		res.Searched += len(items)
		var inScope map[string]bool
		if !clusterScoped {
			inScope = namespaceSet(opts.Namespaces)
		}
		for _, u := range items {
			if err := ctx.Err(); err != nil {
				return Result{}, err
			}
			c := fromUnstructured(u, kind, gvr.Group)
			c.Resource = gvr.Resource
			if inScope != nil && !inScope[c.Namespace] {
				continue
			}
			score, matched, snippets, ok := match(queryFor(clusterScoped), c)
			if !ok {
				continue
			}
			if opts.Filter != nil {
				act := unstructuredActivation(u, kind)
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
	if res.FilterErrors > 0 {
		gaps = append(gaps, resourceapi.Gap{Code: readreason.FilterEvalFailed, Objects: resourceapi.Objects(filterFailed, res.FilterErrors, maxFilterFailedObjects)})
		sortGaps(gaps)
	}
	res.Coverage = resourceapi.SearchCoverage{Status: resourceapi.CoverageComplete, NarrowedBy: cov.narrowedBy(opts.namespaceNarrowing), Gaps: gaps}
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

// shouldScanTyped also consults Options.SkipKinds via the closure below
// when invoked from Search; the standalone form here only honors the
// query-derived kind filter.
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
// IncludeNone (identity-only) flat. SummaryContext attachment is NOT
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
