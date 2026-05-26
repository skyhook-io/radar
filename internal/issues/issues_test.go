package issues

import (
	"sort"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/k8s"
)

// fakeProvider — minimal Provider for unit testing. Each field
// pre-stages what the corresponding method returns. Test cases assemble
// one of these and pass it to Compose.
type fakeProvider struct {
	problems     []k8s.Problem
	missingRefs  []k8s.Problem
	scheduling   []k8s.Problem
	capiProblems []k8s.Problem
	dynamic      map[schema.GroupVersionResource][]*unstructured.Unstructured
	kinds        map[schema.GroupVersionResource]string
	namespaced   map[schema.GroupVersionResource]bool
}

func (f *fakeProvider) DetectProblems(_ []string) []k8s.Problem     { return f.problems }
func (f *fakeProvider) DetectMissingRefs(_ []string) []k8s.Problem  { return f.missingRefs }
func (f *fakeProvider) DetectScheduling(_ []string) []k8s.Problem   { return f.scheduling }
func (f *fakeProvider) DetectCAPIProblems(_ []string) []k8s.Problem { return f.capiProblems }
func (f *fakeProvider) WatchedDynamic() []schema.GroupVersionResource {
	out := make([]schema.GroupVersionResource, 0, len(f.dynamic))
	for g := range f.dynamic {
		out = append(out, g)
	}
	return out
}
func (f *fakeProvider) ListDynamic(gvr schema.GroupVersionResource, _ string) ([]*unstructured.Unstructured, error) {
	return f.dynamic[gvr], nil
}
func (f *fakeProvider) KindForGVR(gvr schema.GroupVersionResource) string {
	return f.kinds[gvr]
}
func (f *fakeProvider) NamespacedForGVR(gvr schema.GroupVersionResource) (bool, bool) {
	namespaced, ok := f.namespaced[gvr]
	return namespaced, ok
}

func TestCompose_NormalizesProblemSeverity(t *testing.T) {
	p := &fakeProvider{
		problems: []k8s.Problem{
			{Kind: "Deployment", Namespace: "ns", Name: "a", Severity: "critical", Reason: "down"},
			{Kind: "Deployment", Namespace: "ns", Name: "b", Severity: "high", Reason: "slow"},
			{Kind: "Deployment", Namespace: "ns", Name: "c", Severity: "medium", Reason: "warn"},
		},
	}
	out := Compose(p, Filters{})
	if len(out) != 3 {
		t.Fatalf("got %d issues", len(out))
	}
	bySev := map[Severity]int{}
	for _, i := range out {
		bySev[i.Severity]++
	}
	if bySev[SeverityCritical] != 1 || bySev[SeverityWarning] != 2 {
		t.Fatalf("severity normalization wrong: %+v", bySev)
	}
}

func TestCompose_PodSchedulingWinsOverProblem(t *testing.T) {
	// A pod stuck post-bind trips both sources: DetectProblems flags it
	// Pending>5m and DetectScheduling names the actual CNI/volume blocker.
	// The scheduling row is richer, so the generic problem row for the SAME
	// pod must be dropped — without collapsing unrelated rows.
	p := &fakeProvider{
		problems: []k8s.Problem{
			{Kind: "Pod", Namespace: "ns", Name: "stuck", Severity: "high", Reason: "Pending"},
			{Kind: "Pod", Namespace: "ns", Name: "other", Severity: "high", Reason: "CrashLoopBackOff"},
			{Kind: "Deployment", Namespace: "ns", Name: "stuck", Severity: "critical", Reason: "down"},
		},
		scheduling: []k8s.Problem{
			{Kind: "Pod", Namespace: "ns", Name: "stuck", Severity: "high", Reason: "VolumeMount"},
		},
	}
	out := Compose(p, Filters{})

	var stuckPodRows []Issue
	for _, i := range out {
		if i.Kind == "Pod" && i.Name == "stuck" {
			stuckPodRows = append(stuckPodRows, i)
		}
	}
	if len(stuckPodRows) != 1 {
		t.Fatalf("expected exactly 1 row for Pod ns/stuck (scheduling wins), got %d: %+v", len(stuckPodRows), out)
	}
	if stuckPodRows[0].Source != SourceScheduling || stuckPodRows[0].Reason != "VolumeMount" {
		t.Errorf("the surviving Pod row should be the scheduling one, got %+v", stuckPodRows[0])
	}
	// The unrelated problem-source pod and the same-name Deployment must
	// survive — dedup keys on (source=problem, kind=Pod, ns/name) only.
	var sawOtherPod, sawDeploy bool
	for _, i := range out {
		if i.Kind == "Pod" && i.Name == "other" {
			sawOtherPod = true
		}
		if i.Kind == "Deployment" && i.Name == "stuck" {
			sawDeploy = true
		}
	}
	if !sawOtherPod {
		t.Errorf("unrelated problem-source Pod must not be dropped: %+v", out)
	}
	if !sawDeploy {
		t.Errorf("same-name Deployment must not be dropped by Pod dedup: %+v", out)
	}
}

