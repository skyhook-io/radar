// Package longhorn reads Longhorn's own objects (longhorn.io/v1beta2).
// Fields are those of the v1beta2 types in Longhorn 1.10 to 1.13; a field a
// version doesn't write reads as unknown, never as zero.
package longhorn

import (
	"sort"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const Group = "longhorn.io"

// Health is how a Volume reads: healthy, degraded (replicas rebuilding),
// faulted (no usable replica), detached (not attached, not a problem) or
// unknown.
type Health string

const (
	HealthHealthy  Health = "healthy"
	HealthDegraded Health = "degraded"
	HealthFaulted  Health = "faulted"
	HealthDetached Health = "detached"
	HealthUnknown  Health = "unknown"
)

// VolumeHealth reads status.robustness, and status.state for a detached
// volume (whose robustness Longhorn reports as unknown).
func VolumeHealth(v *unstructured.Unstructured) Health {
	if v == nil {
		return HealthUnknown
	}
	robustness, _, _ := unstructured.NestedString(v.Object, "status", "robustness")
	switch robustness {
	case "healthy":
		return HealthHealthy
	case "degraded":
		return HealthDegraded
	case "faulted":
		return HealthFaulted
	}
	if state, _, _ := unstructured.NestedString(v.Object, "status", "state"); state == "detached" && robustness == "unknown" {
		return HealthDetached
	}
	return HealthUnknown
}

// HealthyReplicas counts the Engine's replicas in mode RW. ok is false when
// the Engine or its replicaModeMap is missing: the count is then unknown,
// never the desired replica count.
func HealthyReplicas(engine *unstructured.Unstructured) (int, bool) {
	if engine == nil {
		return 0, false
	}
	modes, found, _ := unstructured.NestedStringMap(engine.Object, "status", "replicaModeMap")
	if !found {
		return 0, false
	}
	n := 0
	for _, mode := range modes {
		if mode == "RW" {
			n++
		}
	}
	return n, true
}

// RebuildProgress is one replica rebuild from the Engine's
// status.rebuildStatus, keyed there by replica address.
type RebuildProgress struct {
	Replica  string
	Progress int
	State    string
	Error    string
}

// Rebuild lists the rebuilds in progress or failed, by replica address.
func Rebuild(engine *unstructured.Unstructured) []RebuildProgress {
	if engine == nil {
		return nil
	}
	statuses, _, _ := unstructured.NestedMap(engine.Object, "status", "rebuildStatus")
	var out []RebuildProgress
	for replica, raw := range statuses {
		s, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		rebuilding, _, _ := unstructured.NestedBool(s, "isRebuilding")
		errText, _, _ := unstructured.NestedString(s, "error")
		if !rebuilding && errText == "" {
			continue
		}
		progress, _, _ := unstructured.NestedInt64(s, "progress")
		state, _, _ := unstructured.NestedString(s, "state")
		out = append(out, RebuildProgress{Replica: replica, Progress: int(progress), State: state, Error: errText})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Replica < out[j].Replica })
	return out
}
