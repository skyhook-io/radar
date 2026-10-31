package gitops

import (
	"encoding/json"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestFluxLastApplyAttempt(t *testing.T) {
	root := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"status": map[string]any{
			"lastHandledReconcileAt": "2026-10-01T00:00:00Z",
			"history": []any{
				map[string]any{"firstReconciled": "2026-09-01T00:00:00Z", "lastReconciled": "2026-10-30T12:00:00.123456788Z"},
				map[string]any{"lastReconciled": "2026-10-30T14:00:00.123456789+02:00", "lastReconciledStatus": "HealthCheckFailed"},
				map[string]any{"lastReconciled": "2026-10-30T13:00:00+02:00"},
				map[string]any{"lastReconciled": "invalid"},
			},
		},
	}}
	if got := FluxLastApplyAttempt(root); got != "2026-10-30T12:00:00.123456789Z" {
		t.Fatalf("last apply = %q", got)
	}
	unstructured.RemoveNestedField(root.Object, "status", "history")
	if got := FluxLastApplyAttempt(root); got != "" {
		t.Fatalf("missing history invented apply time %q", got)
	}
	root.SetAPIVersion("example.io/v1")
	if got := FluxLastApplyAttempt(root); got != "" {
		t.Fatalf("unrelated kind got apply time %q", got)
	}
}

func TestFluxDeployedReleaseStandardJSONNumbers(t *testing.T) {
	var obj map[string]any
	if err := json.Unmarshal([]byte(`{"apiVersion":"helm.toolkit.fluxcd.io/v2","kind":"HelmRelease","status":{"history":[{"version":5,"chartVersion":"1.3.0","status":"failed"},{"version":4,"chartVersion":"1.2.0","status":"deployed","lastDeployed":"2026-10-27T12:00:00Z"}]}}`), &obj); err != nil {
		t.Fatal(err)
	}
	got := FluxDeployedRelease(&unstructured.Unstructured{Object: obj})
	if got == nil || got.Version != 4 || got.ChartVersion != "1.2.0" || got.LastDeployed != "2026-10-27T12:00:00Z" {
		t.Fatalf("standard JSON snapshot = %+v", got)
	}
}

func TestFluxDeployedReleaseKeepsIdentityAndTimeTogether(t *testing.T) {
	root := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
		"status": map[string]any{"history": []any{
			map[string]any{"version": int64(5), "chartVersion": "1.3.0", "status": "failed", "lastDeployed": "2026-10-30T00:00:00Z"},
			map[string]any{"version": int64(4), "chartVersion": "1.2.0", "status": "deployed", "lastDeployed": "2026-10-27T00:00:00Z"},
			map[string]any{"version": int64(3), "chartVersion": "1.1.0", "status": "superseded", "lastDeployed": "2026-10-26T00:00:00Z"},
		}},
	}}
	got := FluxDeployedRelease(root)
	if got == nil || got.Version != 4 || got.ChartVersion != "1.2.0" || got.LastDeployed != "2026-10-27T00:00:00Z" {
		t.Fatalf("deployed = %+v", got)
	}
	history, _, _ := unstructured.NestedSlice(root.Object, "status", "history")
	history = append(history, map[string]any{"version": int64(6), "chartVersion": "1.1.0", "status": "deployed", "lastDeployed": "invalid"})
	if err := unstructured.SetNestedSlice(root.Object, history, "status", "history"); err != nil {
		t.Fatal(err)
	}
	got = FluxDeployedRelease(root)
	if got == nil || got.Version != 6 || got.ChartVersion != "1.1.0" || got.LastDeployed != "" {
		t.Fatalf("rollback borrowed an older date: %+v", got)
	}
	root.SetAPIVersion("example.io/v1")
	if got := FluxDeployedRelease(root); got != nil {
		t.Fatalf("unrelated kind got deployed snapshot %+v", got)
	}
}
