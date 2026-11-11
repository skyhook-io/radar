package k8s

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func fluxHelmRelease(status map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
		"metadata": map[string]any{"namespace": "team", "name": "app"},
		"status":   status,
	}}
}

func helmEntry(version int64, chart, lastDeployed string) map[string]any {
	return map[string]any{"version": version, "chartVersion": chart, "status": "deployed", "lastDeployed": lastDeployed}
}

func diffPath(diff *DiffInfo, path string) bool {
	if diff == nil {
		return false
	}
	for _, f := range diff.Fields {
		if f.Path == path {
			return true
		}
	}
	return false
}

func TestFluxHelmReleaseDiffNamesChartChange(t *testing.T) {
	oldHR := fluxHelmRelease(map[string]any{"history": []any{helmEntry(58, "5.4.0", "2026-11-09T09:00:00Z")}})
	newHR := fluxHelmRelease(map[string]any{"history": []any{helmEntry(59, "5.5.0", "2026-11-10T09:00:00Z"), helmEntry(58, "5.4.0", "2026-11-09T09:00:00Z")}})
	diff := ComputeDiff("HelmRelease", oldHR, newHR)
	if !diffPath(diff, "status.history[0]") || !strings.Contains(diff.Summary, "chart 5.4.0 → 5.5.0 (release 58 → 59)") {
		t.Fatalf("diff = %+v", diff)
	}
}

func TestFluxHelmReleaseDiffReadsStringAppliedRevision(t *testing.T) {
	diff := ComputeDiff("HelmRelease", fluxHelmRelease(map[string]any{"lastAppliedRevision": "5.4.0"}), fluxHelmRelease(map[string]any{"lastAppliedRevision": "5.5.0"}))
	if !diffPath(diff, "status.lastAppliedRevision") {
		t.Fatalf("diff = %+v", diff)
	}
}

func TestFluxHelmReleaseDiffKeepsInt64AppliedRevision(t *testing.T) {
	diff := ComputeDiff("HelmRelease", fluxHelmRelease(map[string]any{"lastAppliedRevision": int64(3)}), fluxHelmRelease(map[string]any{"lastAppliedRevision": int64(4)}))
	if !diffPath(diff, "status.lastAppliedRevision") {
		t.Fatalf("diff = %+v", diff)
	}
}

func TestFluxHelmReleaseDiffIgnoresUnchangedHistory(t *testing.T) {
	oldHR := fluxHelmRelease(map[string]any{"history": []any{helmEntry(59, "5.5.0", "2026-11-10T09:00:00Z")}})
	newHR := fluxHelmRelease(map[string]any{"history": []any{helmEntry(59, "5.5.0", "2026-11-10T10:00:00Z")}})
	if diffPath(ComputeDiff("HelmRelease", oldHR, newHR), "status.history[0]") {
		t.Fatal("unchanged release reported as a chart change")
	}
}

func TestFluxHelmReleaseDiffNamesReleaseOutcome(t *testing.T) {
	failed := helmEntry(60, "5.6.0", "2026-11-11T09:00:00Z")
	failed["status"] = "failed"
	oldHR := fluxHelmRelease(map[string]any{"history": []any{helmEntry(59, "5.5.0", "2026-11-10T09:00:00Z")}})
	newHR := fluxHelmRelease(map[string]any{"history": []any{failed, helmEntry(59, "5.5.0", "2026-11-10T09:00:00Z")}})
	if diff := ComputeDiff("HelmRelease", oldHR, newHR); diff == nil || !strings.Contains(diff.Summary, "chart 5.5.0 → 5.6.0 (release 59 → 60, failed)") {
		t.Fatalf("diff = %+v", diff)
	}
}
