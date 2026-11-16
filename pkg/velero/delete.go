package velero

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// The labels Velero's deletion controller selects a backup's
// DeleteBackupRequests by (Velero v1.18.2 pkg/apis/velero/v1/labels.go), set
// up front as `velero backup delete` does.
const (
	BackupNameLabel = "velero.io/backup-name"
	BackupUIDLabel  = "velero.io/backup-uid"
)

// ValidLabelValue is Velero's label.GetValidName: a value longer than a label
// allows keeps its first 57 characters and gains 6 of its SHA-256.
func ValidLabelValue(value string) string {
	if len(value) <= maxLabelValue {
		return value
	}
	sum := sha256.Sum256([]byte(value))
	return value[:maxLabelValue-6] + hex.EncodeToString(sum[:])[:6]
}

// BackupStillRunning reports the phases Velero's backup tracker holds a backup
// in once it has started (InProgress and everything after it before a
// terminal phase). Velero refuses to delete a backup in them.
func BackupStillRunning(phase string) bool {
	switch phase {
	case "InProgress", "WaitingForPluginOperations", "WaitingForPluginOperationsPartiallyFailed",
		"Finalizing", "FinalizingPartiallyFailed":
		return true
	}
	return false
}

// DeleteBackupRequestFor is the request `velero backup delete` creates (Velero
// v1.18.2 pkg/cmd/cli/backup/delete.go): the backup's name in its spec, and its
// name and UID as labels, so Velero removes the request once the backup is gone.
func DeleteBackupRequestFor(namespace, backup, uid string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": Group + "/v1",
		"kind":       "DeleteBackupRequest",
		"metadata": map[string]any{
			"generateName": backup + "-",
			"namespace":    namespace,
			"labels": map[string]any{
				BackupNameLabel: ValidLabelValue(backup),
				BackupUIDLabel:  uid,
			},
		},
		"spec": map[string]any{"backupName": backup},
	}}
}

// DeletionRefusal is why Velero would refuse to delete a backup now, or "".
// The location is the backup's own (spec.storageLocation); nil when it could
// not be read, in which case only the backup's phase is checked.
func DeletionRefusal(backup *unstructured.Unstructured, location *unstructured.Unstructured) string {
	phase, _, _ := unstructured.NestedString(backup.Object, "status", "phase")
	if BackupStillRunning(phase) {
		return "This backup is still running (" + phase + "). Velero deletes a backup only once it has finished."
	}
	if loc, _ := LocationOf(backup); loc == "" {
		return "This backup has no storage location yet; wait for Velero to start it before deleting."
	}
	if location == nil {
		return ""
	}
	if mode, _, _ := unstructured.NestedString(location.Object, "spec", "accessMode"); mode == "ReadOnly" {
		return fmt.Sprintf("Its storage location %s is read-only, and Velero does not delete backups from a read-only location.", location.GetName())
	}
	if p, _, _ := unstructured.NestedString(location.Object, "status", "phase"); p != "Available" {
		return fmt.Sprintf("Its storage location %s is not available, and Velero deletes a backup only from an available location.", location.GetName())
	}
	return ""
}

// DeletionState is where one deletion stands, read from its request and the
// backup it names.
type DeletionState string

const (
	// DeletionWaiting: Velero has not picked the request up.
	DeletionWaiting DeletionState = "waiting"
	// DeletionInProgress: Velero is deleting the backup.
	DeletionInProgress DeletionState = "inProgress"
	// DeletionRefused: Velero checked the request and refused it before
	// deleting anything.
	DeletionRefused DeletionState = "refused"
	// DeletionFailed: Velero started deleting and reported errors. Some of the
	// backup may already be gone.
	DeletionFailed DeletionState = "failed"
	// DeletionDone: the backup the request was for no longer exists.
	DeletionDone DeletionState = "deleted"
	// DeletionUnknown: the request is gone and the backup is still there.
	// Velero removes a processed or in-progress request after 24 hours, so its
	// outcome can't be read any more.
	DeletionUnknown DeletionState = "unknown"
)

// DeletionReading is one deletion's state and Velero's errors, if it reported
// any.
type DeletionReading struct {
	State  DeletionState `json:"state"`
	Errors []string      `json:"errors,omitempty"`
}

// ReadDeletion reads a deletion from its request (nil when it no longer
// exists) and whether the backup it was for (by UID) still exists.
//
// Velero deletes a successful request along with the backup, so a missing
// request with the backup gone means it worked; with the backup still there it
// means nothing can be said.
func ReadDeletion(request *unstructured.Unstructured, backupExists bool) DeletionReading {
	if request == nil {
		if backupExists {
			return DeletionReading{State: DeletionUnknown}
		}
		return DeletionReading{State: DeletionDone}
	}
	phase, _, _ := unstructured.NestedString(request.Object, "status", "phase")
	errs, _, _ := unstructured.NestedStringSlice(request.Object, "status", "errors")
	switch {
	case phase == "Processed" && len(errs) > 0 && refusedUpFront(errs):
		return DeletionReading{State: DeletionRefused, Errors: errs}
	case phase == "Processed" && len(errs) > 0:
		return DeletionReading{State: DeletionFailed, Errors: errs}
	case phase == "Processed" && !backupExists:
		return DeletionReading{State: DeletionDone}
	case phase == "Processed":
		// Processed without errors while the backup remains: Velero is still
		// removing the Backup object, or removing it failed after the record.
		return DeletionReading{State: DeletionInProgress}
	case phase == "InProgress":
		return DeletionReading{State: DeletionInProgress}
	default:
		return DeletionReading{State: DeletionWaiting}
	}
}

// The errors Velero's deletion controller records when it refuses a request
// before touching the backup (Velero v1.18.2
// pkg/controller/backup_deletion_controller.go). Each is its only error.
var upFrontRefusals = []string{
	"spec.backupName is required",
	"backup is still in progress",
	"backup not found",
	"backup storage location ",
	"cannot delete backup because backup storage location ",
}

func refusedUpFront(errs []string) bool {
	if len(errs) != 1 {
		return false
	}
	for _, prefix := range upFrontRefusals {
		if strings.HasPrefix(errs[0], prefix) {
			return true
		}
	}
	return false
}
