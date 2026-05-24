// Pure-function tests for the shared summarycontext core. The
// REST/MCP-specific wiring tests (attachSummaryContextToList,
// dispatch-on-CanReadClusterScoped, the ai-handler issueIndexNamespaces
// helper) stay at their respective handler sites in internal/server
// and internal/mcp.

package summarycontext

import (
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/policyreports"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	"github.com/skyhook-io/radar/pkg/topology"
)

// fakeIssuesProvider is a minimal issues.Provider for the BuildIssueIndex
// tests. Only the fields the index path touches are wired.
//
// DetectProblems mirrors CacheProvider.DetectProblems: empty namespaces
// returns the full set; a non-empty slice drops cluster-scoped rows
// (Namespace=="") to match the production flattenNamespacedProblems
// behavior — needed so the cluster-scoped-filter regression test can
// pin the actual bug.
type fakeIssuesProvider struct {
	problems []k8s.Problem
}

func (f *fakeIssuesProvider) DetectProblems(namespaces []string) []k8s.Problem {
	if len(namespaces) == 0 {
		return f.problems
	}
	allowed := map[string]bool{}
	for _, ns := range namespaces {
		allowed[ns] = true
	}
	out := make([]k8s.Problem, 0, len(f.problems))
	for _, p := range f.problems {
		if p.Namespace == "" {
			continue
		}
		if allowed[p.Namespace] {
			out = append(out, p)
		}
	}
	return out
}
func (f *fakeIssuesProvider) DetectCAPIProblems(_ []string) []k8s.Problem { return nil }
func (f *fakeIssuesProvider) DetectMissingRefs(_ []string) []k8s.Problem  { return nil }
func (f *fakeIssuesProvider) WarningEvents(_ []string, _ time.Duration) []*corev1.Event {
	return nil
}
func (f *fakeIssuesProvider) WatchedDynamic() []schema.GroupVersionResource { return nil }
func (f *fakeIssuesProvider) ListDynamic(_ schema.GroupVersionResource, _ string) ([]*unstructured.Unstructured, error) {
	return nil, nil
}
func (f *fakeIssuesProvider) KindForGVR(_ schema.GroupVersionResource) string { return "" }
func (f *fakeIssuesProvider) KyvernoFindings() []policyreports.SubjectFindings { return nil }
func (f *fakeIssuesProvider) KyvernoStatus() string                              { return "" }

func fmtPodName(i int) string { return fmt.Sprintf("pod-%05d", i) }

// TestIssueIndexKey_GroupAware pins that two resources sharing
// kind+namespace+name but in different API groups get independent
// counts. Without group in the key, e.g. Knative serving.knative.dev/
// Service vs corev1 ""/Service collapse onto one bucket — and either
// the CRD inherits the core Service's count or vice versa. This breaks
// the moment a user has two operators each shipping a kind named
// "Cluster" in the same namespace.
func TestIssueIndexKey_GroupAware(t *testing.T) {
	idx := IssueIndex{}
	// Same kind+ns+name, different groups — must be independent buckets.
	idx[issueIndexKey("", "Service", "prod", "api")] = 2
	idx[issueIndexKey("serving.knative.dev", "Service", "prod", "api")] = 5

	if got := idx.Count("", "Service", "prod", "api"); got != 2 {
		t.Errorf("core Service count = %d, want 2 (Knative bucket bleeding through?)", got)
	}
	if got := idx.Count("serving.knative.dev", "Service", "prod", "api"); got != 5 {
		t.Errorf("Knative Service count = %d, want 5 (collided with core Service bucket?)", got)
	}
	// Wrong group lookup is a miss, not a fallback.
	if got := idx.Count("example.io", "Service", "prod", "api"); got != 0 {
		t.Errorf("unknown-group lookup = %d, want 0 (key should not coalesce across groups)", got)
	}
}

// TestBuildIssueIndex_GroupAware exercises the full BuildIssueIndex
// path with two CRDs that share kind+namespace+name but live in
// different API groups. Pre-fix, both rows landed under the same
// "service|prod|api" key and one inherited the other's count.
func TestBuildIssueIndex_GroupAware(t *testing.T) {
	// Inject via a fake issues.Provider rather than the cache plumbing —
	// keeps the test focused on the index-key arithmetic.
	p := &fakeIssuesProvider{
		problems: []k8s.Problem{
			{Kind: "Service", Group: "", Namespace: "prod", Name: "api", Reason: "Endpoints", Severity: "warning"},
			{Kind: "Service", Group: "serving.knative.dev", Namespace: "prod", Name: "api", Reason: "RevisionFailed", Severity: "warning"},
			{Kind: "Service", Group: "serving.knative.dev", Namespace: "prod", Name: "api", Reason: "RouteNotReady", Severity: "warning"},
		},
	}
	idx := BuildIssueIndex(p, nil)
	if got := idx.Count("", "Service", "prod", "api"); got != 1 {
		t.Errorf("core Service count = %d, want 1", got)
	}
	if got := idx.Count("serving.knative.dev", "Service", "prod", "api"); got != 2 {
		t.Errorf("Knative Service count = %d, want 2", got)
	}
}

