package gitops

import (
	"encoding/json"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestFluxLastApply(t *testing.T) {
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
	if got, succeeded := FluxLastApply(root); got != "2026-10-30T12:00:00.123456789Z" || succeeded {
		t.Fatalf("last apply = %q, succeeded = %v; want the failed health check", got, succeeded)
	}
	// A later success wins, and an older failure does not mark it failed.
	history, _, _ := unstructured.NestedSlice(root.Object, "status", "history")
	history = append(history, map[string]any{"lastReconciled": "2026-10-30T12:05:00Z", "lastReconciledStatus": "ReconciliationSucceeded"})
	_ = unstructured.SetNestedSlice(root.Object, history, "status", "history")
	if got, succeeded := FluxLastApply(root); got != "2026-10-30T12:05:00Z" || !succeeded {
		t.Fatalf("last apply = %q, succeeded = %v; want the later success", got, succeeded)
	}
	unstructured.RemoveNestedField(root.Object, "status", "history")
	if got, _ := FluxLastApply(root); got != "" {
		t.Fatalf("missing history invented apply time %q", got)
	}
	root.SetAPIVersion("example.io/v1")
	if got, _ := FluxLastApply(root); got != "" {
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

func TestFluxKustomizationHistoryReadsEntries(t *testing.T) {
	root := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"status": map[string]any{"history": []any{
			map[string]any{"digest": "sha256:old", "firstReconciled": "2026-11-10T08:00:00Z", "lastReconciled": "2026-11-10T10:00:00Z",
				"lastReconciledDuration": "900ms", "lastReconciledStatus": "ReconciliationFailed", "totalReconciliations": int64(3),
				"metadata": map[string]any{"revision": "main@sha1:aaa"}},
			map[string]any{"digest": "sha256:new", "firstReconciled": "2026-11-10T10:30:00Z", "lastReconciled": "2026-11-10T11:00:00Z",
				"lastReconciledDuration": "1.102930992s", "lastReconciledStatus": "ReconciliationSucceeded", "totalReconciliations": int64(12),
				"metadata": map[string]any{"revision": "main@sha1:bbb"}},
		}},
	}}
	got := FluxKustomizationHistory(root)
	if len(got) != 2 {
		t.Fatalf("entries = %d, want 2", len(got))
	}
	want := FluxReconcileEntry{Revision: "main@sha1:bbb", Digest: "sha256:new", FirstReconciled: "2026-11-10T10:30:00Z", LastReconciled: "2026-11-10T11:00:00Z", Duration: "1.102930992s", Status: "ReconciliationSucceeded", Runs: 12}
	if got[0] != want {
		t.Fatalf("newest = %+v, want %+v", got[0], want)
	}
	if got[1].Revision != "main@sha1:aaa" || got[1].Status != "ReconciliationFailed" || got[1].Runs != 3 {
		t.Fatalf("older = %+v", got[1])
	}
	if FluxHelmHistory(root) != nil {
		t.Fatal("Helm reader accepted a Kustomization")
	}
	root.SetAPIVersion("example.io/v1")
	if FluxKustomizationHistory(root) != nil {
		t.Fatal("non-Flux group got history")
	}
}

func TestFluxHelmHistoryReadsEntries(t *testing.T) {
	root := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
		"status": map[string]any{"history": []any{
			map[string]any{"version": float64(58), "status": "superseded", "chartVersion": "5.4.0"},
			map[string]any{"version": int64(59), "status": "deployed", "chartVersion": "5.5.0", "appVersion": "2.1.0",
				"firstDeployed": "2026-11-10T09:00:00Z", "lastDeployed": "2026-11-10T09:01:00Z", "ociDigest": "sha256:abc"},
		}},
	}}
	got := FluxHelmHistory(root)
	if len(got) != 2 || got[0].Version != 59 || got[1].Version != 58 {
		t.Fatalf("entries = %+v, want 59 then 58", got)
	}
	want := FluxHelmEntry{Version: 59, ChartVersion: "5.5.0", AppVersion: "2.1.0", Status: "deployed", FirstDeployed: "2026-11-10T09:00:00Z", LastDeployed: "2026-11-10T09:01:00Z", OCIDigest: "sha256:abc"}
	if got[0] != want {
		t.Fatalf("newest = %+v, want %+v", got[0], want)
	}
	if FluxKustomizationHistory(root) != nil {
		t.Fatal("Kustomization reader accepted a HelmRelease")
	}
	root.SetAPIVersion("example.io/v1")
	if FluxHelmHistory(root) != nil {
		t.Fatal("non-Flux group got history")
	}
}

func TestFluxDeployedReleaseCarriesOCIDigest(t *testing.T) {
	root := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
		"status": map[string]any{"history": []any{
			map[string]any{"version": int64(59), "status": "deployed", "chartVersion": "5.5.0", "ociDigest": "sha256:abc"},
		}},
	}}
	if got := FluxDeployedRelease(root); got == nil || got.OCIDigest != "sha256:abc" {
		t.Fatalf("deployed = %+v, want OCIDigest sha256:abc", got)
	}
}
