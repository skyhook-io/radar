// Mirror of internal/server/summary_context_test.go for the MCP path —
// pins the group-aware issue index key and the NoLimit fix so the MCP
// list_resources / search builders stay in lockstep with REST.

package mcp

import (
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	bp "github.com/skyhook-io/radar/pkg/audit"
)

// fakeIssuesProvider is a minimal issues.Provider for the buildIssueIndex
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
func (f *fakeIssuesProvider) AuditFindings(_ []string) []bp.Finding       { return nil }
func (f *fakeIssuesProvider) WarningEvents(_ []string, _ time.Duration) []*corev1.Event {
	return nil
}
func (f *fakeIssuesProvider) WatchedDynamic() []schema.GroupVersionResource { return nil }
func (f *fakeIssuesProvider) ListDynamic(_ schema.GroupVersionResource, _ string) ([]*unstructured.Unstructured, error) {
	return nil, nil
}
func (f *fakeIssuesProvider) KindForGVR(_ schema.GroupVersionResource) string { return "" }

func fmtPodName(i int) string { return fmt.Sprintf("pod-%05d", i) }

// TestIssueIndexKey_GroupAware pins that two resources sharing
// kind+namespace+name but in different API groups get independent
// counts. The MCP layer mirrors the REST layer's index — same hazard,
// same fix.
func TestIssueIndexKey_GroupAware(t *testing.T) {
	idx := issueIndex{}
	idx[issueIndexKey("", "Service", "prod", "api")] = 2
	idx[issueIndexKey("serving.knative.dev", "Service", "prod", "api")] = 5

	if got := idx.count("", "Service", "prod", "api"); got != 2 {
		t.Errorf("core Service count = %d, want 2 (Knative bucket bleeding through?)", got)
	}
	if got := idx.count("serving.knative.dev", "Service", "prod", "api"); got != 5 {
		t.Errorf("Knative Service count = %d, want 5 (collided with core Service bucket?)", got)
	}
	if got := idx.count("example.io", "Service", "prod", "api"); got != 0 {
		t.Errorf("unknown-group lookup = %d, want 0", got)
	}
}

// TestBuildIssueIndex_GroupAware exercises the full buildIssueIndex
// path with two CRDs that share kind+namespace+name across groups.
func TestBuildIssueIndex_GroupAware(t *testing.T) {
	p := &fakeIssuesProvider{
		problems: []k8s.Problem{
			{Kind: "Service", Group: "", Namespace: "prod", Name: "api", Reason: "Endpoints", Severity: "warning"},
			{Kind: "Service", Group: "serving.knative.dev", Namespace: "prod", Name: "api", Reason: "RevisionFailed", Severity: "warning"},
			{Kind: "Service", Group: "serving.knative.dev", Namespace: "prod", Name: "api", Reason: "RouteNotReady", Severity: "warning"},
		},
	}
	idx := buildIssueIndex(p, nil, "")
	if got := idx.count("", "Service", "prod", "api"); got != 1 {
		t.Errorf("core Service count = %d, want 1", got)
	}
	if got := idx.count("serving.knative.dev", "Service", "prod", "api"); got != 2 {
		t.Errorf("Knative Service count = %d, want 2", got)
	}
}

// TestBuildIssueIndex_BeyondMaxLimit pins that resources whose issues
// would fall in the tail beyond MaxLimit still get correct issueCounts.
// Pre-fix, buildIssueIndex passed Limit:MaxLimit (1000) to Compose; on
// a cluster with >1000 issues the post-sort truncation silently zeroed
// tail counts. NoLimit removes the cap because the index is a per-
// resource bucket count, not a paginated list.
func TestBuildIssueIndex_BeyondMaxLimit(t *testing.T) {
	probs := make([]k8s.Problem, 0, issues.MaxLimit+50)
	for i := 0; i < issues.MaxLimit+50; i++ {
		probs = append(probs, k8s.Problem{
			Kind: "Pod", Namespace: "prod", Name: fmtPodName(i), Reason: "ImagePullBackOff", Severity: "warning",
		})
	}
	p := &fakeIssuesProvider{problems: probs}
	idx := buildIssueIndex(p, nil, "")
	tailName := fmtPodName(issues.MaxLimit + 25)
	if got := idx.count("", "Pod", "prod", tailName); got != 1 {
		t.Fatalf("tail pod %s count = %d, want 1 (silent MaxLimit truncation?)", tailName, got)
	}
	if got := idx.count("", "Pod", "prod", fmtPodName(0)); got != 1 {
		t.Errorf("head pod count = %d, want 1", got)
	}
}