func TestCompose_SchedulingComposedByDefault(t *testing.T) {
	countSource := func(in []Issue, s Source) int {
		n := 0
		for _, i := range in {
			if i.Source == s {
				n++
			}
		}
		return n
	}
	p := &fakeProvider{
		problems: []k8s.Problem{
			{Kind: "Deployment", Namespace: "prod", Name: "api", Severity: "critical", Reason: "Unavailable"},
		},
		scheduling: []k8s.Problem{
			{Kind: "Pod", Namespace: "prod", Name: "web-x", Severity: "high", Reason: "Unschedulable", Message: "no node has kubernetes.io/arch=arm64"},
		},
	}

	// Both curated sources compose unconditionally; each row carries its
	// source label for CEL/UI grouping.
	out := Compose(p, Filters{})
	if countSource(out, SourceScheduling) != 1 || countSource(out, SourceProblem) != 1 {
		t.Fatalf("Compose should include problem + scheduling, got %+v", out)
	}
}

func TestCompose_MissingRefsComposedByDefault(t *testing.T) {
	p := &fakeProvider{
		problems: []k8s.Problem{
			{Kind: "Service", Namespace: "prod", Name: "api", Severity: "warning", Reason: "Selector matches no pods"},
		},
		missingRefs: []k8s.Problem{
			{Kind: "Pod", Namespace: "prod", Name: "web", Severity: "critical", Reason: "Missing PVC"},
		},
	}

	out := Compose(p, Filters{})
	if !hasIssueSource(out, SourceProblem) || !hasIssueSource(out, SourceMissingRef) {
		t.Fatalf("Compose should include problem + missing_ref, got %+v", out)
	}
}

func TestCompose_GenericCRDConditionFallback(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata":   map[string]any{"name": "my-app", "namespace": "argocd"},
		"status": map[string]any{
			"conditions": []any{
				map[string]any{
					"type":               "Synced",
					"status":             "False",
					"reason":             "OutOfSync",
					"message":            "drift detected",
					"lastTransitionTime": time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339),
				},
			},
		},
	}}
	p := &fakeProvider{
		dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {app}},
		kinds:   map[schema.GroupVersionResource]string{gvr: "Application"},
	}
	out := Compose(p, Filters{})
	if len(out) != 1 {
		t.Fatalf("got %d issues, want 1", len(out))
	}
	hit := out[0]
	if hit.Source != SourceCondition {
		t.Fatalf("source: %s", hit.Source)
	}
	if hit.Group != "argoproj.io" {
		t.Fatalf("group not propagated: %+v", hit)
	}
	if hit.Severity != SeverityWarning {
		t.Fatalf("severity: %s", hit.Severity)
	}
	if hit.Reason == "" || hit.Message != "drift detected" {
		t.Fatalf("reason/message: %+v", hit)
	}
}

