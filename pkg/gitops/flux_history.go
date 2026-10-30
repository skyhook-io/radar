package gitops

import (
	"math"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// FluxLastApplyAttempt returns the latest completion recorded in Kustomization
// history. Failures before apply may not produce a history entry.
func FluxLastApplyAttempt(root *unstructured.Unstructured) string {
	if root == nil || root.GetKind() != "Kustomization" || ToolOf(root) != ToolFluxCD {
		return ""
	}
	history, _, _ := unstructured.NestedSlice(root.Object, "status", "history")
	var latest time.Time
	for _, raw := range history {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		value, _, _ := unstructured.NestedString(entry, "lastReconciled")
		if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil && parsed.After(latest) {
			latest = parsed
		}
	}
	if latest.IsZero() {
		return ""
	}
	return latest.UTC().Format(time.RFC3339Nano)
}

type FluxDeployedSnapshot struct {
	Version      int64
	ChartVersion string
	LastDeployed string
}

// FluxDeployedRelease keeps release identity and deployment time on the same
// snapshot, even when a newer upgrade failed.
func FluxDeployedRelease(root *unstructured.Unstructured) *FluxDeployedSnapshot {
	if root == nil || root.GetKind() != "HelmRelease" || ToolOf(root) != ToolFluxCD {
		return nil
	}
	history, _, _ := unstructured.NestedSlice(root.Object, "status", "history")
	var selected *FluxDeployedSnapshot
	for _, raw := range history {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		status, _, _ := unstructured.NestedString(entry, "status")
		version, _, _ := unstructured.NestedInt64(entry, "version")
		// Standard JSON map decoding uses float64; dynamic Kubernetes clients
		// use int64 for the same release version.
		if value, ok := entry["version"].(float64); ok && value > 0 && value < float64(math.MaxInt64) && math.Trunc(value) == value {
			version = int64(value)
		}
		if status != "deployed" || version <= 0 || (selected != nil && version <= selected.Version) {
			continue
		}
		chartVersion, _, _ := unstructured.NestedString(entry, "chartVersion")
		selected = &FluxDeployedSnapshot{Version: version, ChartVersion: chartVersion}
		value, _, _ := unstructured.NestedString(entry, "lastDeployed")
		if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil && !parsed.IsZero() {
			selected.LastDeployed = parsed.UTC().Format(time.RFC3339Nano)
		}
	}
	return selected
}
