package insights

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func fluxKustomization(status map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]any{"namespace": "team", "name": "app"},
		"status":   status,
	}}
}

func kustomizationHistory() []any {
	return []any{
		map[string]any{"digest": "sha256:old", "firstReconciled": "2026-11-10T08:00:00Z", "lastReconciled": "2026-11-10T10:00:00Z",
			"lastReconciledDuration": "900ms", "lastReconciledStatus": "ReconciliationFailed", "totalReconciliations": int64(3),
			"metadata": map[string]any{"revision": "main@sha1:aaa"}},
		map[string]any{"digest": "sha256:new", "firstReconciled": "2026-11-10T10:30:00Z", "lastReconciled": "2026-11-10T11:00:00Z",
			"lastReconciledDuration": "1.1s", "lastReconciledStatus": "ReconciliationSucceeded", "totalReconciliations": int64(12),
			"metadata": map[string]any{"revision": "main@sha1:bbb"}},
	}
}

func TestFluxActivityKustomizationHistoryRows(t *testing.T) {
	rows := BuildFluxActivity(fluxKustomization(map[string]any{"history": kustomizationHistory()}), nil)
	want := []HistoryItem{
		{Kind: "revision", ID: "sha256:new", Revision: "main@sha1:bbb", FirstAt: "2026-11-10T10:30:00Z", DeployedAt: "2026-11-10T11:00:00Z", Duration: "1.1s", Runs: 12, Phase: "Applied"},
		{Kind: "revision", ID: "sha256:old", Revision: "main@sha1:aaa", FirstAt: "2026-11-10T08:00:00Z", DeployedAt: "2026-11-10T10:00:00Z", Duration: "900ms", Runs: 3, Phase: "Failed", Message: "ReconciliationFailed"},
	}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestFluxActivityHelmReleaseRows(t *testing.T) {
	root := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
		"metadata": map[string]any{"namespace": "team", "name": "app"},
		"status": map[string]any{"history": []any{
			map[string]any{"version": int64(58), "status": "superseded", "chartVersion": "5.4.0", "firstDeployed": "2026-11-09T09:00:00Z", "lastDeployed": "2026-11-09T09:00:00Z"},
			map[string]any{"version": int64(59), "status": "deployed", "chartVersion": "5.5.0", "firstDeployed": "2026-11-10T09:00:00Z", "lastDeployed": "2026-11-10T09:01:00Z"},
		}},
	}}
	rows := BuildFluxActivity(root, nil)
	if len(rows) != 2 || rows[0].Revision != "5.5.0 · release 59" || rows[0].Phase != "Deployed" || rows[0].ID != "59" || rows[0].FirstAt != "" || rows[0].DeployedAt != "2026-11-10T09:01:00Z" || rows[1].Phase != "Superseded" || rows[0].Kind != "revision" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestFluxActivityFallsBackToConditions(t *testing.T) {
	root := fluxKustomization(map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True", "reason": "ReconciliationSucceeded", "message": "Applied revision", "lastTransitionTime": "2026-11-10T11:00:00Z"}}})
	want := buildHistory(root, "fluxcd")
	for i := range want {
		want[i].Kind = "condition"
	}
	if got := BuildFluxActivity(root, nil); len(want) == 0 || !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %+v, want %+v", got, want)
	}
}

func TestFluxActivityConditionRowOnFailure(t *testing.T) {
	root := fluxKustomization(map[string]any{"history": kustomizationHistory(), "conditions": []any{
		map[string]any{"type": "Ready", "status": "False", "reason": "BuildFailed", "message": "kustomize build failed", "lastTransitionTime": "2026-11-10T09:00:00Z"}}})
	rows := BuildFluxActivity(root, nil)
	if len(rows) != 3 || rows[0].Kind != "condition" || rows[0].ID != "Ready" || rows[0].Phase != "Failed" || rows[0].Message != "kustomize build failed" || rows[1].Kind != "revision" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestFluxActivityFiltersRootEvents(t *testing.T) {
	resolver := &fakeResolver{events: map[string][]EventSummary{"app": {
		{Type: "Warning", Reason: "HealthCheckFailed", Message: "timeout", LastTimestamp: "2026-11-10T12:00:00Z", Count: 2},
		{Type: "Normal", Reason: "ReconciliationSucceeded", LastTimestamp: "2026-11-10T11:59:00Z"},
		{Type: "Normal", Reason: "ArtifactUpToDate", LastTimestamp: "2026-11-10T11:58:00Z"},
		{Type: "Normal", Reason: "Progressing", Message: "3 objects changed", LastTimestamp: "2026-11-10T10:45:00Z"},
	}}}
	rows := BuildFluxActivity(fluxKustomization(map[string]any{"history": kustomizationHistory()}), resolver)
	var events []HistoryItem
	for _, r := range rows {
		if r.Kind == "event" {
			events = append(events, r)
		}
	}
	want := []HistoryItem{
		{Kind: "event", ID: "HealthCheckFailed", Phase: "Warning", Message: "timeout", DeployedAt: "2026-11-10T12:00:00Z", Runs: 2},
		{Kind: "event", ID: "Progressing", Phase: "Normal", Message: "3 objects changed", DeployedAt: "2026-11-10T10:45:00Z"},
	}
	if !reflect.DeepEqual(events, want) || len(rows) != 4 || rows[0].ID != "HealthCheckFailed" || rows[2].ID != "Progressing" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestBuildUsesFluxActivityOnlyForFlux(t *testing.T) {
	app := argoApp(map[string]any{"history": []any{map[string]any{"id": int64(1), "revision": "abc", "deployedAt": "2026-11-10T09:00:00Z"}}})
	if got := Build(app, nil, nil).History; !reflect.DeepEqual(got, BuildHistory(app)) {
		t.Fatalf("argo history = %+v", got)
	}
	root := fluxKustomization(map[string]any{"history": kustomizationHistory(), "conditions": []any{map[string]any{"type": "Ready", "status": "True", "reason": "ReconciliationSucceeded"}}})
	if got := Build(root, nil, nil).History; len(got) != 2 || got[0].Kind != "revision" {
		t.Fatalf("flux Build history = %+v", got)
	}
	for _, row := range BuildHistory(root) {
		if row.Kind != "" {
			t.Fatalf("BuildHistory changed: %+v", row)
		}
	}
}

func TestFluxActivitySortsMixedTimeZones(t *testing.T) {
	resolver := &fakeResolver{events: map[string][]EventSummary{"app": {
		{Type: "Warning", Reason: "HealthCheckFailed", LastTimestamp: "2026-11-10T12:30:00+02:00"},
	}}}
	rows := BuildFluxActivity(fluxKustomization(map[string]any{"history": kustomizationHistory()}), resolver)
	// 10:30Z sits between the 11:00Z and 10:00Z history rows.
	if len(rows) != 3 || rows[0].Revision != "main@sha1:bbb" || rows[1].ID != "HealthCheckFailed" {
		t.Fatalf("rows = %+v", rows)
	}
}