func TestCompose_CAPIGroupSkippedByGenericFallback(t *testing.T) {
	// Curated CAPI checker owns this group — generic fallback should
	// skip it to avoid double-reporting.
	gvr := schema.GroupVersionResource{Group: "cluster.x-k8s.io", Version: "v1beta1", Resource: "clusters"}
	cl := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "c1"},
		"status": map[string]any{
			"conditions": []any{
				map[string]any{"type": "Ready", "status": "False", "reason": "X"},
			},
		},
	}}
	p := &fakeProvider{
		dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {cl}},
		kinds:   map[schema.GroupVersionResource]string{gvr: "Cluster"},
	}
	out := Compose(p, Filters{})
	if len(out) != 0 {
		t.Fatalf("CAPI should be skipped by generic fallback: %+v", out)
	}
}

func TestCompose_DropsUnauthorizedClusterScopedIssues(t *testing.T) {
	p := &fakeProvider{
		problems: []k8s.Problem{
			{Kind: "Deployment", Namespace: "team-a", Name: "api", Severity: "critical", Reason: "down"},
			{Kind: "Node", Name: "worker-1", Severity: "critical", Reason: "not ready"},
		},
	}
	out := Compose(p, Filters{
		CanReadClusterScoped: func(kind, group string) bool {
			if kind != "Node" || group != "" {
				t.Fatalf("unexpected cluster-scoped check: kind=%q group=%q", kind, group)
			}
			return false
		},
	})
	if len(out) != 1 {
		t.Fatalf("expected only namespaced issue, got %+v", out)
	}
	if out[0].Kind != "Deployment" || out[0].Namespace != "team-a" {
		t.Fatalf("wrong issue retained: %+v", out)
	}
}

func TestCompose_DropsUnauthorizedClusterScopedCRDConditions(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "karpenter.sh", Version: "v1", Resource: "nodepools"}
	np := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "karpenter.sh/v1",
		"kind":       "NodePool",
		"metadata":   map[string]any{"name": "default"},
		"status": map[string]any{
			"conditions": []any{
				map[string]any{"type": "Ready", "status": "False", "reason": "Drifted"},
			},
		},
	}}
	p := &fakeProvider{
		dynamic:    map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {np}},
		kinds:      map[schema.GroupVersionResource]string{gvr: "NodePool"},
		namespaced: map[schema.GroupVersionResource]bool{gvr: false},
	}
	out := Compose(p, Filters{
		CanReadClusterScoped: func(kind, group string) bool {
			if kind != "NodePool" || group != "karpenter.sh" {
				t.Fatalf("unexpected cluster-scoped check: kind=%q group=%q", kind, group)
			}
			return false
		},
	})
	if len(out) != 0 {
		t.Fatalf("cluster-scoped CRD condition leaked despite denied access: %+v", out)
	}
}

func TestCompose_SeveritySortedDescending(t *testing.T) {
	p := &fakeProvider{
		problems: []k8s.Problem{
			{Kind: "Pod", Name: "warn1", Severity: "high"},
			{Kind: "Pod", Name: "crit1", Severity: "critical"},
			{Kind: "Pod", Name: "warn2", Severity: "medium"},
		},
	}
	out := Compose(p, Filters{})
	if out[0].Name != "crit1" {
		t.Fatalf("critical should sort first, got %+v", out[0])
	}
}

func TestCompose_SeverityFilter(t *testing.T) {
	p := &fakeProvider{
		problems: []k8s.Problem{
			{Kind: "Pod", Name: "a", Severity: "critical"},
			{Kind: "Pod", Name: "b", Severity: "medium"},
		},
	}
	out := Compose(p, Filters{Severities: []Severity{SeverityCritical}})
	if len(out) != 1 || out[0].Name != "a" {
		t.Fatalf("severity filter wrong: %+v", out)
	}
}

func TestCompose_KindFilter(t *testing.T) {
	p := &fakeProvider{
		problems: []k8s.Problem{
			{Kind: "Pod", Name: "p", Severity: "critical"},
			{Kind: "Deployment", Name: "d", Severity: "critical"},
		},
	}
	out := Compose(p, Filters{Kinds: []string{"Pod"}})
	if len(out) != 1 || out[0].Kind != "Pod" {
		t.Fatalf("kind filter wrong: %+v", out)
	}
}

