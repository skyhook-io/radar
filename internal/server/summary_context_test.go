// Wiring tests for the REST-side ResourceSummaryContext builders. The pure-
// function tests (issueIndex key arithmetic, BuildIssueIndex over a
// fake provider, CanonicalSingular, ManagedByFromRelationships) live in
// internal/summarycontext alongside the shared core they exercise.

package server

import (
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/internal/summarycontext"
	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

// stubBuilder records calls and returns a deterministic ResourceSummaryContext
// keyed by the resource identity. Avoids standing up a topology cache or
// issue provider — those are exercised by the per-layer unit tests.
//
// Key shape mirrors the production issueIndexKey (group|kind|ns|name)
// so test fixtures pin the group-aware lookup.
func stubBuilder(t *testing.T, want map[string]*resourcecontext.ResourceSummaryContext) summarycontext.Builder {
	t.Helper()
	return func(obj runtime.Object, u *unstructured.Unstructured, group, kind, namespace, name string) *resourcecontext.ResourceSummaryContext {
		key := group + "|" + kind + "|" + namespace + "|" + name
		return want[key]
	}
}

// TestAttachResourceSummaryContextToList wires together MinifyList + the
// per-row attach helper and asserts the ResourceSummaryContext field lands in
// the JSON each row marshals to.
func TestAttachResourceSummaryContextToList(t *testing.T) {
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
	want := map[string]*resourcecontext.ResourceSummaryContext{
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
	summarycontext.AttachToTypedList(results, objs, stubBuilder(t, want))

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

// TestAttachResourceSummaryContextToList_MismatchedLengthsSilent — defensive
// path that protects against a future refactor where MinifyList might
// drop unsupported kinds. Attach must skip rather than panic.
func TestAttachResourceSummaryContextToList_MismatchedLengthsSilent(t *testing.T) {
	objs := []runtime.Object{
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "api-1"}},
	}
	results := []any{
		&aicontext.ResourceSummary{Kind: "Pod", Name: "api-1"},
		&aicontext.ResourceSummary{Kind: "Pod", Name: "api-2"},
	}
	// Length mismatch (1 obj vs 2 results) — must not panic, must skip.
	summarycontext.AttachToTypedList(results, objs, func(obj runtime.Object, _ *unstructured.Unstructured, group, kind, namespace, name string) *resourcecontext.ResourceSummaryContext {
		return &resourcecontext.ResourceSummaryContext{Health: "healthy"}
	})
	for i, row := range results {
		summary, ok := row.(*aicontext.ResourceSummary)
		if !ok {
			t.Fatalf("row %d: unexpected type %T", i, row)
		}
		if summary.SummaryContext != nil {
			t.Errorf("row %d: ResourceSummaryContext should be nil on length mismatch, got %#v", i, summary.SummaryContext)
		}
	}
}

// TestAttachResourceSummaryContextToUnstructuredList covers the dynamic-CRD
// path. summarizeUnstructured returns *ResourceSummary so the attach
// helper is symmetric with the typed path.
func TestAttachResourceSummaryContextToUnstructuredList(t *testing.T) {
	items := []*unstructured.Unstructured{
		{Object: map[string]any{
			"apiVersion": "argoproj.io/v1alpha1",
			"kind":       "Application",
			"metadata":   map[string]any{"name": "storefront", "namespace": "argocd"},
			"status":     map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}},
		}},
	}
	want := map[string]*resourcecontext.ResourceSummaryContext{
		"argoproj.io|Application|argocd|storefront": {
			Health:     "healthy",
			IssueCount: 1,
		},
	}

	results := []any{aicontext.MinifyUnstructured(items[0], aicontext.LevelSummary)}
	summarycontext.AttachToUnstructuredList(results, items, stubBuilder(t, want))

	summary, ok := results[0].(*aicontext.ResourceSummary)
	if !ok || summary == nil {
		t.Fatalf("unexpected row type %T", results[0])
	}
	if summary.SummaryContext == nil {
		t.Fatalf("ResourceSummaryContext not attached")
	}
	if summary.SummaryContext.Health != "healthy" {
		t.Errorf("Health = %q, want healthy", summary.SummaryContext.Health)
	}
	if summary.SummaryContext.IssueCount != 1 {
		t.Errorf("IssueCount = %d, want 1", summary.SummaryContext.IssueCount)
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

// TestIssueIndexNamespaces_ClusterScopedDropsFilter pins the fix for the
// "cluster-scoped issues filtered out for cluster-scoped rows" bug.
// Pre-fix, handleAIListResources passed the user's namespaced-access set
// straight into the issue index. For cluster-scoped kinds (Node, PV,
// cluster-scoped CRDs) every issue lives at namespace="" — the index
// then dropped them all, silently zeroing issueCount on every row even
// when the user had cluster-scoped read access. The helper now returns
// nil for cluster-scoped kinds so Compose runs cluster-wide.
func TestIssueIndexNamespaces_ClusterScopedDropsFilter(t *testing.T) {
	userNs := []string{"prod", "staging"}

	// Cluster-scoped built-ins from the static catalogue (ClassifyKindScope
	// hits ClusterOnlyKindGVR before touching discovery, so this works
	// without a discovery client wired up).
	clusterCases := []struct {
		kind  string
		group string
	}{
		{"Node", ""},
		{"nodes", ""},
		{"PersistentVolume", ""},
		{"ClusterRole", "rbac.authorization.k8s.io"},
		{"StorageClass", "storage.k8s.io"},
	}
	for _, tc := range clusterCases {
		got := issueIndexNamespaces(userNs, tc.kind, tc.group)
		if got != nil {
			t.Errorf("issueIndexNamespaces(%q, %q) = %v, want nil — cluster-scoped kinds must not be namespace-filtered",
				tc.kind, tc.group, got)
		}
	}

	// Namespaced kinds preserve the user's namespace set as-is so the
	// scoping the per-user RBAC enforced upstream is honored.
	namespacedCases := []struct {
		kind  string
		group string
	}{
		{"Pod", ""},
		{"Deployment", "apps"},
		{"ConfigMap", ""},
	}
	for _, tc := range namespacedCases {
		got := issueIndexNamespaces(userNs, tc.kind, tc.group)
		if len(got) != len(userNs) {
			t.Errorf("issueIndexNamespaces(%q, %q) len = %d, want %d (namespace filter must pass through for namespaced kinds)",
				tc.kind, tc.group, len(got), len(userNs))
			continue
		}
		for i := range got {
			if got[i] != userNs[i] {
				t.Errorf("issueIndexNamespaces(%q, %q)[%d] = %q, want %q",
					tc.kind, tc.group, i, got[i], userNs[i])
			}
		}
	}

	// Pass-through when caller already provided nil (cluster-wide).
	if got := issueIndexNamespaces(nil, "Pod", ""); got != nil {
		t.Errorf("issueIndexNamespaces(nil, Pod) = %v, want nil", got)
	}
}
