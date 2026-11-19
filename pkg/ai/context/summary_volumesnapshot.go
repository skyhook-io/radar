package context

import (
	"strconv"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const volumeSnapshotGroup = "snapshot.storage.k8s.io"

// volumeSnapshotState reads the readiness the snapshot controller (for a
// VolumeSnapshot) or the CSI sidecar (for a VolumeSnapshotContent) reports.
// The controller clears status.error once the snapshot is ready, so an error
// on a ready object is not a failure. Mirrors getVolumeSnapshotStatus in
// packages/k8s-ui.
func volumeSnapshotState(obj *unstructured.Unstructured) string {
	if obj.GetDeletionTimestamp() != nil {
		return "Terminating"
	}
	ready, readyFound, _ := unstructured.NestedBool(obj.Object, "status", "readyToUse")
	if readyFound && ready {
		return "Ready"
	}
	if _, found, _ := unstructured.NestedMap(obj.Object, "status", "error"); found {
		return "Error"
	}
	if readyFound {
		return "Not ready"
	}
	if obj.GetKind() == "VolumeSnapshot" {
		if bound, _, _ := unstructured.NestedString(obj.Object, "status", "boundVolumeSnapshotContentName"); bound == "" {
			return "Pending"
		}
	} else if _, found, _ := unstructured.NestedMap(obj.Object, "status"); !found {
		return "Pending"
	}
	return "Readiness unknown"
}

func volumeSnapshotReadiness(s *ResourceSummary, obj *unstructured.Unstructured) {
	s.Status = volumeSnapshotState(obj)
	if ready, found, _ := unstructured.NestedBool(obj.Object, "status", "readyToUse"); found {
		s.Ready = strconv.FormatBool(ready)
	}
	if s.Status == "Error" {
		s.Issue = "VolumeSnapshotError"
	}
}

func summarizeVolumeSnapshot(obj *unstructured.Unstructured) *ResourceSummary {
	s := &ResourceSummary{
		Kind:      "VolumeSnapshot",
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		Age:       age(obj.GetCreationTimestamp().Time),
	}
	volumeSnapshotReadiness(s, obj)
	if pvc, _, _ := unstructured.NestedString(obj.Object, "spec", "source", "persistentVolumeClaimName"); pvc != "" {
		s.Target = "PersistentVolumeClaim/" + pvc
	} else if content, _, _ := unstructured.NestedString(obj.Object, "spec", "source", "volumeSnapshotContentName"); content != "" {
		s.Target = "VolumeSnapshotContent/" + content
	}
	if size, _, _ := unstructured.NestedString(obj.Object, "status", "restoreSize"); size != "" {
		s.Capacity = size
	}
	return s
}

func summarizeVolumeSnapshotContent(obj *unstructured.Unstructured) *ResourceSummary {
	s := &ResourceSummary{
		Kind: "VolumeSnapshotContent",
		Name: obj.GetName(),
		Age:  age(obj.GetCreationTimestamp().Time),
	}
	volumeSnapshotReadiness(s, obj)
	s.Type, _, _ = unstructured.NestedString(obj.Object, "spec", "driver")
	s.Strategy, _, _ = unstructured.NestedString(obj.Object, "spec", "deletionPolicy")
	name, _, _ := unstructured.NestedString(obj.Object, "spec", "volumeSnapshotRef", "name")
	namespace, _, _ := unstructured.NestedString(obj.Object, "spec", "volumeSnapshotRef", "namespace")
	if name != "" {
		s.Target = "VolumeSnapshot/" + name
		if namespace != "" {
			s.Target = "VolumeSnapshot/" + namespace + "/" + name
		}
	}
	// The content reports restore size in bytes, unlike the snapshot's quantity.
	if size, found, _ := unstructured.NestedInt64(obj.Object, "status", "restoreSize"); found && size >= 0 {
		s.Capacity = resource.NewQuantity(size, resource.BinarySI).String()
	}
	return s
}

func summarizeVolumeSnapshotClass(obj *unstructured.Unstructured) *ResourceSummary {
	s := &ResourceSummary{
		Kind: "VolumeSnapshotClass",
		Name: obj.GetName(),
		Age:  age(obj.GetCreationTimestamp().Time),
	}
	s.Type, _, _ = unstructured.NestedString(obj.Object, "driver")
	s.Strategy, _, _ = unstructured.NestedString(obj.Object, "deletionPolicy")
	if obj.GetAnnotations()["snapshot.storage.kubernetes.io/is-default-class"] == "true" {
		s.Status = "Default"
	}
	return s
}
