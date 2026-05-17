package server

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
	bp "github.com/skyhook-io/radar/pkg/audit"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	"github.com/skyhook-io/radar/pkg/topology"
)

// k8sProblem is the test-side alias kept short so generated rows
// don't have to repeat the package qualifier.
type k8sProblem = k8s.Problem

// issuesMaxLimit mirrors internal/issues.MaxLimit at test scope so the
// MaxLimit-overflow assertion doesn't depend on test order against the
// importing package's constant.
var issuesMaxLimit = issues.MaxLimit

// fakeIssuesProvider is a minimal issues.Provider for the buildIssueIndex
// tests. Only the fields the index path touches are wired; the CRD-
// condition fallback path is exercised by issues' own tests.
type fakeIssuesProvider struct {
	problems []k8s.Problem
}

func (f *fakeIssuesProvider) DetectProblems(_ []string) []k8s.Problem     { return f.problems }
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

// stubBuilder records calls and returns a deterministic SummaryContext
// keyed by the resource identity. Avoids standing up a topology cache or
// issue provider — those are exercised by the per-layer unit tests.
//
// Key shape mirrors the production issueIndexKey (group|kind|ns|name)
// so test fixtures pin the group-aware lookup.
func stubBuilder(t *testing.T, want map[string]*resourcecontext.SummaryContext) summaryContextBuilder {
	t.Helper()
	return func(obj runtime.Object, u *unstructured.Unstructured, group, kind, namespace, name string) *resourcecontext.SummaryContext {
		key := group + "|" + kind + "|" + namespace + "|" + name
		return want[key]
	}
}

// TestAttachSummaryContextToList wires together MinifyList + the
// per-row attach helper and asserts the SummaryContext field lands in
// the JSON each row marshals to.
func TestAttachSummaryContextToList(t *testing.T) {
	objs := []runtime.Object{
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "api-1", Namespace: "prod"},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Ready: true}}},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "api-2", Namespace: "prod"},
			Status:     corev1.PodStatus{Phase: corev1.PodFailed},
		},
	}
	// Group is "" for core-group Pods.
	want := map[string]*resourcecontext.SummaryContext{
		"|Pod|prod|api-1": {
			ManagedBy:  &resourcecontext.ManagedByRef{Kind: "Deployment", Source: "native", Name: "api", Namespace: "prod"},
			Health:     "healthy",
			IssueCount: 0,
		},
		"|Pod|prod|api-2": {
			ManagedBy:  &resourcecontext.ManagedByRef{Kind: "Deployment", Source: "native", Name: "api", Namespace: "prod"},
			Health:     "unhealthy",
			IssueCount: 3,
		},
	}

	results, err := aicontext.MinifyList(objs, aicontext.LevelSummary)
	if err != nil {
		t.Fatalf("MinifyList: %v", err)
	}
	attachSummaryContextToList(results, objs, stubBuilder(t, want))

	// Row 0 — healthy pod.
	b, _ := json.Marshal(results[0])
	wantSubs := []string{
		`"summaryContext":`,
		`"managedBy":{"kind":"Deployment"`,
		`"health":"healthy"`,
	}
	for _, sub := range wantSubs {
		if !contains(string(b), sub) {
			t.Errorf("row 0 missing %s in %s", sub, b)
		}
	}

	// Row 1 — unhealthy pod with issueCount.
	b, _ = json.Marshal(results[1])
	wantSubs = []string{
		`"health":"unhealthy"`,
		`"issueCount":3`,
	}
	for _, sub := range wantSubs {
		if !contains(string(b), sub) {
			t.Errorf("row 1 missing %s in %s", sub, b)
		}
	}
}

// TestAttachSummaryContextToList_MismatchedLengthsSilent — defensive
// path that protects against a future refactor where MinifyList might
// drop unsupported kinds. Attach must skip rather than panic.
func TestAttachSummaryContextToList_MismatchedLengthsSilent(t *testing.T) {
	objs := []runtime.Object{
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "api-1"}},
	}
	results := []any{
		&aicontext.ResourceSummary{Kind: "Pod", Name: "api-1"},
		&aicontext.ResourceSummary{Kind: "Pod", Name: "api-2"},
	}
	// Length mismatch (1 obj vs 2 results) — must not panic, must skip.
	attachSummaryContextToList(results, objs, func(obj runtime.Object, _ *unstructured.Unstructured, group, kind, namespace, name string) *resourcecontext.SummaryContext {
		return &resourcecontext.SummaryContext{Health: "healthy"}
	})
	for i, row := range results {
		summary, ok := row.(*aicontext.ResourceSummary)
		if !ok {
			t.Fatalf("row %d: unexpected type %T", i, row)
		}
		if summary.SummaryContext != nil {
			t.Errorf("row %d: SummaryContext should be nil on length mismatch, got %#v", i, summary.SummaryContext)
		}
	}
}