// TestBuildIssueIndex_BeyondMaxLimit pins that resources whose issues
// would fall in the tail beyond MaxLimit still get correct issueCounts.
// Pre-fix, BuildIssueIndex passed Limit:MaxLimit (1000) to Compose; on
// a cluster with >1000 issues the post-sort truncation silently zeroed
// out counts for tail resources. The fix is Limit:NoLimit — the index
// is a bucketed count, not a paginated list.
func TestBuildIssueIndex_BeyondMaxLimit(t *testing.T) {
	probs := make([]k8s.Problem, 0, issues.MaxLimit+50)
	for i := 0; i < issues.MaxLimit+50; i++ {
		probs = append(probs, k8s.Problem{
			Kind: "Pod", Namespace: "prod", Name: fmtPodName(i), Reason: "ImagePullBackOff", Severity: "warning",
		})
	}
	p := &fakeIssuesProvider{problems: probs}
	idx := BuildIssueIndex(p, nil)
	tailName := fmtPodName(issues.MaxLimit + 25)
	if got := idx.Count("", "Pod", "prod", tailName); got != 1 {
		t.Fatalf("tail pod %s count = %d, want 1 (silent MaxLimit truncation?)", tailName, got)
	}
	if got := idx.Count("", "Pod", "prod", fmtPodName(0)); got != 1 {
		t.Errorf("head pod count = %d, want 1", got)
	}
}

