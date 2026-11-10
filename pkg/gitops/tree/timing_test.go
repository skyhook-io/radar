package tree

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"testing"
)

func TestEnrichFluxNodeDoesNotCallRequestMarkerAReconcile(t *testing.T) {
	root := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
		"status": map[string]any{"lastHandledReconcileAt": "2026-10-01T00:00:00Z", "history": []any{
			map[string]any{"version": int64(5), "chartVersion": "1.3.0", "status": "failed"},
			map[string]any{"version": int64(4), "chartVersion": "1.2.0", "status": "deployed"},
		}},
	}}
	node := enrichNodeFromObject(Node{}, root)
	if node.Data["revision"] != "1.2.0" {
		t.Fatalf("revision = %v", node.Data["revision"])
	}
	if _, exists := node.Data["lastReconciledAt"]; exists {
		t.Fatal("request marker was exported as reconcile time")
	}
}

func TestEnrichArgoNodeValidatesComparisonTime(t *testing.T) {
	root := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"status": map[string]any{"reconciledAt": "2026-10-30T14:00:00+02:00"},
	}}
	if got := enrichNodeFromObject(Node{}, root).Data["lastReconciledAt"]; got != "2026-10-30T12:00:00Z" {
		t.Fatalf("comparison time = %v", got)
	}
	root.SetAPIVersion("example.io/v1")
	if got := enrichNodeFromObject(Node{}, root).Data["lastReconciledAt"]; got != nil {
		t.Fatalf("unrelated resource comparison = %v", got)
	}
	root.SetAPIVersion("argoproj.io/v1alpha1")
	if err := unstructured.SetNestedField(root.Object, "not-a-time", "status", "reconciledAt"); err != nil {
		t.Fatal(err)
	}
	if got := enrichNodeFromObject(Node{}, root).Data["lastReconciledAt"]; got != nil {
		t.Fatalf("invalid comparison = %v", got)
	}
}
