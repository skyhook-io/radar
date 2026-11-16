// Package longhorn reads Longhorn's own objects (longhorn.io/v1beta2).
// Fields are those of the v1beta2 types in Longhorn 1.10 to 1.13; a field a
// version doesn't write reads as unknown, never as zero.
package longhorn

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const Group = "longhorn.io"

// Health is how a Volume reads: healthy, degraded (fewer healthy replicas than requested),
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

// CSIDriver is the driver a Longhorn PersistentVolume names; its
// spec.csi.volumeHandle is the Volume's name.
const CSIDriver = "driver.longhorn.io"
