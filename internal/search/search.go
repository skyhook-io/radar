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
	"github.com/skyhook-io/radar/pkg/resourcecontext"
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
	DynamicResources() ([]schema.GroupVersionResource, error)
	TypedCoverage(kind string, namespaces []string) string
	DynamicObservation(gvr schema.GroupVersionResource) k8score.DynamicResourceObservation
	WarmDynamic(ctx context.Context, gvr schema.GroupVersionResource, namespace string) error
	KindForGVR(gvr schema.GroupVersionResource) string
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
	{"ConfigMap", "configmaps", ""},
	{"Secret", "secrets", ""},
	{"PersistentVolumeClaim", "persistentvolumeclaims", ""},
	{"PersistentVolume", "persistentvolumes", ""},
	{"StorageClass", "storageclasses", "storage.k8s.io"},
	{"HorizontalPodAutoscaler", "hpas", "autoscaling"},
	{"PodDisruptionBudget", "poddisruptionbudgets", "policy"},
	{"Node", "nodes", ""},
	{"Namespace", "namespaces", ""},
	{"Event", "events", ""},
	{"Role", "roles", "rbac.authorization.k8s.io"},
	{"ClusterRole", "clusterroles", "rbac.authorization.k8s.io"},
	{"RoleBinding", "rolebindings", "rbac.authorization.k8s.io"},
	{"ClusterRoleBinding", "clusterrolebindings", "rbac.authorization.k8s.io"},
	{"ServiceAccount", "serviceaccounts", ""},
	{"NetworkPolicy", "networkpolicies", "networking.k8s.io"},
	{"IngressClass", "ingressclasses", "networking.k8s.io"},
	{"LimitRange", "limitranges", ""},
	{"ResourceQuota", "resourcequotas", ""},
}

