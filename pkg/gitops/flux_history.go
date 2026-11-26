package gitops

import (
	"math"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// FluxLastApply returns the latest completion recorded in Kustomization
// history and whether Flux recorded it as successful. Failures before apply
// may not produce a history entry.
func FluxLastApply(root *unstructured.Unstructured) (at string, succeeded bool) {
	if root == nil || root.GetKind() != "Kustomization" || ToolOf(root) != ToolFluxCD {
		return "", false
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
			status, _, _ := unstructured.NestedString(entry, "lastReconciledStatus")
			succeeded = status == "ReconciliationSucceeded"
		}
	}
	if latest.IsZero() {
		return "", false
	}
	return latest.UTC().Format(time.RFC3339Nano), succeeded
}

type FluxDeployedSnapshot struct {
	Version      int64
	ChartVersion string
	Status       string
	LastDeployed string
}

// FluxReleaseSnapshots reads the release records retained by helm-controller.
func FluxReleaseSnapshots(root *unstructured.Unstructured) []FluxDeployedSnapshot {
	if root == nil || root.GetKind() != "HelmRelease" || ToolOf(root) != ToolFluxCD {
		return nil
	}
	history, _, _ := unstructured.NestedSlice(root.Object, "status", "history")
	var snapshots []FluxDeployedSnapshot
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
		if version <= 0 {
			continue
		}
		chartVersion, _, _ := unstructured.NestedString(entry, "chartVersion")
		snapshot := FluxDeployedSnapshot{Version: version, ChartVersion: chartVersion, Status: status}
		value, _, _ := unstructured.NestedString(entry, "lastDeployed")
		if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil && !parsed.IsZero() {
			snapshot.LastDeployed = parsed.UTC().Format(time.RFC3339Nano)
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots
}

// FluxDeployedRelease keeps release identity and deployment time on the same
// snapshot, even when a newer upgrade failed.
func FluxDeployedRelease(root *unstructured.Unstructured) *FluxDeployedSnapshot {
	var selected *FluxDeployedSnapshot
	for _, snapshot := range FluxReleaseSnapshots(root) {
		if snapshot.Status == "deployed" && (selected == nil || snapshot.Version > selected.Version) {
			selected = &snapshot
		}
	}
	return selected
}
