package gitops

import (
	"math"
	"sort"
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
	LastDeployed string
	OCIDigest    string
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
		version := fluxReleaseVersion(entry)
		if status != "deployed" || version <= 0 || (selected != nil && version <= selected.Version) {
			continue
		}
		chartVersion, _, _ := unstructured.NestedString(entry, "chartVersion")
		ociDigest, _, _ := unstructured.NestedString(entry, "ociDigest")
		selected = &FluxDeployedSnapshot{Version: version, ChartVersion: chartVersion, OCIDigest: ociDigest}
		value, _, _ := unstructured.NestedString(entry, "lastDeployed")
		if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil && !parsed.IsZero() {
			selected.LastDeployed = parsed.UTC().Format(time.RFC3339Nano)
		}
	}
	return selected
}

func fluxReleaseVersion(entry map[string]any) int64 {
	version, _, _ := unstructured.NestedInt64(entry, "version")
	// Standard JSON map decoding uses float64; dynamic Kubernetes clients
	// use int64 for the same release version.
	if value, ok := entry["version"].(float64); ok && value > 0 && value < float64(math.MaxInt64) && math.Trunc(value) == value {
		version = int64(value)
	}
	return version
}

// FluxReconcileEntry is one Kustomization status.history entry: one
// revision Flux applied, with its run count and the last run's outcome.
type FluxReconcileEntry struct {
	Revision, Digest, FirstReconciled, LastReconciled, Duration, Status string
	Runs                                                                int64
}

// FluxKustomizationHistory returns status.history newest lastReconciled first.
func FluxKustomizationHistory(root *unstructured.Unstructured) []FluxReconcileEntry {
	if root == nil || root.GetKind() != "Kustomization" || ToolOf(root) != ToolFluxCD {
		return nil
	}
	history, _, _ := unstructured.NestedSlice(root.Object, "status", "history")
	var out []FluxReconcileEntry
	for _, raw := range history {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		var e FluxReconcileEntry
		e.Revision, _, _ = unstructured.NestedString(entry, "metadata", "revision")
		e.Digest, _, _ = unstructured.NestedString(entry, "digest")
		e.FirstReconciled, _, _ = unstructured.NestedString(entry, "firstReconciled")
		e.LastReconciled, _, _ = unstructured.NestedString(entry, "lastReconciled")
		e.Duration, _, _ = unstructured.NestedString(entry, "lastReconciledDuration")
		e.Status, _, _ = unstructured.NestedString(entry, "lastReconciledStatus")
		e.Runs = fluxInt(entry["totalReconciliations"])
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return fluxTime(out[i].LastReconciled).After(fluxTime(out[j].LastReconciled)) })
	return out
}

// FluxHelmEntry is one HelmRelease status.history entry (one Helm release).
type FluxHelmEntry struct {
	Version                                                                  int64
	ChartVersion, AppVersion, Status, FirstDeployed, LastDeployed, OCIDigest string
}

// FluxHelmHistory returns status.history highest release version first.
func FluxHelmHistory(root *unstructured.Unstructured) []FluxHelmEntry {
	if root == nil || root.GetKind() != "HelmRelease" || ToolOf(root) != ToolFluxCD {
		return nil
	}
	history, _, _ := unstructured.NestedSlice(root.Object, "status", "history")
	var out []FluxHelmEntry
	for _, raw := range history {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		e := FluxHelmEntry{Version: fluxReleaseVersion(entry)}
		e.ChartVersion, _, _ = unstructured.NestedString(entry, "chartVersion")
		e.AppVersion, _, _ = unstructured.NestedString(entry, "appVersion")
		e.Status, _, _ = unstructured.NestedString(entry, "status")
		e.FirstDeployed, _, _ = unstructured.NestedString(entry, "firstDeployed")
		e.LastDeployed, _, _ = unstructured.NestedString(entry, "lastDeployed")
		e.OCIDigest, _, _ = unstructured.NestedString(entry, "ociDigest")
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	return out
}

func fluxInt(value any) int64 {
	switch v := value.(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	}
	return 0
}

func fluxTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}
