package issues

import (
	"fmt"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	veleropkg "github.com/skyhook-io/radar/pkg/velero"
)

func unavailableLocation(name string) *unstructured.Unstructured {
	return veleroReadingObj("BackupStorageLocation", name, veleroNow.Add(-72*time.Hour), nil,
		map[string]any{"provider": "aws"}, map[string]any{"phase": "Unavailable", "message": "bucket not found"})
}

// rejected is a scheduled backup Velero failed in validation with these errors.
func rejected(schedule string, errors ...string) *unstructured.Unstructured {
	errs := make([]any, len(errors))
	for i, e := range errors {
		errs[i] = e
	}
	return veleroReadingObj("Backup", schedule+"-20261116010000", veleroNow.Add(-time.Hour), map[string]any{veleropkg.ScheduleNameLabel: schedule},
		map[string]any{"storageLocation": "primary"}, map[string]any{"phase": "FailedValidation", "validationErrors": errs})
}

func unavailable(location string) string {
	return fmt.Sprintf("backup can't be created because BackupStorageLocation %s is in Unavailable status.", location)
}

func detect(objs ...*unstructured.Unstructured) []Issue {
	byKind := map[string][]*unstructured.Unstructured{}
	for _, o := range objs {
		byKind[o.GetKind()] = append(byKind[o.GetKind()], o)
	}
	return DetectVelero(byKind)
}

// Every schedule writing to a dead bucket fails each night; that is one fault.
func TestVeleroRunsRejectedByAnUnavailableLocationAreItsIssue(t *testing.T) {
	objs := []*unstructured.Unstructured{unavailableLocation("primary")}
	for i := range 50 {
		objs = append(objs, rejected(fmt.Sprintf("nightly-%02d", i), unavailable("primary")))
	}
	got := detect(objs...)
	if len(got) != 1 || got[0].Reason != ReasonVeleroBSLUnavailable {
		t.Fatalf("got %v, want only the location's issue", reasonsOf(got))
	}
	if n := len(got[0].AffectedRefs); n != 50 {
		t.Errorf("affected %d runs, want 50", n)
	}
	if veleroFact(got[0], factVeleroRejectedRuns) == nil {
		t.Errorf("facts %+v, want the rejected runs named", got[0].DiagnosticContext)
	}
}

// Only Velero's own statement that the location was unavailable ties a run to
// it: another error, or the run naming another location, stands on its own.
func TestVeleroRunsWithACauseOfTheirOwnStayIssues(t *testing.T) {
	cases := map[string]*unstructured.Unstructured{
		"another error too":  rejected("mixed", unavailable("primary"), "invalid included resource type"),
		"another location":   rejected("elsewhere", unavailable("secondary")),
		"a different reason": rejected("readonly", "backup can't be created because backup storage location primary is currently in read-only mode"),
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			got := byReason(detect(unavailableLocation("primary"), run))
			if _, ok := got[ReasonVeleroBackupValidationFailed]; !ok {
				t.Fatalf("got %v, want the run raised on its own", got)
			}
			if loc := got[ReasonVeleroBSLUnavailable]; len(loc.AffectedRefs) != 0 {
				t.Errorf("location claimed %+v", loc.AffectedRefs)
			}
		})
	}
}

// Once the location recovers nothing covers the rejection, and it is raised
// until a later run supersedes it.
func TestVeleroRejectedRunReturnsWhenItsLocationRecovers(t *testing.T) {
	recovered := veleroLocation(time.Minute)
	recovered.SetName("primary")
	got := byReason(detect(recovered, rejected("nightly", unavailable("primary"))))
	if _, ok := got[ReasonVeleroBackupValidationFailed]; !ok {
		t.Fatalf("got %v, want the rejected run back on its own", got)
	}
}

func TestVeleroRestoreRejectedByAnUnavailableLocation(t *testing.T) {
	restore := veleroReadingObj("Restore", "restore-payments", veleroNow.Add(-time.Hour), nil,
		map[string]any{"backupName": "nightly-1"},
		map[string]any{"phase": "FailedValidation", "validationErrors": []any{"The BSL primary is unavailable, cannot retrieve the backup"}})
	got := detect(unavailableLocation("primary"), restore)
	if len(got) != 1 || len(got[0].AffectedRefs) != 1 || got[0].AffectedRefs[0].Kind != "Restore" {
		t.Fatalf("got %+v, want the restore under the location", got)
	}
}

// A caller who can't read the runs gets the location's issue without them:
// no names, no links, no count.
func TestVeleroRejectedRunsAreRedactedForACallerWhoCannotReadThem(t *testing.T) {
	got := detect(unavailableLocation("primary"), rejected("nightly", unavailable("primary")))
	shown := RedactIssueRelatedRefs(got[0], func(r Ref) bool { return r.Kind != "Backup" })
	if len(shown.AffectedRefs) != 0 || veleroFact(shown, factVeleroRejectedRuns) != nil {
		t.Fatalf("shown %+v / %+v, want the runs gone", shown.AffectedRefs, shown.DiagnosticContext)
	}
	if shown.Reason != ReasonVeleroBSLUnavailable {
		t.Errorf("the location's own issue must stay: %+v", shown)
	}
}
