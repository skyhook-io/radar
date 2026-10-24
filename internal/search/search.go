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
	"sync"
	"sync/atomic"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/k8s"
	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
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
	// KindAccess authorizes namespaced kinds in kindRBACGroups by the
	// caller's own list permission on that kind, on top of Namespaces. nil
	// disables the gate (auth off: the cache's own RBAC is the only filter).
	KindAccess *KindAccess
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

// KindAccess answers per-kind list permission for the caller. Both checks
// are handler-supplied SubjectAccessReviews; they are only asked about kinds
// search gates (kindRBACGroups).
type KindAccess struct {
	// ClusterWide reports whether the caller may list the resource in every
	// namespace.
	ClusterWide func(group, resource string) bool
	// InNamespaces splits namespaces into those where the caller may list
	// the resource and those it could not decide (check budget spent, or the
	// check failed). Namespaces in neither are denied.
	InNamespaces func(group, resource string, namespaces []string) (allowed, unchecked []string)
}

// MaxAccessChecks bounds the uncached SubjectAccessReviews one search may
// send for KindAccess. They share Radar's client rate limit with every other
// screen, and a caller can see hundreds of namespaces.
const MaxAccessChecks = 64

// CheckNamespaces implements KindAccess.InNamespaces over a caller's cached
// decisions and fresh checks. cached returns a remembered decision; check
// runs one and reports whether the apiserver answered. budget is shared by
// one search's calls. A namespace past the budget, or whose check failed,
// comes back unchecked rather than denied.
func CheckNamespaces(namespaces []string, budget *atomic.Int32, cached func(ns string) (allowed, ok bool), check func(ns string) (allowed, answered bool)) (allowed, unchecked []string) {
	var pending []string
	for _, ns := range namespaces {
		if v, ok := cached(ns); ok {
			if v {
				allowed = append(allowed, ns)
			}
			continue
		}
		if budget.Add(-1) < 0 {
			unchecked = append(unchecked, ns)
			continue
		}
		pending = append(pending, ns)
	}
	const maxConcurrent = 16
	sem := make(chan struct{}, maxConcurrent)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, ns := range pending {
		wg.Add(1)
		sem <- struct{}{}
		go func(ns string) {
			defer wg.Done()
			defer func() { <-sem }()
			v, answered := check(ns)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case !answered:
				unchecked = append(unchecked, ns)
			case v:
				allowed = append(allowed, ns)
			}
		}(ns)
	}
	wg.Wait()
	sort.Strings(allowed)
	sort.Strings(unchecked)
	return allowed, unchecked
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
	findings := newFindingsScan(ctx)
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
	coverage := newKindCoverage(q)

	// Typed kinds.
	for _, tk := range typedKinds {
		if !shouldScanTyped(tk.Kind, tk.Group, tk.Plural, q) {
			continue
		}
		coverage.matched(tk.Kind, tk.Group, tk.Plural)
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
			if len(q.NSFilter) > 0 {
				coverage.add(UnsearchedKind{Kind: tk.Kind, Group: tk.Group, Reason: UnsearchedClusterScoped})
			}
		} else if override, ok := opts.NamespacesByKind[tk.Kind]; ok && override != nil {
			// nil overrides fall back to Options.Namespaces (per doc): without
			// this guard a nil entry would set listNs=nil and trigger a
			// cluster-wide list — silent bypass of the namespace constraint
			// in security-sensitive code.
			listNs = override
		}
		objs, err := p.ListTyped(tk.Plural, listNs)
		// Partial results are better than blanking the whole search; an
		// explicit kind: filter is told what wasn't read.
		if err != nil {
			coverage.add(UnsearchedKind{Kind: tk.Kind, Group: tk.Group, Reason: UnsearchedUnreadable})
			continue
		}
		res.Searched += len(objs)
		for _, obj := range objs {
			c, ok := fromObject(obj, tk.Kind)
			if !ok {
				continue
			}
			c.Group = tk.Group
			c.Resource = tk.Plural
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
		if !shouldScanCRD(kind, gvr.Group, gvr.Resource, q) {
			continue
		}
		coverage.matched(kind, gvr.Group, gvr.Resource)
		clusterScoped, gvrGroup, gvrResource := classifyDynamicScope(p, gvr, kind)
		if clusterScoped {
			if opts.CanReadClusterScoped != nil && !opts.CanReadClusterScoped(kind, gvrGroup, gvrResource) {
				continue
			}
			if len(q.NSFilter) > 0 {
				coverage.add(UnsearchedKind{Kind: kind, Group: gvr.Group, Reason: UnsearchedClusterScoped})
			}
		}
		// Namespaced CRDs honor namespace constraints. Cluster-scoped CRDs
		// always list at cluster scope after the SAR predicate above.
		listNs := opts.Namespaces
		gated := !clusterScoped && opts.KindAccess != nil && kindRBACGroups[gvr.Group]
		var items []*unstructured.Unstructured
		if clusterScoped || len(listNs) == 0 {
			its, err := p.ListDynamic(ctx, gvr, "")
			if err != nil {
				coverage.add(UnsearchedKind{Kind: kind, Group: gvr.Group, Reason: UnsearchedUnreadable})
				continue
			}
			items = its
		} else {
			var unreadable []string
			for _, ns := range listNs {
				if err := ctx.Err(); err != nil {
					return Result{}, err
				}
				its, err := p.ListDynamic(ctx, gvr, ns)
				if err != nil {
					unreadable = append(unreadable, ns)
					continue
				}
				items = append(items, its...)
			}
			// A gap only counts where the caller could have read the kind.
			if gated && len(unreadable) > 0 {
				var unchecked []string
				unreadable, unchecked = opts.KindAccess.InNamespaces(gvr.Group, gvr.Resource, unreadable)
				res.AccessIncomplete = res.AccessIncomplete || len(unchecked) > 0
			}
			if len(unreadable) > 0 {
				coverage.add(UnsearchedKind{Kind: kind, Group: gvr.Group, Reason: UnsearchedUnreadable, Namespaces: unreadable})
			}
		}
		// A gated kind is authorized per namespace, over every namespace
		// holding its objects and never over the query's matches, so what
		// the caller is told about unconfirmed access can't depend on report
		// contents they may not read. Fresh checks are capped per search
		// (MaxAccessChecks); objects the caller may not list are dropped
		// before they are counted, filtered or returned.
		if gated && len(q.NSFilter) > 0 {
			// The caller's own ns: filter narrows what is worth authorizing.
			kept := make([]*unstructured.Unstructured, 0, len(items))
			for _, u := range items {
				if sliceContainsFold(q.NSFilter, u.GetNamespace()) {
					kept = append(kept, u)
				}
			}
			items = kept
		}
		if gated && len(items) > 0 {
			if !opts.KindAccess.ClusterWide(gvr.Group, gvr.Resource) {
				allowed, unchecked := opts.KindAccess.InNamespaces(gvr.Group, gvr.Resource, namespacesOf(items))
				items = itemsInNamespaces(items, allowed)
				res.AccessIncomplete = res.AccessIncomplete || len(unchecked) > 0
			}
		}
		res.Searched += len(items)
		for _, u := range items {
			if err := ctx.Err(); err != nil {
				return Result{}, err
			}
			c := identityFromUnstructured(u, kind, gvr.Group)
			c.Resource = gvr.Resource
			if !matchesModifiers(q, c) {
				continue
			}
			if len(q.Tokens) > 0 {
				addUnstructuredContent(&c, u, findings)
			}
			score, matched, snippets, ok := match(q, c)
			if !ok {
				continue
			}
			if opts.Filter != nil {
				act, complete := unstructuredActivation(u, kind, gvr.Group, findings)
				if !complete {
					// Findings limit reached: left out, reported through
					// IncompleteFindings.
					continue
				}
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

	res.IncompleteFindings = findings.reason

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
	res.UnsearchedKinds = coverage.result()
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
	return true
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

// kindCoverage tracks, for a query with kind: filters, which terms matched a
// kind the search scans and which matched kinds it could not fully read.
// A nil *kindCoverage (no kind filter) records nothing.
type kindCoverage struct {
	terms      []string
	hit        map[string]bool
	unsearched []UnsearchedKind
	seen       map[string]bool
}

func newKindCoverage(q Query) *kindCoverage {
	if len(q.KindFilter) == 0 {
		return nil
	}
	return &kindCoverage{terms: q.KindFilter, hit: make(map[string]bool), seen: make(map[string]bool)}
}

func (k *kindCoverage) matched(kind, group, resource string) {
	if k == nil {
		return
	}
	for _, term := range k.terms {
		if kindMatches(kind, group, resource, []string{term}) {
			k.hit[term] = true
		}
	}
}

func (k *kindCoverage) add(u UnsearchedKind) {
	if k == nil {
		return
	}
	key := u.Group + "/" + u.Kind + "/" + u.Reason
	if k.seen[key] {
		return
	}
	k.seen[key] = true
	k.unsearched = append(k.unsearched, u)
}

func (k *kindCoverage) result() []UnsearchedKind {
	if k == nil {
		return nil
	}
	out := k.unsearched
	for _, term := range k.terms {
		if !k.hit[term] {
			out = append(out, UnsearchedKind{Kind: term, Reason: UnsearchedNotIndexed})
		}
	}
	return out
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
