// Package gkedisk knows which Compute Engine disk types a GKE node's machine
// series can attach. Newer machine series attach only Hyperdisk, so a
// Persistent Disk (pd-*) volume scheduled onto one of their nodes never
// attaches: the CSI driver retries forever and the kubelet's later events
// carry only a backoff echo of the original Compute Engine error.
package gkedisk

import (
	"fmt"
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
)

const (
	CSIDriver       = "pd.csi.storage.gke.io"
	InTreeProvision = "kubernetes.io/gce-pd"

	InstanceTypeLabel = "node.kubernetes.io/instance-type"

	provisionedByAnnotation = "pv.kubernetes.io/provisioned-by"

	// Both provisioners default an unset "type" parameter to pd-standard.
	defaultDiskType = "pd-standard"
)

// hyperdiskOnlySeries lists machine series that support none of pd-standard,
// pd-balanced, pd-ssd or pd-extreme, per the Compute Engine disk-support
// matrix (https://cloud.google.com/compute/docs/disks/persistent-disks).
// Series with partial PD support (C3, C3D, H3) are deliberately absent: a
// missing series is never flagged, so the table can only under-report.
var hyperdiskOnlySeries = map[string]bool{
	"a4": true, "a4x": true,
	"c4": true, "c4a": true, "c4d": true, "c4n": true,
	"g4":  true,
	"h4d": true,
	"m4":  true, "m4n": true,
	"n4": true, "n4a": true, "n4d": true,
	"x4": true, "x5": true,
	"z4d": true,
}

// MachineSeries returns the series of a Compute Engine machine type:
// "n4d-standard-4" → "n4d", "n4-custom-4-8192" → "n4".
func MachineSeries(machineType string) string {
	series, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(machineType)), "-")
	return series
}

// HyperdiskOnly reports whether machineType belongs to a series that cannot
// attach any Persistent Disk type.
func HyperdiskOnly(machineType string) bool {
	return hyperdiskOnlySeries[MachineSeries(machineType)]
}

// IsPersistentDisk reports whether a disk type is a Persistent Disk type
// (pd-standard, pd-balanced, pd-ssd, pd-extreme) rather than Hyperdisk.
func IsPersistentDisk(diskType string) bool {
	return strings.HasPrefix(strings.ToLower(diskType), "pd-")
}

var incompatibleAttachRE = regexp.MustCompile(`([a-z0-9-]+) disk type cannot be used by ([a-z0-9-]+) machine type`)

// ParseIncompatibleAttach extracts the disk and machine type from Compute
// Engine's attach rejection, e.g. "googleapi: Error 400: pd-ssd disk type
// cannot be used by n4d-standard-4 machine type., badRequest".
func ParseIncompatibleAttach(message string) (diskType, machineType string, ok bool) {
	m := incompatibleAttachRE.FindStringSubmatch(strings.ToLower(message))
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// IsAttachBackoffEcho reports whether an attach-failure message is the CSI
// external-attacher's retry notice ("… due to backoff condition"), which
// repeats until the next real attempt and carries no cause of its own.
func IsAttachBackoffEcho(message string) bool {
	return strings.Contains(strings.ToLower(message), "backoff condition")
}

// DiskType returns the Compute Engine disk type a PersistentVolume was
// provisioned as. ok is false when the PV isn't a dynamically provisioned GCE
// Persistent Disk or Hyperdisk volume, or its StorageClass is unknown.
func DiskType(pv *corev1.PersistentVolume, storageClass func(name string) *storagev1.StorageClass) (string, bool) {
	if pv == nil {
		return "", false
	}
	switch {
	case pv.Spec.CSI != nil && pv.Spec.CSI.Driver == CSIDriver:
	case pv.Spec.GCEPersistentDisk != nil:
	default:
		return "", false
	}
	// A StorageClass's "type" describes disks it provisions. A pre-existing
	// disk can be bound under any class name, so without provisioning
	// provenance the class says nothing about the disk.
	if p := pv.Annotations[provisionedByAnnotation]; p != CSIDriver && p != InTreeProvision {
		return "", false
	}
	if pv.Spec.StorageClassName == "" || storageClass == nil {
		return "", false
	}
	sc := storageClass(pv.Spec.StorageClassName)
	if sc == nil || (sc.Provisioner != CSIDriver && sc.Provisioner != InTreeProvision) {
		return "", false
	}
	for key, value := range sc.Parameters {
		if strings.EqualFold(key, "type") {
			if t := strings.TrimSpace(value); t != "" {
				return strings.ToLower(t), true
			}
		}
	}
	return defaultDiskType, true
}

// IncompatibilityCause explains why a disk type can't attach to a machine type.
func IncompatibilityCause(diskType, machineType string) string {
	if IsPersistentDisk(diskType) && HyperdiskOnly(machineType) {
		return fmt.Sprintf("%s volumes can't attach to %s nodes: the %s machine series supports only Hyperdisk.",
			diskType, machineType, strings.ToUpper(MachineSeries(machineType)))
	}
	return fmt.Sprintf("Compute Engine doesn't allow %s disks on %s machines, so the volume never attaches.", diskType, machineType)
}

// IncompatibilityAction names the two fixes for an incompatible attach.
func IncompatibilityAction(diskType string) string {
	pin := fmt.Sprintf("Pin the workload to a node pool whose machine type supports %s (a nodeSelector or required node affinity on %s or cloud.google.com/machine-family)",
		diskType, InstanceTypeLabel)
	if IsPersistentDisk(diskType) {
		return pin + ", or move the data to a Hyperdisk StorageClass whose type every node pool the workload can run on supports."
	}
	return pin + ", or move the data to a StorageClass whose disk type every node pool the workload can run on supports."
}