func TestCompose_LimitTruncates(t *testing.T) {
	probs := make([]k8s.Problem, 0, 50)
	for i := 0; i < 50; i++ {
		probs = append(probs, k8s.Problem{Kind: "Pod", Name: "p", Severity: "critical"})
	}
	p := &fakeProvider{problems: probs}
	out := Compose(p, Filters{Limit: 10})
	if len(out) != 10 {
		t.Fatalf("limit not honored: %d", len(out))
	}
}

func TestCompose_DeterministicOrderForTies(t *testing.T) {
	// Same severity + same last-seen → tiebreak on (kind, ns, name).
	// All hits are critical, all DurationSeconds=0, so LastSeen ties.
	p := &fakeProvider{
		problems: []k8s.Problem{
			{Kind: "Service", Namespace: "ns", Name: "z", Severity: "critical"},
			{Kind: "Pod", Namespace: "ns", Name: "a", Severity: "critical"},
			{Kind: "Pod", Namespace: "ns", Name: "b", Severity: "critical"},
		},
	}
	out := Compose(p, Filters{})
	got := []string{out[0].Kind + "/" + out[0].Name, out[1].Kind + "/" + out[1].Name, out[2].Kind + "/" + out[2].Name}
	want := []string{"Pod/a", "Pod/b", "Service/z"} // Pod < Service alphabetically
	if got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("tiebreak order: got %v want %v", got, want)
	}
}

// silence unused-import lint when sort isn't used elsewhere
var _ = sort.Strings

func hasIssueSource(issues []Issue, source Source) bool {
	for _, issue := range issues {
		if issue.Source == source {
			return true
		}
	}
	return false
}

// flattenNamespacedProblems exists to keep CacheProvider's per-
// namespace fan-out from leaking + duplicating cluster-scoped
// problems (Node, etc.). These tests pin that contract.

func TestFlattenNamespacedProblems_DropsClusterScopedEntries(t *testing.T) {
	// Each per-namespace list as returned by k8s.DetectProblems
	// includes the cluster-scoped Node block — without filtering, a
	// namespace-bounded caller asking for {ns1, ns2} would see Node
	// problems twice AND see them at all (RBAC violation if the user
	// lacks `list nodes` at cluster scope).
	perNs := [][]k8s.Problem{
		{
			{Kind: "Pod", Namespace: "ns1", Name: "p1", Severity: "critical"},
			{Kind: "Node", Name: "node-1", Severity: "high"}, // empty Namespace
		},
		{
			{Kind: "Pod", Namespace: "ns2", Name: "p2", Severity: "critical"},
			{Kind: "Node", Name: "node-1", Severity: "high"}, // dup leak
		},
	}
	out := flattenNamespacedProblems(perNs)
	if len(out) != 2 {
		t.Fatalf("want 2 namespaced problems, got %d: %+v", len(out), out)
	}
	for _, p := range out {
		if p.Kind == "Node" {
			t.Errorf("Node problem leaked through namespace-scoped flatten: %+v", p)
		}
		if p.Namespace == "" {
			t.Errorf("cluster-scoped problem leaked: %+v", p)
		}
	}
}

func TestFlattenNamespacedProblems_PreservesNamespacedAcrossSlices(t *testing.T) {
	// Namespaced rows from different per-namespace calls all survive
	// — no over-zealous dedup.
	perNs := [][]k8s.Problem{
		{{Kind: "Pod", Namespace: "ns1", Name: "a"}},
		{{Kind: "Pod", Namespace: "ns2", Name: "a"}}, // same name, different ns
		{{Kind: "Service", Namespace: "ns3", Name: "svc"}},
	}
	out := flattenNamespacedProblems(perNs)
	if len(out) != 3 {
		t.Fatalf("want 3 problems preserved, got %d: %+v", len(out), out)
	}
}

func TestFlattenNamespacedProblems_EmptyInputReturnsNil(t *testing.T) {
	if out := flattenNamespacedProblems(nil); len(out) != 0 {
		t.Errorf("nil input should produce empty output, got %+v", out)
	}
	if out := flattenNamespacedProblems([][]k8s.Problem{}); len(out) != 0 {
		t.Errorf("empty input should produce empty output, got %+v", out)
	}
}

