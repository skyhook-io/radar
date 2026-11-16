package velero

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestValidLabelValueMatchesVelero(t *testing.T) {
	if got := ValidLabelValue("nightly-20261116"); got != "nightly-20261116" {
		t.Errorf("a short name changed: %q", got)
	}
	long := strings.Repeat("a", 70)
	got := ValidLabelValue(long)
	// Velero v1.18.2 label.GetValidName: 57 characters of the name and 6 of its SHA-256.
	if len(got) != 63 || !strings.HasPrefix(got, strings.Repeat("a", 57)) || got[57:] != "6bd5e5" {
		t.Errorf("ValidLabelValue(70×a) = %q", got)
	}
}

func TestDeleteBackupRequestFor(t *testing.T) {
	r := DeleteBackupRequestFor("velero", "nightly-1", "uid-1")
	if spec, _, _ := unstructured.NestedString(r.Object, "spec", "backupName"); spec != "nightly-1" {
		t.Errorf("spec.backupName = %q", spec)
	}
	if r.GetLabels()[BackupNameLabel] != "nightly-1" || r.GetLabels()[BackupUIDLabel] != "uid-1" {
		t.Errorf("labels %v: Velero cleans up a request by these after a successful deletion", r.GetLabels())
	}
	if r.GetName() != "" || r.GetGenerateName() != "nightly-1-" {
		t.Fatalf("expected a server-generated name: %v", r.Object)
	}

}

func obj(fields map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: fields}
}

func TestDeletionRefusal(t *testing.T) {
	backup := func(phase string) *unstructured.Unstructured {
		return obj(map[string]any{"spec": map[string]any{"storageLocation": "default"}, "status": map[string]any{"phase": phase}})
	}
	location := func(mode, phase string) *unstructured.Unstructured {
		l := obj(map[string]any{"spec": map[string]any{"accessMode": mode}, "status": map[string]any{"phase": phase}})
		l.SetName("default")
		return l
	}
	cases := []struct {
		name     string
		backup   *unstructured.Unstructured
		location *unstructured.Unstructured
		refused  string
	}{
		{"finished, available location", backup("Completed"), location("ReadWrite", "Available"), ""},
		{"failed runs can be deleted", backup("Failed"), location("ReadWrite", "Available"), ""},
		{"location assigned but not started", backup("New"), location("ReadWrite", "Available"), ""},
		{"no storage location yet", obj(map[string]any{"status": map[string]any{"phase": "New"}}), nil, "no storage location"},
		{"running", backup("InProgress"), location("ReadWrite", "Available"), "still running"},
		{"finalizing", backup("FinalizingPartiallyFailed"), nil, "still running"},
		{"waiting for plugins", backup("WaitingForPluginOperations"), nil, "still running"},
		{"read-only location", backup("Completed"), location("ReadOnly", "Available"), "read-only"},
		{"unavailable location", backup("Completed"), location("ReadWrite", "Unavailable"), "not available"},
		{"location never validated", backup("Completed"), location("ReadWrite", ""), "not available"},
		{"location unread: phase only", backup("Completed"), nil, ""},
	}
	for _, c := range cases {
		got := DeletionRefusal(c.backup, c.location)
		if (c.refused == "") != (got == "") || !strings.Contains(got, c.refused) {
			t.Errorf("%s: refusal %q, want one containing %q", c.name, got, c.refused)
		}
	}
}

func TestReadDeletion(t *testing.T) {
	request := func(phase string, errs ...any) *unstructured.Unstructured {
		status := map[string]any{"phase": phase}
		if len(errs) > 0 {
			status["errors"] = errs
		}
		return obj(map[string]any{"status": status})
	}
	cases := []struct {
		name    string
		request *unstructured.Unstructured
		exists  bool
		want    DeletionState
	}{
		{"not picked up", request(""), true, DeletionWaiting},
		{"new", request("New"), true, DeletionWaiting},
		{"deleting", request("InProgress"), true, DeletionInProgress},
		{"refused: read-only", request("Processed", "cannot delete backup because backup storage location default is currently in read-only mode"), true, DeletionRefused},
		{"refused: no location", request("Processed", "backup storage location nowhere not found"), true, DeletionRefused},
		{"refused: running", request("Processed", "backup is still in progress"), true, DeletionRefused},
		{"failed partway", request("Processed", "error deleting snapshot snap-1: throttled"), true, DeletionFailed},
		{"failed with several errors", request("Processed", "backup storage location x not found", "error deleting restore velero/r"), true, DeletionFailed},
		{"processed, object still going", request("Processed"), true, DeletionInProgress},
		{"processed and gone", request("Processed"), false, DeletionDone},
		{"request removed with the backup", nil, false, DeletionDone},
		{"request removed, backup remains", nil, true, DeletionUnknown},
	}
	for _, c := range cases {
		got := ReadDeletion(c.request, c.exists)
		if got.State != c.want {
			t.Errorf("%s: state %q, want %q", c.name, got.State, c.want)
		}
		if (c.want == DeletionFailed || c.want == DeletionRefused) && len(got.Errors) == 0 {
			t.Errorf("%s: errors %v", c.name, got.Errors)
		}
	}
}