// TestSummaryContextBuilderFromIndexes_DispatchesByScope pins the
// dual-index dispatch on the MCP path. Search returns mixed-kind hits
// (namespaced Pods + cluster-scoped Nodes); a single namespace-scoped
// index would zero issueCount on the Node hits because their problems
// live at namespace="". Mirror of the REST-side test in
// internal/server.
func TestSummaryContextBuilderFromIndexes_DispatchesByScope(t *testing.T) {
	namespacedIdx := issueIndex{}
	namespacedIdx[issueIndexKey("", "Pod", "prod", "api-7")] = 4

	clusterIdx := issueIndex{}
	clusterIdx[issueIndexKey("", "Node", "", "worker-1")] = 2

	build := summaryContextBuilderFromIndexes(nil, namespacedIdx, clusterIdx)

	if sc := build(nil, nil, "", "Node", "", "worker-1"); sc == nil || sc.IssueCount != 2 {
		t.Errorf("Node hit: got %+v, want IssueCount=2 from clusterIdx", sc)
	}
	if sc := build(nil, nil, "", "Pod", "prod", "api-7"); sc == nil || sc.IssueCount != 4 {
		t.Errorf("Pod hit: got %+v, want IssueCount=4 from namespacedIdx", sc)
	}
	// Cross-bucket name lookups must not leak.
	if sc := build(nil, nil, "", "Node", "", "api-7"); sc != nil && sc.IssueCount != 0 {
		t.Errorf("Node hit using Pod-bucket name leaked count: %+v", sc)
	}
	if sc := build(nil, nil, "", "Pod", "prod", "worker-1"); sc != nil && sc.IssueCount != 0 {
		t.Errorf("Pod hit using Node-bucket name leaked count: %+v", sc)
	}
}

// TestNewSearchSummaryContextBuilder_BuildsDualIndex pins the
// constructor: scanNamespaces non-nil → two distinct indexes, one
// scoped, one cluster-wide. Without this, MCP search responses zero
// out issueCount on Node / PV / cluster-scoped CRD hits. Mirror of the
// REST-side test.
func TestNewSearchSummaryContextBuilder_BuildsDualIndex(t *testing.T) {
	p := &fakeIssuesProvider{
		problems: []k8s.Problem{
			{Kind: "Node", Group: "", Namespace: "", Name: "worker-1", Reason: "NotReady", Severity: "critical"},
			{Kind: "Pod", Group: "", Namespace: "prod", Name: "api-7", Reason: "ImagePullBackOff", Severity: "warning"},
		},
	}

	namespacedIdx := buildIssueIndex(p, []string{"prod"}, "")
	clusterIdx := buildIssueIndex(p, nil, "")

	if got := namespacedIdx.count("", "Node", "", "worker-1"); got != 0 {
		t.Errorf("namespacedIdx Node count = %d, want 0 (sanity)", got)
	}
	if got := clusterIdx.count("", "Node", "", "worker-1"); got != 1 {
		t.Errorf("clusterIdx Node count = %d, want 1", got)
	}
	if got := namespacedIdx.count("", "Pod", "prod", "api-7"); got != 1 {
		t.Errorf("namespacedIdx Pod count = %d, want 1", got)
	}

	build := summaryContextBuilderFromIndexes(nil, namespacedIdx, clusterIdx)
	if sc := build(nil, nil, "", "Node", "", "worker-1"); sc == nil || sc.IssueCount != 1 {
		t.Errorf("Node hit via builder: got %+v, want IssueCount=1 (was 0 pre-fix)", sc)
	}
	if sc := build(nil, nil, "", "Pod", "prod", "api-7"); sc == nil || sc.IssueCount != 1 {
		t.Errorf("Pod hit via builder: got %+v, want IssueCount=1", sc)
	}
}

// TestBuildIssueIndex_ClusterScopedIssueRequiresUnfilteredCompose pins
// the MCP-side regression for the cluster-scoped issueCount bug. When
// handleListResources hands a namespace-restricted slice to the issue
// index, cluster-scoped issues (Namespace=="") are dropped by Compose's
// per-namespace problem walk — every Node row gets issueCount=0 even
// when the user has cluster-scoped Node access. The fix routes
// clusterScoped through and forces idxNamespaces=nil before calling
// newSummaryContextBuilder; this test pins the buildIssueIndex behavior
// that backs that path.
func TestBuildIssueIndex_ClusterScopedIssueRequiresUnfilteredCompose(t *testing.T) {
	p := &fakeIssuesProvider{
		problems: []k8s.Problem{
			{Kind: "Node", Namespace: "", Name: "worker-1", Reason: "NotReady", Severity: "critical"},
		},
	}
	// Cluster-wide compose surfaces the Node issue.
	idx := buildIssueIndex(p, nil, "Node")
	if got := idx.count("", "Node", "", "worker-1"); got != 1 {
		t.Errorf("cluster-wide index: Node issueCount = %d, want 1", got)
	}
	// Namespace-scoped compose drops the same issue — what the pre-fix
	// MCP handler did on every Node list for a namespace-restricted user.
	scopedIdx := buildIssueIndex(p, []string{"prod", "staging"}, "Node")
	if got := scopedIdx.count("", "Node", "", "worker-1"); got != 0 {
		t.Errorf("namespace-scoped index: Node issueCount = %d, want 0 (namespace filter drops cluster-scoped issue)", got)
	}
}
