package insights

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestBuildFluxDatesDescribeRecordedEvents(t *testing.T) {
	kustomization := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"status": map[string]any{
			"lastAppliedRevision": "main@sha1:current", "lastAttemptedRevision": "main@sha1:failed",
			"lastHandledReconcileAt": "2026-10-01T00:00:00Z",
			"history":                []any{map[string]any{"lastReconciled": "2026-10-30T12:00:00Z", "lastReconciledStatus": "ReconciliationFailed"}},
		},
	}}
	summary := Build(kustomization, nil, nil).Summary
	if summary.LastRevision != "main@sha1:current" || summary.LastApplyAttempt != "2026-10-30T12:00:00Z" || summary.LastReconcile != "" {
		t.Fatalf("Kustomization summary = %+v", summary)
	}
	unstructured.RemoveNestedField(kustomization.Object, "status", "history")
	if summary := Build(kustomization, nil, nil).Summary; summary.LastApplyAttempt != "" || summary.LastReconcile != "" {
		t.Fatalf("missing history invented time: %+v", summary)
	}
	helm := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
		"status": map[string]any{"lastAttemptedRevision": "1.3.0", "lastHandledReconcileAt": "manual-marker", "history": []any{
			map[string]any{"version": int64(5), "chartVersion": "1.3.0", "status": "failed", "lastDeployed": "2026-10-30T12:00:00Z"},
			map[string]any{"version": int64(4), "chartVersion": "1.2.0", "status": "deployed", "lastDeployed": "2026-10-27T12:00:00Z"},
		}},
	}}
	summary = Build(helm, nil, nil).Summary
	if summary.LastRevision != "1.2.0" || summary.TargetRevision != "1.3.0" || summary.LastDeployed != "2026-10-27T12:00:00Z" || summary.LastReconcile != "" {
		t.Fatalf("Helm summary = %+v", summary)
	}
	history := []any{map[string]any{"version": int64(6), "status": "deployed", "lastDeployed": "2026-10-30T12:00:00Z"}}
	if err := unstructured.SetNestedField(helm.Object, "1.1.0", "status", "lastAppliedRevision"); err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedSlice(helm.Object, history, "status", "history"); err != nil {
		t.Fatal(err)
	}
	if summary := Build(helm, nil, nil).Summary; summary.LastDeployed != "" || summary.LastRevision != "1.1.0" {
		t.Fatalf("unknown deployed chart was paired with attempted revision: %+v", summary)
	}
}