// TestAttachSummaryContextToUnstructuredList covers the dynamic-CRD
// path. summarizeUnstructured returns *ResourceSummary so the attach
// helper is symmetric with the typed path.
func TestAttachSummaryContextToUnstructuredList(t *testing.T) {
	items := []*unstructured.Unstructured{
		{Object: map[string]any{
			"apiVersion": "argoproj.io/v1alpha1",
			"kind":       "Application",
			"metadata":   map[string]any{"name": "storefront", "namespace": "argocd"},
			"status":     map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}},
		}},
	}
	want := map[string]*resourcecontext.SummaryContext{
		"argoproj.io|Application|argocd|storefront": {
			Health:     "healthy",
			IssueCount: 1,
		},
	}

	results := []any{aicontext.MinifyUnstructured(items[0], aicontext.LevelSummary)}
	attachSummaryContextToUnstructuredList(results, items, stubBuilder(t, want))

	summary, ok := results[0].(*aicontext.ResourceSummary)
	if !ok || summary == nil {
		t.Fatalf("unexpected row type %T", results[0])
	}
	if summary.SummaryContext == nil {
		t.Fatalf("SummaryContext not attached")
	}
	if summary.SummaryContext.Health != "healthy" {
		t.Errorf("Health = %q, want healthy", summary.SummaryContext.Health)
	}
	if summary.SummaryContext.IssueCount != 1 {
		t.Errorf("IssueCount = %d, want 1", summary.SummaryContext.IssueCount)
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
	got := managedByFromRelationships(rel)
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
	got := managedByFromRelationships(rel)
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
	got := managedByFromRelationships(rel)
	if got == nil || got.Kind != "Application" || got.Source != "argocd" {
		t.Errorf("got %#v, want Application/argocd", got)
	}
}

func TestManagedByFromRelationships_NilSafe(t *testing.T) {
	if got := managedByFromRelationships(nil); got != nil {
		t.Errorf("nil rel: got %#v, want nil", got)
	}
	if got := managedByFromRelationships(&topology.Relationships{}); got != nil {
		t.Errorf("empty rel: got %#v, want nil", got)
	}
}

// TestIssueIndexKey_GroupAware pins that two resources sharing
// kind+namespace+name but in different API groups get independent
// counts. Without group in the key, e.g. Knative serving.knative.dev/
// Service vs corev1 ""/Service collapse onto one bucket — and either
// the CRD inherits the core Service's count or vice versa. This breaks
// the moment a user has two operators each shipping a kind named
// "Cluster" in the same namespace.
func TestIssueIndexKey_GroupAware(t *testing.T) {
	idx := issueIndex{}
	// Same kind+ns+name, different groups — must be independent buckets.
	idx[issueIndexKey("", "Service", "prod", "api")] = 2
	idx[issueIndexKey("serving.knative.dev", "Service", "prod", "api")] = 5

	if got := idx.count("", "Service", "prod", "api"); got != 2 {
		t.Errorf("core Service count = %d, want 2 (Knative bucket bleeding through?)", got)
	}
	if got := idx.count("serving.knative.dev", "Service", "prod", "api"); got != 5 {
		t.Errorf("Knative Service count = %d, want 5 (collided with core Service bucket?)", got)
	}
	// Wrong group lookup is a miss, not a fallback.
	if got := idx.count("example.io", "Service", "prod", "api"); got != 0 {
		t.Errorf("unknown-group lookup = %d, want 0 (key should not coalesce across groups)", got)
	}
}

// TestBuildIssueIndex_GroupAware exercises the full buildIssueIndex
// path with two CRDs that share kind+namespace+name but live in
// different API groups. Pre-fix, both rows landed under the same
// "service|prod|api" key and one inherited the other's count.
func TestBuildIssueIndex_GroupAware(t *testing.T) {
	// Inject via a fake issues.Provider rather than the cache plumbing —
	// keeps the test focused on the index-key arithmetic.
	p := &fakeIssuesProvider{
		problems: []k8sProblem{
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
// out counts for tail resources. The fix is Limit:NoLimit — the index
// is a bucketed count, not a paginated list.
func TestBuildIssueIndex_BeyondMaxLimit(t *testing.T) {
	// Generate MaxLimit+50 problem rows across distinct resources so
	// every bucket has exactly one issue. Without the NoLimit fix, the
	// last 50 resources' counts collapse to 0.
	probs := make([]k8sProblem, 0, issuesMaxLimit+50)
	for i := 0; i < issuesMaxLimit+50; i++ {
		probs = append(probs, k8sProblem{
			Kind: "Pod", Namespace: "prod", Name: fmtPodName(i), Reason: "ImagePullBackOff", Severity: "warning",
		})
	}
	p := &fakeIssuesProvider{problems: probs}
	idx := buildIssueIndex(p, nil, "")
	// Spot-check a tail resource — anything beyond MaxLimit must still
	// resolve to count=1, not 0.
	tailName := fmtPodName(issuesMaxLimit + 25)
	if got := idx.count("", "Pod", "prod", tailName); got != 1 {
		t.Fatalf("tail pod %s count = %d, want 1 (silent MaxLimit truncation?)", tailName, got)
	}
	// And the first resource sees its count too — sanity that the
	// truncation didn't shift in the other direction.
	if got := idx.count("", "Pod", "prod", fmtPodName(0)); got != 1 {
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
		if got := canonicalSingular(in); got != want {
			t.Errorf("canonicalSingular(%q) = %q, want %q", in, got, want)
		}
	}
}

// contains is a tiny strings.Contains alias kept local so the test file
// doesn't need a strings import alongside the existing imports.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
