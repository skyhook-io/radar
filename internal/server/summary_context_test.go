package server

import (
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	"github.com/skyhook-io/radar/pkg/topology"
)

// stubBuilder records calls and returns a deterministic SummaryContext
// keyed by the resource identity. Avoids standing up a topology cache or
// issue provider — those are exercised by the per-layer unit tests.
func stubBuilder(t *testing.T, want map[string]*resourcecontext.SummaryContext) summaryContextBuilder {
	t.Helper()
	return func(obj runtime.Object, u *unstructured.Unstructured, kind, namespace, name string) *resourcecontext.SummaryContext {
		key := kind + "|" + namespace + "|" + name
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
	want := map[string]*resourcecontext.SummaryContext{
		"Pod|prod|api-1": {
			ManagedBy:  &resourcecontext.ManagedByRef{Kind: "Deployment", Source: "native", Name: "api", Namespace: "prod"},
			Health:     "healthy",
			IssueCount: 0,
		},
		"Pod|prod|api-2": {
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
	attachSummaryContextToList(results, objs, func(obj runtime.Object, _ *unstructured.Unstructured, kind, namespace, name string) *resourcecontext.SummaryContext {
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
		"Application|argocd|storefront": {
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
