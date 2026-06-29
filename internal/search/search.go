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
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/k8s"
	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
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
	WatchedDynamic() []schema.GroupVersionResource
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
	// SkipKinds names kinds the walker must NOT scan, regardless of
	// query/filter content. The handler populates this from per-user
	// SubjectAccessReviews against sensitive kinds (Node, PersistentVolume,
	// StorageClass, Namespace, and Secrets when the user has no per-namespace
	// access at all) — users without the list verb don't see those rows even
	// if the underlying SA's informer cache holds them. Without this gate, a
	// k8s `view` Cloud viewer would see Secret names, Node IPs, etc. because
	// the cache reads happen as the SA, not the end user.
	SkipKinds map[string]bool
	// NamespacesByKind, when set for a typed kind, replaces Options.Namespaces
	// for that kind only. Use this when per-kind RBAC narrows access below
	// the namespace-discovery boundary (e.g. user can list pods cluster-wide
	// but secrets only in `team-a`). Cluster-scoped kinds and dynamic CRDs
	// ignore this map. nil entries fall back to Options.Namespaces.
	NamespacesByKind map[string][]string
	// CanReadClusterScoped authorizes cluster-scoped resources before the
	// cache walker scans them. Handlers provide a per-user SAR-backed
	// predicate; nil preserves auth-mode=none behavior where the service
	// account's cache permissions are the only gate.
	CanReadClusterScoped func(kind, group, resource string) bool
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

	var res Result
	// Buffer hits along with the source object so summaryBuilder (topology
	// lookups, issue-index reads) can run AFTER sort + truncate — without
	// this, broad queries pay topology lookups for thousands of matches
	// only to ship at most opts.Limit of them.
	var pending []pendingHit
	// CEL filter eval errors are silently dropped per-row (the agent
	// just gets fewer hits, no 500), but we log the first error so an
	// operator can see when rows are dying to runtime issues — typical
	// causes: missing-field traversal (filter assumed a field this
	// kind doesn't carry), type mismatches on dyn-typed nested
	// fields, or cost-limit overruns. Parse/type errors against the
	// declared bindings fail at compile and return 400 before we ever
	// get here. Without this log line, "my filter returns nothing" is
	// indistinguishable from "the cluster has nothing matching" —
	// stats.FilterErrors on the response surfaces the same signal to
	// the agent.
	var firstFilterErr error
	filterErrCount := 0

	// Typed kinds.
	for _, tk := range typedKinds {
		if !shouldScanTyped(tk.Kind, q) {
			continue
		}
		if opts.SkipKinds[tk.Kind] {
			// Per-user RBAC says no — drop the kind entirely whether
			// or not the query asked for it. An explicit `kind:Secret`
			// request from a user who can't list secrets ends up
			// returning zero hits rather than leaking names. Same as
			// the SA-forbidden lister returning ErrForbidden today.
			continue
		}
		// Cluster-scoped kinds ignore the namespace constraint — they're
		// orthogonal to namespace RBAC. Namespaced kinds may have a per-kind
		// override (e.g. user has list-secrets only in a subset of their
		// allowed namespaces); fall back to Options.Namespaces otherwise.
		listNs := opts.Namespaces
		if isClusterScopedKind(tk.Kind) {
			if opts.CanReadClusterScoped != nil && !opts.CanReadClusterScoped(tk.Kind, tk.Group, tk.Plural) {
				continue
			}
			listNs = nil
		} else if override, ok := opts.NamespacesByKind[tk.Kind]; ok && override != nil {
			// nil overrides fall back to Options.Namespaces (per doc): without
			// this guard a nil entry would set listNs=nil and trigger a
			// cluster-wide list — silent bypass of the namespace constraint
			// in security-sensitive code.
			listNs = override
		}
		objs, err := p.ListTyped(tk.Plural, listNs)
		if err != nil {
			// Forbidden / unknown — silently skip this kind, partial
			// results are better than blanking the whole search.
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
					// JSON-marshal of a typed object failing is rare
					// (chan fields / unsupported reflect targets) but
					// silent loss of a row is worse than a log line.
					filterErrCount++
					if firstFilterErr == nil {
						firstFilterErr = fmt.Errorf("activation: %w", err)
					}
					continue
				}
				ok, err := opts.Filter.Match(act)
				if err != nil {
					filterErrCount++
					if firstFilterErr == nil {
						firstFilterErr = err
					}
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

	// Dynamic kinds (CRDs).
	for _, gvr := range p.WatchedDynamic() {
		kind := p.KindForGVR(gvr)
		if kind == "" {
			continue
		}
		if !shouldScanCRD(kind, q) {
			continue
		}
		clusterScoped, gvrGroup, gvrResource := classifyDynamicScope(p, gvr, kind)
		if clusterScoped {
			if opts.CanReadClusterScoped != nil && !opts.CanReadClusterScoped(kind, gvrGroup, gvrResource) {
				continue
			}
		}
		// Namespaced CRDs honor namespace constraints. Cluster-scoped CRDs
		// always list at cluster scope after the SAR predicate above.
		var items []*unstructured.Unstructured
		if clusterScoped || len(opts.Namespaces) == 0 {
			its, err := p.ListDynamic(ctx, gvr, "")
			if err != nil {
				continue
			}
			items = its
		} else {
			for _, ns := range opts.Namespaces {
				its, err := p.ListDynamic(ctx, gvr, ns)
				if err != nil {
					continue
				}
				items = append(items, its...)
			}
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
					// Defensive: u or u.Object was nil. Shouldn't
					// happen for cache-listed objects but a log
					// surfaces an unexpected cache state instead of
					// silently losing rows.
					log.Printf("[search] unexpected nil unstructured for kind=%s gvr=%s", kind, gvr.String())
					continue
				}
				ok, err := opts.Filter.Match(act)
				if err != nil {
					filterErrCount++
					if firstFilterErr == nil {
						firstFilterErr = err
					}
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

	if filterErrCount > 0 {
		log.Printf("[search] CEL filter eval errors: %d rows; first=%v", filterErrCount, firstFilterErr)
		res.FilterErrors = filterErrCount
		if firstFilterErr != nil {
			res.FilterErrorSample = firstFilterErr.Error()
		}
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

// shouldScanTyped also consults Options.SkipKinds via the closure below
// when invoked from Search; the standalone form here only honors the
// query-derived kind filter.
func shouldScanTyped(kind string, q Query) bool {
	if len(q.KindFilter) > 0 {
		return kindMatches(kind, q.KindFilter)
	}
	// Default skip-list: events are high-volume diagnostic data, not
	// resources users find by name. Honored only when no explicit kind
	// filter is set.
	return strings.ToLower(kind) != "event"
}

func shouldScanCRD(kind string, q Query) bool {
	if len(q.KindFilter) > 0 {
		return kindMatches(kind, q.KindFilter)
	}
	return true
}

// isClusterScopedKind returns true for the kinds in typedKinds that exist
// outside any namespace. Used to bypass the namespace-list filter for them
// (a cluster-scoped lister rejects a non-empty namespace argument).
func isClusterScopedKind(kind string) bool {
	switch kind {
	case "Node", "Namespace", "PersistentVolume", "StorageClass":
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
