package context

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func volumeSnapshotObject(kind string, fields map[string]any) *unstructured.Unstructured {
	obj := map[string]any{
		"apiVersion": "snapshot.storage.k8s.io/v1",
		"kind":       kind,
		"metadata":   map[string]any{"name": "snap"},
	}
	for k, v := range fields {
		obj[k] = v
	}
	return &unstructured.Unstructured{Object: obj}
}

func TestVolumeSnapshotSummaryStatus(t *testing.T) {
	failure := map[string]any{"message": "rpc error: code = Internal"}
	for _, tc := range []struct {
		name, kind                       string
		status                           any
		wantStatus, wantReady, wantIssue string
	}{
		{"snapshot without status", "VolumeSnapshot", nil, "Pending", "", ""},
		{"snapshot bound, readiness unreported", "VolumeSnapshot", map[string]any{"boundVolumeSnapshotContentName": "c"}, "Readiness unknown", "", ""},
		{"snapshot not ready", "VolumeSnapshot", map[string]any{"boundVolumeSnapshotContentName": "c", "readyToUse": false}, "Not ready", "false", ""},
		{"snapshot ready", "VolumeSnapshot", map[string]any{"boundVolumeSnapshotContentName": "c", "readyToUse": true}, "Ready", "true", ""},
		{"snapshot error", "VolumeSnapshot", map[string]any{"readyToUse": false, "error": failure}, "Error", "false", "VolumeSnapshotError"},
		{"ready wins over a leftover error", "VolumeSnapshot", map[string]any{"readyToUse": true, "error": failure}, "Ready", "true", ""},
		{"content not yet processed", "VolumeSnapshotContent", nil, "Pending", "", ""},
		{"content ready", "VolumeSnapshotContent", map[string]any{"readyToUse": true, "snapshotHandle": "h"}, "Ready", "true", ""},
		{"content error", "VolumeSnapshotContent", map[string]any{"error": failure}, "Error", "", "VolumeSnapshotError"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fields := map[string]any{}
			if tc.status != nil {
				fields["status"] = tc.status
			}
			s := summarizeUnstructured(volumeSnapshotObject(tc.kind, fields))
			if s.Status != tc.wantStatus || s.Ready != tc.wantReady || s.Issue != tc.wantIssue {
				t.Fatalf("got status=%q ready=%q issue=%q", s.Status, s.Ready, s.Issue)
			}
		})
	}
}

func TestVolumeSnapshotSummaryTerminating(t *testing.T) {
	u := volumeSnapshotObject("VolumeSnapshot", map[string]any{"status": map[string]any{"readyToUse": true}})
	now := metav1.Now()
	u.SetDeletionTimestamp(&now)
	if s := summarizeUnstructured(u); s.Status != "Terminating" || !s.Terminating {
		t.Fatalf("got %+v", s)
	}
}

func TestVolumeSnapshotSummaryFields(t *testing.T) {
	snap := summarizeUnstructured(volumeSnapshotObject("VolumeSnapshot", map[string]any{
		"spec":   map[string]any{"source": map[string]any{"persistentVolumeClaimName": "data"}, "volumeSnapshotClassName": "csi-hostpath"},
		"status": map[string]any{"readyToUse": true, "restoreSize": "1Gi"},
	}))
	if snap.Target != "PersistentVolumeClaim/data" || snap.Capacity != "1Gi" {
		t.Fatalf("snapshot: %+v", snap)
	}
	preProvisioned := summarizeUnstructured(volumeSnapshotObject("VolumeSnapshot", map[string]any{
		"spec": map[string]any{"source": map[string]any{"volumeSnapshotContentName": "imported"}},
	}))
	if preProvisioned.Target != "VolumeSnapshotContent/imported" || preProvisioned.Capacity != "" {
		t.Fatalf("pre-provisioned snapshot: %+v", preProvisioned)
	}

	content := summarizeUnstructured(volumeSnapshotObject("VolumeSnapshotContent", map[string]any{
		"spec": map[string]any{
			"driver": "hostpath.csi.k8s.io", "deletionPolicy": "Retain",
			"volumeSnapshotRef": map[string]any{"name": "data-snap", "namespace": "demo"},
		},
		"status": map[string]any{"readyToUse": true, "restoreSize": int64(1073741824)},
	}))
	if content.Type != "hostpath.csi.k8s.io" || content.Strategy != "Retain" || content.Target != "VolumeSnapshot/demo/data-snap" || content.Capacity != "1Gi" {
		t.Fatalf("content: %+v", content)
	}

	class := volumeSnapshotObject("VolumeSnapshotClass", map[string]any{"driver": "hostpath.csi.k8s.io", "deletionPolicy": "Delete"})
	class.SetAnnotations(map[string]string{"snapshot.storage.kubernetes.io/is-default-class": "true"})
	if s := summarizeUnstructured(class); s.Type != "hostpath.csi.k8s.io" || s.Strategy != "Delete" || s.Status != "Default" {
		t.Fatalf("class: %+v", s)
	}
}

func TestVolumeSnapshotSummaryIgnoresOtherGroups(t *testing.T) {
	u := volumeSnapshotObject("VolumeSnapshot", map[string]any{"status": map[string]any{"readyToUse": true}})
	u.SetAPIVersion("volumesnapshot.external-storage.k8s.io/v1")
	if s := summarizeUnstructured(u); s.Status == "Ready" {
		t.Fatalf("foreign group used the CSI summary: %+v", s)
	}
}