// countingProvider wraps fakeProvider and tallies ListDynamic calls per
// GVR. Used by TestDetectGenericCRDIssues_SkipsListWhenKindFiltered to
// pin that detectGenericCRDIssues short-circuits the per-GVR
// ListDynamic call when f.Kinds excludes the GVR's kind — on clusters
// with hundreds of watched CRDs, scanning every one for a pods-only
// summaryContext request was the dominant cost.
type countingProvider struct {
	fakeProvider
	listCalls map[schema.GroupVersionResource]int
}

func (c *countingProvider) ListDynamic(gvr schema.GroupVersionResource, ns string) ([]*unstructured.Unstructured, error) {
	if c.listCalls == nil {
		c.listCalls = map[schema.GroupVersionResource]int{}
	}
	c.listCalls[gvr]++
	return c.fakeProvider.ListDynamic(gvr, ns)
}

// TestDetectGenericCRDIssues_SkipsListWhenKindFiltered pins the
// "scan all CRDs before kindFilter applies" perf fix in
// detectGenericCRDIssues. Pre-fix, a Compose call with Kinds=["Pod"]
// still iterated every watched CRD GVR and ran ListDynamic on each;
// applyFilters then discarded the non-matching rows at the end.
//
// On a cluster with hundreds of watched CRDs this dominated the
// summaryContext per-row index build for list_resources kind=pods.
// The fix routes f.Kinds awareness into detectGenericCRDIssues so
// non-matching GVRs skip the ListDynamic call entirely.
func TestDetectGenericCRDIssues_SkipsListWhenKindFiltered(t *testing.T) {
	podGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	appGVR := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	npGVR := schema.GroupVersionResource{Group: "karpenter.sh", Version: "v1", Resource: "nodepools"}

	p := &countingProvider{
		fakeProvider: fakeProvider{
			dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{
				podGVR: {}, // empty — only counts the call.
				appGVR: {{Object: map[string]any{
					"metadata": map[string]any{"name": "a", "namespace": "argocd"},
					"status": map[string]any{
						"conditions": []any{
							map[string]any{"type": "Synced", "status": "False", "reason": "Drift"},
						},
					},
				}}},
				npGVR: {}, // empty — only counts the call.
			},
			kinds: map[schema.GroupVersionResource]string{
				podGVR: "Pod",
				appGVR: "Application",
				npGVR:  "NodePool",
			},
		},
	}

	// kindFilter restricts to Application — the other two GVRs must NOT
	// be listed. detectGenericCRDIssues lowercases the kind comparison
	// (mirrors applyFilters), so the canonical "Application" matches the
	// emitted Kind for the argoproj.io GVR.
	_ = detectGenericCRDIssues(p, Filters{Kinds: []string{"Application"}})

	if got := p.listCalls[podGVR]; got != 0 {
		t.Errorf("Pod GVR ListDynamic calls = %d, want 0 (kind filter must skip non-matching GVRs)", got)
	}
	if got := p.listCalls[npGVR]; got != 0 {
		t.Errorf("NodePool GVR ListDynamic calls = %d, want 0 (kind filter must skip non-matching GVRs)", got)
	}
	if got := p.listCalls[appGVR]; got == 0 {
		t.Errorf("Application GVR ListDynamic calls = %d, want >= 1 (matching kind must still be scanned)", got)
	}

	// Sanity: empty Kinds filter scans every GVR (no per-kind shortcut
	// when caller didn't ask for one). Pins that the fix is filter-aware
	// rather than always-skip.
	p.listCalls = nil
	_ = detectGenericCRDIssues(p, Filters{})
	for gvr, want := range map[schema.GroupVersionResource]bool{podGVR: true, appGVR: true, npGVR: true} {
		if got := p.listCalls[gvr] > 0; got != want {
			t.Errorf("no kind filter: GVR %s called=%v, want %v", gvr.Resource, got, want)
		}
	}
}
