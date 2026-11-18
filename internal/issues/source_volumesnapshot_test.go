package issues

import (
	"strings"
	"testing"
	"time"

	issuesapi "github.com/skyhook-io/radar/pkg/issuesapi"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var volumeSnapshotGVR = schema.GroupVersionResource{Group: VolumeSnapshotGroup, Version: "v1", Resource: "volumesnapshots"}

// Shaped like the status snapshot-controller v8.6 writes when the requested
// VolumeSnapshotClass does not exist.
func failingVolumeSnapshot() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "snapshot.storage.k8s.io/v1", "kind": "VolumeSnapshot",
		"metadata": map[string]any{"name": "data-snap", "namespace": "demo", "creationTimestamp": "2026-11-15T10:00:00Z"},
		"spec":     map[string]any{"source": map[string]any{"persistentVolumeClaimName": "data"}, "volumeSnapshotClassName": "missing"},
		"status": map[string]any{"readyToUse": false, "error": map[string]any{
			"time":    "2026-11-15T10:00:05Z",
			"message": "Failed to get snapshot class with error volumesnapshotclass.snapshot.storage.k8s.io \"missing\" not found",
		}},
	}}
}

func TestVolumeSnapshotIssues(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*unstructured.Unstructured)
		want   bool
	}{
		{"error while not ready", func(*unstructured.Unstructured) {}, true},
		{"error without readiness reported", func(u *unstructured.Unstructured) {
			unstructured.RemoveNestedField(u.Object, "status", "readyToUse")
		}, true},
		{"ready", func(u *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(u.Object, map[string]any{"readyToUse": true, "boundVolumeSnapshotContentName": "snapcontent-1"}, "status")
		}, false},
		// The controller clears the error on success; a ready snapshot that
		// still carries one is not failing.
		{"ready with leftover error", func(u *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(u.Object, true, "status", "readyToUse")
		}, false},
		{"not ready without error", func(u *unstructured.Unstructured) {
			unstructured.RemoveNestedField(u.Object, "status", "error")
		}, false},
		{"missing status", func(u *unstructured.Unstructured) { unstructured.RemoveNestedField(u.Object, "status") }, false},
		{"terminating", func(u *unstructured.Unstructured) { now := metav1.Now(); u.SetDeletionTimestamp(&now) }, false},
		{"content kind", func(u *unstructured.Unstructured) { u.SetKind("VolumeSnapshotContent") }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := failingVolumeSnapshot()
			tc.change(u)
			got := VolumeSnapshotIssues(volumeSnapshotGVR, u)
			if !tc.want {
				if len(got) != 0 {
					t.Fatalf("unexpected issue: %+v", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("got %d issues, want 1: %+v", len(got), got)
			}
			iss := got[0]
			if iss.Reason != ReasonVolumeSnapshotError || iss.Severity != SeverityWarning || iss.Kind != "VolumeSnapshot" || iss.Group != VolumeSnapshotGroup || iss.Namespace != "demo" || iss.Name != "data-snap" {
				t.Fatalf("got %+v", iss)
			}
			if !strings.Contains(iss.Message, `"missing" not found`) {
				t.Fatalf("message does not quote the controller error: %q", iss.Message)
			}
			if iss.Category != issuesapi.CategoryOperatorConditionFail {
				t.Fatalf("category = %q", iss.Category)
			}
			if want := time.Date(2026, 11, 15, 10, 0, 5, 0, time.UTC); iss.OnsetUnknown || !iss.FirstSeen.Equal(want) {
				t.Fatalf("onset = %v (unknown %v), want the error time %v", iss.FirstSeen, iss.OnsetUnknown, want)
			}
		})
	}
}

func TestVolumeSnapshotIssuesWithoutMessageOrTime(t *testing.T) {
	u := failingVolumeSnapshot()
	_ = unstructured.SetNestedField(u.Object, map[string]any{}, "status", "error")
	got := VolumeSnapshotIssues(volumeSnapshotGVR, u)
	if len(got) != 1 || !got[0].OnsetUnknown || strings.HasSuffix(got[0].Message, ": ") {
		t.Fatalf("got %+v", got)
	}
}