// Options configures a Search call.
type Options struct {
	Limit   int
	Include IncludeMode
	// Namespaces, when non-empty, scopes typed/dynamic listers to those
	// namespaces. The handler computes this as the intersection of the
	// caller's RBAC-allowed namespaces and any `ns:` modifier in the
	// parsed query, so listers never read namespaces the user can't see.
	// Cluster-scoped kinds ignore this namespace list; SkipKinds and
	// CanReadClusterScoped below are the gates for those resources.
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
	// NamespacePartial reports a requested namespace omitted by caller scope or selection.
	NamespacePartial bool
	// NamespacedRBAC lazily gates sensitive typed kinds after readiness is known.
	// Decisions are empty (allowed), override (scoped subset), skip (denied),
	// or list_error (non-authoritative permission check). The scoped subset
	// may accompany list_error to preserve successfully authorized namespaces.
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
	if opts.Limit <= 0 {
		opts.Limit = DefaultLimit
	}
	if opts.Limit > MaxLimit {
		opts.Limit = MaxLimit
	}

	warmCtx, cancelWarm := context.WithTimeout(ctx, 2*time.Second)
	defer cancelWarm()
	res := Result{Unsearched: []UnsearchedKind{}}
	covered := map[string]bool{}
	markKnown := func(kind, plural string) {
		for _, requested := range q.KindFilter {
			if kindMatches(kind, []string{requested}) || strings.EqualFold(plural, requested) {
				covered[requested] = true
			}
		}
	}
	addGap := func(kind, group, reason string) {
		if reason == "cold" && len(q.KindFilter) == 0 {
			kind, group = "*", ""
		}
		res.addGap(kind, group, reason)
	}
	recordFilterError := func(c candidate, err error) {
		res.Partial = true
		res.FilterErrors++
		if res.FilterErrorSample == "" {
			res.FilterErrorSample = err.Error()
		}
		if len(res.FilterFailedObjects) < 20 {
			res.FilterFailedObjects = append(res.FilterFailedObjects, FailedObject{c.Kind, c.Namespace, c.Name})
		}
	}
	// Buffer hits along with the source object so summaryBuilder (topology
	// lookups, issue-index reads) can run AFTER sort + truncate — without
	// this, broad queries pay topology lookups for thousands of matches
	// only to ship at most opts.Limit of them.
	var pending []pendingHit

	// Typed kinds.
	for _, tk := range typedKinds {
		if !shouldScanTyped(tk.Kind, q) {
			continue
		}
		markKnown(tk.Kind, tk.Plural)
		if opts.NamespacePartial && !isClusterScopedKind(tk.Kind) {
			addGap(tk.Kind, tk.Group, "namespace_excluded")
		}
		if opts.NamespaceExcluded && !isClusterScopedKind(tk.Kind) {
			addGap(tk.Kind, tk.Group, "namespace_excluded")
			continue
		}
		if reason := p.TypedCoverage(tk.Kind, opts.Namespaces); reason != "" && reason != "namespace_scope" {
			addGap(tk.Kind, tk.Group, reason)
			continue
		}
		if opts.SkipKinds[tk.Kind] {
			addGap(tk.Kind, tk.Group, "rbac_denied")
			continue
		}
		// Cluster-scoped kinds ignore the namespace constraint — they're
		// orthogonal to namespace RBAC. Namespaced kinds may have a per-kind
		// override (e.g. user has list-secrets only in a subset of their
		// allowed namespaces); fall back to Options.Namespaces otherwise.
		listNs := opts.Namespaces
		if isClusterScopedKind(tk.Kind) {
			if opts.CanReadClusterScoped != nil {
				allowed, authoritative := opts.CanReadClusterScoped(tk.Kind, tk.Group, tk.Plural)
				if !allowed {
					reason := "rbac_denied"
					if !authoritative {
						reason = "list_error"
					}
					addGap(tk.Kind, tk.Group, reason)
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
				addGap(tk.Kind, tk.Group, "rbac_denied")
			}
			listNs = override
			if len(listNs) == 0 {
				addGap(tk.Kind, tk.Group, "rbac_denied")
				continue
			}
		}
		if opts.NamespacedRBAC != nil && slices.ContainsFunc(NamespacedSearchKinds, func(k struct{ Kind, Group, Resource string }) bool { return k.Kind == tk.Kind }) {
			decision, scoped := opts.NamespacedRBAC(listNs, tk.Group, tk.Plural)
			switch decision {
			case "skip":
				addGap(tk.Kind, tk.Group, "rbac_denied")
				continue
			case "override", "list_error":
				reason := "rbac_denied"
				if decision == "list_error" {
					reason = "list_error"
				}
				addGap(tk.Kind, tk.Group, reason)
				listNs = scoped
				if len(listNs) == 0 {
					continue
				}
			}
		}
		if reason := p.TypedCoverage(tk.Kind, listNs); reason != "" {
			addGap(tk.Kind, tk.Group, reason)
			if reason != "namespace_scope" {
				continue
			}
		}
		objs, err := p.ListTyped(tk.Plural, listNs)
		if err != nil {
			addGap(tk.Kind, tk.Group, listErrorReason(err))
			continue
		}
		res.Searched += len(objs)
		for _, obj := range objs {
			c, ok := fromObject(obj, tk.Kind)
			if !ok {
				continue
			}
			c.Group = tk.Group
			score, matched, snippets, ok := match(q, c)
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
	gvrs, err := p.DynamicResources()
	if err != nil {
		addGap("*", "", listErrorReason(err))
	}
	for _, gvr := range gvrs {
		kind := p.KindForGVR(gvr)
		if kind == "" || !shouldScanCRD(kind, gvr.Group, q) {
			continue
		}
		markKnown(kind, gvr.Resource)
		if k8s.TypedKindOwnsGroup(kind, gvr.Group) || (k8score.IsBuiltInAPIGroup(gvr.Group) && slices.ContainsFunc(typedKinds, func(tk struct{ Kind, Plural, Group string }) bool { return tk.Kind == kind })) {
			continue
		}
		observation := p.DynamicObservation(gvr)
		if observation.State == k8score.DynamicObservationUnsupported && len(q.KindFilter) == 0 {
			continue
		}
		reason := dynamicObservationReason(observation)
		warm := len(q.KindFilter) > 0 && reason == "cold"
		if reason != "" && !warm {
			addGap(kind, gvr.Group, reason)
			continue
		}
		clusterScoped, gvrGroup, gvrResource := classifyDynamicScope(p, gvr, kind)
		if opts.NamespacePartial && !clusterScoped {
			addGap(kind, gvr.Group, "namespace_excluded")
		}
		if opts.NamespaceExcluded && !clusterScoped {
			addGap(kind, gvr.Group, "namespace_excluded")
			continue
		}
		if clusterScoped && opts.CanReadClusterScoped != nil {
			allowed, authoritative := opts.CanReadClusterScoped(kind, gvrGroup, gvrResource)
			if !allowed {
				reason := "rbac_denied"
				if !authoritative {
					reason = "list_error"
				}
				addGap(kind, gvr.Group, reason)
				continue
			}
		}
		namespaces := opts.Namespaces
		if clusterScoped || len(namespaces) == 0 {
			namespaces = []string{""}
		}
		var items []*unstructured.Unstructured
		for _, ns := range namespaces {
			outsideScope := observation.Scope == k8score.DynamicObservationScopeExplicitNamespaces && (ns == "" || !slices.Contains(observation.Namespaces, ns))
			if len(q.KindFilter) > 0 && (warm || outsideScope) {
				if err := p.WarmDynamic(warmCtx, gvr, ns); err != nil {
					if outsideScope {
						addGap(kind, gvr.Group, "namespace_scope")
					}
					reason := dynamicObservationReason(p.DynamicObservation(gvr))
					if reason == "" || reason == "cold" {
						reason = listErrorReason(err)
					}
					addGap(kind, gvr.Group, reason)
					if ns != "" || !outsideScope {
						continue
					}
				} else {
					observation = p.DynamicObservation(gvr)
				}
			}
			if reason := dynamicObservationReason(observation); reason != "" {
				addGap(kind, gvr.Group, reason)
				continue
			}
			if observation.Scope == k8score.DynamicObservationScopeExplicitNamespaces && (ns == "" || !slices.Contains(observation.Namespaces, ns)) {
				addGap(kind, gvr.Group, "namespace_scope")
				if ns != "" {
					continue
				}
				for _, watchedNS := range observation.Namespaces {
					its, err := p.ListDynamic(ctx, gvr, watchedNS)
					if err != nil {
						addGap(kind, gvr.Group, listErrorReason(err))
						continue
					}
					items = append(items, its...)
				}
				continue
			}
			its, err := p.ListDynamic(ctx, gvr, ns)
			if err != nil {
				addGap(kind, gvr.Group, listErrorReason(err))
				continue
			}
			items = append(items, its...)
		}
		res.Searched += len(items)
		for _, u := range items {
			c := fromUnstructured(u, kind, gvr.Group)
			score, matched, snippets, ok := match(q, c)
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

	for _, requested := range q.KindFilter {
		if !covered[requested] {
			addGap(requested, "", "not_indexed")
		}
	}
	sort.Slice(res.Unsearched, func(i, j int) bool {
		a, b := res.Unsearched[i], res.Unsearched[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Group != b.Group {
			return a.Group < b.Group
		}
		return a.Reason < b.Reason
	})
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

func classifyDynamicScope(p Provider, gvr schema.GroupVersionResource, kind string) (bool, string, string) {
	if sp, ok := p.(dynamicScopeProvider); ok {
		if namespaced, known := sp.NamespacedForGVR(gvr); known {
			return !namespaced, gvr.Group, gvr.Resource
		}
	}
	return k8s.ClassifyKindScope(kind, gvr.Group)
}

func shouldScanTyped(kind string, q Query) bool {
	if len(q.KindFilter) > 0 {
		return kindMatches(kind, q.KindFilter)
	}
	// Default skip-list: events are high-volume diagnostic data, not
	// resources users find by name. Honored only when no explicit kind
	// filter is set.
	return strings.ToLower(kind) != "event"
}

func shouldScanCRD(kind, group string, q Query) bool {
	if len(q.KindFilter) > 0 {
		return kindMatches(kind, q.KindFilter)
	}
	return !strings.EqualFold(kind, "Event") || !k8score.IsBuiltInAPIGroup(group)
}

// isClusterScopedKind returns true for the kinds in typedKinds that exist
// outside any namespace. Used to bypass the namespace-list filter for them
// (a cluster-scoped lister rejects a non-empty namespace argument).
func isClusterScopedKind(kind string) bool {
	switch kind {
	case "Node", "Namespace", "PersistentVolume", "StorageClass", "ClusterRole", "ClusterRoleBinding", "IngressClass":
		return true
	}
	return false
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