// TestCanonicalSingular pins the kind normalization used to align URL
// plurals with the singular form the issue engine emits.
func TestCanonicalSingular(t *testing.T) {
	cases := map[string]string{
		"pods":        "pod",
		"Pods":        "pod",
		"Deployment":  "deployment",
		"deployments": "deployment",
		"hpa":         "horizontalpodautoscaler",
		"unknownkind": "unknownkind",
	}
	for in, want := range cases {
		if got := CanonicalSingular(in); got != want {
			t.Errorf("CanonicalSingular(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestBuildIssueIndex_ClusterScopedIssueSurfacedWhenUnfiltered pins the
// end-to-end behavior: when the builder passes nil for the namespace
// filter (cluster-scoped kind), node-level issues at namespace=""
// surface in the index and the per-resource lookup returns the correct
// count. With a namespace filter populated, those same issues are
// dropped because Compose's per-namespace problem walk never sees them.
func TestBuildIssueIndex_ClusterScopedIssueSurfacedWhenUnfiltered(t *testing.T) {
	p := &fakeIssuesProvider{
		problems: []k8s.Problem{
			// Cluster-scoped Node issue: namespace="" — the actual shape
			// k8s.DetectProblems emits for NodeNotReady / DiskPressure etc.
			{Kind: "Node", Namespace: "", Name: "worker-1", Reason: "NotReady", Severity: "critical"},
		},
	}

	// Cluster-wide compose (nil namespaces) — issue surfaces.
	idx := BuildIssueIndex(p, nil)
	if got := idx.Count("", "Node", "", "worker-1"); got != 1 {
		t.Errorf("cluster-wide index: Node issueCount = %d, want 1 (cluster-scoped issue should appear)", got)
	}

	// Namespace-scoped compose — same issue, but ns filter to
	// ["prod","staging"] drops it because the user-namespaced perm
	// slice never matches "". This is what the pre-fix handler did for
	// Node lists.
	scopedIdx := BuildIssueIndex(p, []string{"prod", "staging"})
	if got := scopedIdx.Count("", "Node", "", "worker-1"); got != 0 {
		t.Errorf("namespace-scoped index: Node issueCount = %d, want 0 (namespace filter drops cluster-scoped issue)", got)
	}
}

// TestBuildIssueIndex_CRDPlural_NonZeroCount pins the fix for a Bugbot
// finding on PR #722: a CRD listed by its plural form (e.g.
// "applications" for ArgoCD Application) silently returned
// issueCount=0 because BuildIssueIndex used to push the URL kind
// through CanonicalSingular into filters.Kinds. CanonicalSingular only
// covers built-in plurals — CRD plurals fell through unchanged
// ("applications" stayed "applications"), Compose's case-insensitive
// Kind filter then failed against the singular "Application" the
// issue engine emits, and every CRD row's count was zero. We dropped
// the Kinds filter entirely: bucketing by issueIndexKey(group, kind,
// ns, name) is already correct because the lookup side runs through
// CanonicalSingular too. Per-resource lookup uses the row's singular
// Kind (Pascal "Application") so the index and the query agree.
func TestBuildIssueIndex_CRDPlural_NonZeroCount(t *testing.T) {
	p := &fakeIssuesProvider{
		problems: []k8s.Problem{
			{Kind: "Application", Group: "argoproj.io", Namespace: "argocd", Name: "storefront", Reason: "SyncFailed", Severity: "critical"},
		},
	}

	// Pre-fix simulation: the handler would have passed kindFilter="applications"
	// — the URL plural. We no longer take a kindFilter, but verify that
	// the index contains the row keyed by the canonical singular form.
	idx := BuildIssueIndex(p, []string{"argocd"})
	if got := idx.Count("argoproj.io", "Application", "argocd", "storefront"); got != 1 {
		t.Errorf("CRD Application count (singular kind) = %d, want 1", got)
	}
	// Also pin the URL-form lookup path: the per-row Builder is called
	// with the kind as returned by MinifyUnstructured, which for CRDs
	// is the singular ("Application"). If a caller ever pushed the
	// plural ("applications") through Count(), CanonicalSingular won't
	// normalize unknown CRD plurals — that's a separate latent issue
	// that doesn't manifest today because the row source uses the
	// singular. Document the asymmetry explicitly.
	if got := idx.Count("argoproj.io", "applications", "argocd", "storefront"); got != 0 {
		t.Errorf("CRD lookup via plural = %d, want 0 (CanonicalSingular only normalizes built-ins; row source uses singular Kind, so lookup matches via singular path)", got)
	}
}

// TestNewSearchSummaryContextBuilder_BuildsDualIndex pins the end-to-end
// shape used by /api/search and MCP search: scanNamespaces is non-nil
// (a namespace-restricted user, or a user with a `ns:` query modifier),
// so the constructor must compose TWO issue indexes — one scoped to
// those namespaces, one cluster-wide for cluster-scoped hits. Without
// the second index, the Node hit's summaryContext.issueCount returns
// 0 because every Node issue lives at namespace="" and the namespace
// filter drops them.
func TestNewSearchSummaryContextBuilder_BuildsDualIndex(t *testing.T) {
	p := &fakeIssuesProvider{
		problems: []k8s.Problem{
			{Kind: "Node", Group: "", Namespace: "", Name: "worker-1", Reason: "NotReady", Severity: "critical"},
			{Kind: "Pod", Group: "", Namespace: "prod", Name: "api-7", Reason: "ImagePullBackOff", Severity: "warning"},
		},
	}

	// Build the two indexes the search constructor would build.
	namespacedIdx := BuildIssueIndex(p, []string{"prod"})
	clusterIdx := BuildIssueIndex(p, nil)

	// Sanity: pre-fix, the search handler passed namespacedIdx for
	// both; Node issueCount silently zeroed.
	if got := namespacedIdx.Count("", "Node", "", "worker-1"); got != 0 {
		t.Errorf("namespacedIdx Node count = %d, want 0 (sanity — namespace filter drops cluster-scoped issues)", got)
	}
	if got := clusterIdx.Count("", "Node", "", "worker-1"); got != 1 {
		t.Errorf("clusterIdx Node count = %d, want 1 (cluster-wide compose surfaces namespace=\"\" issues)", got)
	}
	if got := namespacedIdx.Count("", "Pod", "prod", "api-7"); got != 1 {
		t.Errorf("namespacedIdx Pod count = %d, want 1", got)
	}

	// With both indexes built, the closure dispatches per-hit by
	// scope. Replay the dispatch via the shared helper to pin the
	// end-to-end shape. Topology is nil; managedBy is nil but
	// issueCount dispatch is what we're pinning here.
	build := BuilderFromIndexes(nil, namespacedIdx, clusterIdx)
	if sc := build(nil, nil, "", "Node", "", "worker-1"); sc == nil || sc.IssueCount != 1 {
		t.Errorf("Node hit via builder: got %+v, want IssueCount=1 (was 0 pre-fix)", sc)
	}
	if sc := build(nil, nil, "", "Pod", "prod", "api-7"); sc == nil || sc.IssueCount != 1 {
		t.Errorf("Pod hit via builder: got %+v, want IssueCount=1", sc)
	}
}

// TestBuilderFromIndexes_DispatchesByScope pins the dual-index dispatch:
// cluster-scoped hits (Node, PV, …) read the cluster-wide index (where
// namespace="" issues live), namespaced hits (Pod, Deployment, …) read
// the namespace-scoped index. Without this dispatch, a search response
// that mixes Pods and Nodes silently zeros issueCount on the Node hits
// — the namespace-scoped index drops every namespace="" issue.
//
// A wiring inversion (cluster-scoped → namespaced index) would
// re-introduce the bug, so we additionally assert no cross-bucket leak.
func TestBuilderFromIndexes_DispatchesByScope(t *testing.T) {
	// Build two distinct indexes so we can tell which one was consulted.
	namespacedIdx := IssueIndex{}
	namespacedIdx[issueIndexKey("", "Pod", "prod", "api-7")] = 4

	clusterIdx := IssueIndex{}
	clusterIdx[issueIndexKey("", "Node", "", "worker-1")] = 2

	// Topology is nil — managedBy is nil but issueCount dispatch is
	// what we're pinning here.
	build := BuilderFromIndexes(nil, namespacedIdx, clusterIdx)

	// Cluster-scoped Node hit — must read clusterIdx.
	if sc := build(nil, nil, "", "Node", "", "worker-1"); sc == nil || sc.IssueCount != 2 {
		t.Errorf("Node hit: got %+v, want IssueCount=2 from clusterIdx", sc)
	}
	// Namespaced Pod hit — must read namespacedIdx.
	if sc := build(nil, nil, "", "Pod", "prod", "api-7"); sc == nil || sc.IssueCount != 4 {
		t.Errorf("Pod hit: got %+v, want IssueCount=4 from namespacedIdx", sc)
	}
	// A cluster-scoped hit whose name only lives in the namespaced
	// index must return 0 (no cross-bucket leak).
	if sc := build(nil, nil, "", "Node", "", "api-7"); sc != nil && sc.IssueCount != 0 {
		t.Errorf("Node hit using Pod-bucket name leaked count: %+v", sc)
	}
	// And a namespaced hit whose name only lives in the cluster index
	// likewise returns 0.
	if sc := build(nil, nil, "", "Pod", "prod", "worker-1"); sc != nil && sc.IssueCount != 0 {
		t.Errorf("Pod hit using Node-bucket name leaked count: %+v", sc)
	}
}

// TestManagedByFromRelationships_PrefersManagedBy pins the topmost-manager
// shortcut: when topology has synthesized a ManagedBy chain (Pod →
// ReplicaSet → Deployment), the helper surfaces the Deployment, not the
// noisy hash-suffixed ReplicaSet that sits in Owner.
func TestManagedByFromRelationships_PrefersManagedBy(t *testing.T) {
	rel := &topology.Relationships{
		Owner: &topology.ResourceRef{Kind: "ReplicaSet", Namespace: "prod", Name: "api-7d5", Group: "apps"},
		ManagedBy: []topology.ResourceRef{
			{Kind: "Deployment", Namespace: "prod", Name: "api", Group: "apps"},
		},
	}
	got := ManagedByFromRelationships(rel)
	want := &resourcecontext.ManagedByRef{Kind: "Deployment", Source: "native", Name: "api", Namespace: "prod"}
	if got == nil || got.Kind != want.Kind || got.Name != want.Name || got.Namespace != want.Namespace || got.Source != want.Source {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

// TestManagedByFromRelationships_FallsBackToOwner covers the case where
// topology synthesis declined ManagedBy (e.g. cluster-scoped roots) —
// we still surface the direct Owner so the row isn't context-less.
func TestManagedByFromRelationships_FallsBackToOwner(t *testing.T) {
	rel := &topology.Relationships{
		Owner: &topology.ResourceRef{Kind: "Application", Namespace: "argocd", Name: "storefront", Group: "argoproj.io"},
	}
	got := ManagedByFromRelationships(rel)
	if got == nil {
		t.Fatalf("got nil, want Application ref")
	}
	if got.Source != "argocd" {
		t.Errorf("Source = %q, want argocd", got.Source)
	}
}

// TestManagedByFromRelationships_ManagedByWinsOverOwner pins that when
// both ManagedBy and Owner are set, ManagedBy[0] takes precedence — the
// server-synthesized topmost-manager walk should never be shadowed by
// the direct owner ref left over for back-compat.
func TestManagedByFromRelationships_ManagedByWinsOverOwner(t *testing.T) {
	rel := &topology.Relationships{
		Owner: &topology.ResourceRef{Kind: "ReplicaSet", Namespace: "prod", Name: "api-7d5", Group: "apps"},
		ManagedBy: []topology.ResourceRef{
			{Kind: "Application", Namespace: "argocd", Name: "storefront", Group: "argoproj.io"},
		},
	}
	got := ManagedByFromRelationships(rel)
	if got == nil || got.Kind != "Application" || got.Source != "argocd" {
		t.Errorf("got %#v, want Application/argocd", got)
	}
}

func TestManagedByFromRelationships_NilSafe(t *testing.T) {
	if got := ManagedByFromRelationships(nil); got != nil {
		t.Errorf("nil rel: got %#v, want nil", got)
	}
	if got := ManagedByFromRelationships(&topology.Relationships{}); got != nil {
		t.Errorf("empty rel: got %#v, want nil", got)
	}
}
