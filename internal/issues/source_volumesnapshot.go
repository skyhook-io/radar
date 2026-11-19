package issues

import (
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// VolumeSnapshotGroup is the CSI snapshot API group.
const VolumeSnapshotGroup = "snapshot.storage.k8s.io"

// ReasonVolumeSnapshotError marks a VolumeSnapshot whose controller reports
// an error and has not made it ready to use.
const ReasonVolumeSnapshotError = "VolumeSnapshotError"

// VolumeSnapshotIssues reports a VolumeSnapshot carrying status.error while
// it is not ready to use. The snapshot controller copies the error from the
// bound VolumeSnapshotContent, keeps retrying, and clears it on success, so a
// present error is the current failure, not history. VolumeSnapshotContents
// are not assessed separately: their error reaches the snapshot, and reporting
// both would count one failure twice. The API says the message should not
// contain sensitive information, so it is quoted.
func VolumeSnapshotIssues(gvr schema.GroupVersionResource, u *unstructured.Unstructured) []Issue {
	if u == nil || u.GetKind() != "VolumeSnapshot" || u.GetDeletionTimestamp() != nil {
		return nil
	}
	if ready, found, _ := unstructured.NestedBool(u.Object, "status", "readyToUse"); found && ready {
		return nil
	}
	snapshotErr, found, _ := unstructured.NestedMap(u.Object, "status", "error")
	if !found {
		return nil
	}
	detail, _ := snapshotErr["message"].(string)
	detail = strings.TrimSpace(detail)
	message := "The snapshot controller reports an error for this VolumeSnapshot and keeps retrying; it is not ready to use"
	if detail != "" {
		message += ": " + detail
	}
	at, known := time.Time{}, false
	if raw, ok := snapshotErr["time"].(string); ok {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			at, known = t, true
		}
	}
	out, _ := filterShapedIssues([]Issue{newConditionIssue(gvr, "VolumeSnapshot", u.GetNamespace(), u.GetName(), SeverityWarning, ReasonVolumeSnapshotError, message, at, known, "", u.GetCreationTimestamp().Time)}, Filters{})
	return out
}
