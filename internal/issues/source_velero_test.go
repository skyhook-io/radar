package issues

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/issuesapi"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func veleroGVR(resource string) schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: VeleroGroup, Version: "v1", Resource: resource}
}

type veleroObj struct {
	name      string
	namespace string
	schedule  string
	phase     string
	// started is minutes before now; 0 means "no startTimestamp" — the shape a
	// FailedValidation run really has, since Velero only stamps a start time
	// after validation passes.
	startedMinsAgo int
	// createdMinsAgo defaults to 24h when unset. Set it explicitly whenever a
	// test depends on ordering: creation time and start time are different
	// lifecycle events and must never be compared against each other.
	createdMinsAgo int
	validation     []string
	message        string
	errors         int64
	paused         bool
	// itemOperationTimeout is the budget the backup declares. Empty means the
	// object carries none, which is the shape most real backups have.
	itemOperationTimeout string
}

func (o veleroObj) build(kind string) *unstructured.Unstructured {
	ns := o.namespace
	if ns == "" {
		ns = "velero"
	}
	created := 24 * 60
	if o.createdMinsAgo > 0 {
		created = o.createdMinsAgo
	}
	meta := map[string]any{
		"name":              o.name,
		"namespace":         ns,
		"creationTimestamp": metav1.NewTime(time.Now().Add(-time.Duration(created) * time.Minute)).UTC().Format(time.RFC3339),
	}
	if o.schedule != "" {
		meta["labels"] = map[string]any{veleroScheduleLabel: o.schedule}
	}
	status := map[string]any{}
	if o.phase != "" {
		status["phase"] = o.phase
	}
	if o.startedMinsAgo > 0 {
		start := time.Now().Add(-time.Duration(o.startedMinsAgo) * time.Minute).UTC().Format(time.RFC3339)
		status["startTimestamp"] = start
		status["completionTimestamp"] = start
	}
	if len(o.validation) > 0 {
		errs := make([]any, len(o.validation))
		for i, e := range o.validation {
			errs[i] = e
		}
		status["validationErrors"] = errs
	}
	if o.message != "" {
		status["failureReason"] = o.message
	}
	if o.errors > 0 {
		status["errors"] = o.errors
	}
	spec := map[string]any{}
	if o.paused {
		spec["paused"] = true
	}
	if o.itemOperationTimeout != "" {
		spec["itemOperationTimeout"] = o.itemOperationTimeout
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "velero.io/v1",
		"kind":       kind,
		"metadata":   meta,
		"spec":       spec,
		"status":     status,
	}}
}

func buildVelero(kind string, objs ...veleroObj) []*unstructured.Unstructured {
	out := make([]*unstructured.Unstructured, 0, len(objs))
	for _, o := range objs {
		out = append(out, o.build(kind))
	}
	return out
}

func detectBackups(objs ...veleroObj) []Issue {
	return detectVeleroIssues(veleroGVR("backups"), "Backup", buildVelero("Backup", objs...), nil)
}

func reasonsOf(issues []Issue) []string {
	out := make([]string, 0, len(issues))
	for _, i := range issues {
		out = append(out, i.Reason)
	}
	return out
}

func namesOf(issues []Issue) []string {
	out := make([]string, 0, len(issues))
	for _, i := range issues {
		out = append(out, i.Name)
	}
	return out
}

// Every terminal Backup phase Velero can report must produce an Issue with the
// right severity, and no phase may fall through to silence. FailedValidation is
// the one that used to render as a grey "Unknown" pill and produced nothing at
// all.
func TestVeleroBackupPhaseToIssue(t *testing.T) {
	cases := []struct {
		phase        string
		wantIssue    bool
		wantSeverity Severity
		wantReason   string
	}{
		{phase: "Failed", wantIssue: true, wantSeverity: SeverityCritical, wantReason: ReasonVeleroBackupFailed},
		{phase: "FailedValidation", wantIssue: true, wantSeverity: SeverityCritical, wantReason: ReasonVeleroBackupValidationFailed},
		{phase: "PartiallyFailed", wantIssue: true, wantSeverity: SeverityWarning, wantReason: ReasonVeleroBackupPartiallyFailed},
		{phase: "FinalizingPartiallyFailed", wantIssue: true, wantSeverity: SeverityWarning, wantReason: ReasonVeleroBackupPartiallyFailed},
		{phase: "WaitingForPluginOperationsPartiallyFailed", wantIssue: true, wantSeverity: SeverityWarning, wantReason: ReasonVeleroBackupPartiallyFailed},
		// Healthy and in-flight phases are silent.
		{phase: "Completed"},
		{phase: "New"},
		{phase: "Queued"},
		{phase: "ReadyToStart"},
		{phase: "InProgress"},
		{phase: "WaitingForPluginOperations"},
		{phase: "Finalizing"},
		{phase: "Deleting"},
		{phase: ""},
	}
	for _, tc := range cases {
		t.Run(tc.phase, func(t *testing.T) {
			got := detectBackups(veleroObj{name: "b1", phase: tc.phase, startedMinsAgo: 30})
			if !tc.wantIssue {
				if len(got) != 0 {
					t.Fatalf("phase %q must not raise an issue, got %v", tc.phase, reasonsOf(got))
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("phase %q: want 1 issue, got %d %v", tc.phase, len(got), reasonsOf(got))
			}
			if got[0].Reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", got[0].Reason, tc.wantReason)
			}
			if got[0].Severity != tc.wantSeverity {
				t.Errorf("severity = %q, want %q", got[0].Severity, tc.wantSeverity)
			}
			if got[0].Source != SourceCondition {
				t.Errorf("source = %q, want %q", got[0].Source, SourceCondition)
			}
			if got[0].Category != issuesapi.CategoryBackupFailed {
				t.Errorf("category = %q, want %q", got[0].Category, issuesapi.CategoryBackupFailed)
			}
		})
	}
}

// Supersession is the reason this adapter reads the whole list. Velero keeps
// failed Backups until their TTL expires, so without it one bad night stays red
// for days after the schedule recovered.
func TestVeleroBackupSupersession(t *testing.T) {
	t.Run("later success clears an earlier failure", func(t *testing.T) {
		got := detectBackups(
			veleroObj{name: "nightly-1", schedule: "nightly", phase: "Failed", createdMinsAgo: 240, startedMinsAgo: 240},
			veleroObj{name: "nightly-2", schedule: "nightly", phase: "Completed", createdMinsAgo: 60, startedMinsAgo: 60},
		)
		if len(got) != 0 {
			t.Fatalf("a later Completed run must supersede the failure, got %v", reasonsOf(got))
		}
	})

	t.Run("only the newest failure in a series is reported", func(t *testing.T) {
		got := detectBackups(
			veleroObj{name: "nightly-3", schedule: "nightly", phase: "Failed", createdMinsAgo: 60, startedMinsAgo: 60},
			veleroObj{name: "nightly-1", schedule: "nightly", phase: "Failed", createdMinsAgo: 240, startedMinsAgo: 240},
			veleroObj{name: "nightly-2", schedule: "nightly", phase: "Failed", createdMinsAgo: 120, startedMinsAgo: 120},
		)
		if len(got) != 1 || got[0].Name != "nightly-3" {
			t.Fatalf("want one issue on nightly-3, got %v", namesOf(got))
		}
	})

	t.Run("an earlier success does not clear a later failure", func(t *testing.T) {
		got := detectBackups(
			veleroObj{name: "nightly-2", schedule: "nightly", phase: "Failed", createdMinsAgo: 60, startedMinsAgo: 60},
			veleroObj{name: "nightly-1", schedule: "nightly", phase: "Completed", createdMinsAgo: 240, startedMinsAgo: 240},
		)
		if len(got) != 1 || got[0].Name != "nightly-2" {
			t.Fatalf("want the later failure reported, got %v", namesOf(got))
		}
	})

	t.Run("an in-flight run neither clears nor raises", func(t *testing.T) {
		// The running backup is newer but has no verdict yet — it is not
		// evidence of recovery, so the last decided run still drives the issue.
		got := detectBackups(
			veleroObj{name: "nightly-1", schedule: "nightly", phase: "Failed", createdMinsAgo: 120, startedMinsAgo: 120},
			veleroObj{name: "nightly-2", schedule: "nightly", phase: "InProgress", createdMinsAgo: 5, startedMinsAgo: 5},
		)
		if len(got) != 1 || got[0].Name != "nightly-1" {
			t.Fatalf("want the last decided run reported, got %v", namesOf(got))
		}
	})

	t.Run("a group with no decided run is silent", func(t *testing.T) {
		got := detectBackups(veleroObj{name: "nightly-1", schedule: "nightly", phase: "InProgress", startedMinsAgo: 5})
		if len(got) != 0 {
			t.Fatalf("an entirely in-flight series must be silent, got %v", reasonsOf(got))
		}
	})

	t.Run("supersession is per schedule", func(t *testing.T) {
		got := detectBackups(
			veleroObj{name: "nightly-1", schedule: "nightly", phase: "Failed", createdMinsAgo: 60, startedMinsAgo: 60},
			veleroObj{name: "weekly-1", schedule: "weekly", phase: "Completed", createdMinsAgo: 30, startedMinsAgo: 30},
		)
		if len(got) != 1 || got[0].Name != "nightly-1" {
			t.Fatalf("a different schedule's success must not clear nightly, got %v", namesOf(got))
		}
	})

	t.Run("ad-hoc backups are never superseded", func(t *testing.T) {
		// Unlabelled backups are one-off operator runs, not a series — a later
		// manual backup of something else says nothing about this one.
		got := detectBackups(
			veleroObj{name: "manual-a", phase: "Failed", createdMinsAgo: 120, startedMinsAgo: 120},
			veleroObj{name: "manual-b", phase: "Completed", createdMinsAgo: 60, startedMinsAgo: 60},
			veleroObj{name: "manual-c", phase: "Failed", createdMinsAgo: 30, startedMinsAgo: 30},
		)
		if len(got) != 2 {
			t.Fatalf("want both ad-hoc failures reported, got %v", namesOf(got))
		}
	})

	t.Run("supersession does not cross namespaces", func(t *testing.T) {
		// Two Velero installs (e.g. velero and kommander) can both run a
		// schedule called "nightly"; one succeeding says nothing about the other.
		got := detectBackups(
			veleroObj{name: "nightly-1", namespace: "velero", schedule: "nightly", phase: "Failed", createdMinsAgo: 60, startedMinsAgo: 60},
			veleroObj{name: "nightly-1", namespace: "kommander", schedule: "nightly", phase: "Completed", createdMinsAgo: 30, startedMinsAgo: 30},
		)
		if len(got) != 1 || got[0].Namespace != "velero" {
			t.Fatalf("want the velero-namespace failure reported, got %d issues", len(got))
		}
	})
}

// Ordering must be driven by creation time; the name is only a tie-break. Here
// the two disagree — the newest run sorts LAST alphabetically — so a
// name-ordered implementation would pick the stale success and hide the failure.
func TestVeleroSupersessionOrdersByTimeNotName(t *testing.T) {
	got := detectBackups(
		// Lexically greatest, but the OLDER run — and it succeeded.
		veleroObj{name: "nightly-zzz", schedule: "nightly", phase: "Completed", createdMinsAgo: 240, startedMinsAgo: 240},
		// Lexically smallest, but the NEWER run — and it failed.
		veleroObj{name: "nightly-aaa", schedule: "nightly", phase: "Failed", createdMinsAgo: 30, startedMinsAgo: 30},
	)
	if len(got) != 1 {
		t.Fatalf("the newer failure must win over the older success; got %d issues %v", len(got), namesOf(got))
	}
	if got[0].Name != "nightly-aaa" {
		t.Errorf("want nightly-aaa (newest by creation time), got %q — ordering fell back to the name", got[0].Name)
	}
}

// A FailedValidation run never starts, so it has no startTimestamp. Ordering a
// series must therefore not compare one run's start time against another's
// creation time — they are different lifecycle events, and mixing them can
// invert the order and let a success bury a newer real failure.
//
// The sequence below is reachable on v1.18: the schedule controller does not
// block a new backup while a prior one sits Queued, and the backup controller
// only stamps startTimestamp after validation passes. So the older run can
// start (and succeed) after the newer run was created and rejected.
func TestVeleroSupersessionNeverComparesStartAgainstCreation(t *testing.T) {
	got := detectBackups(
		// Created first, sat Queued, started late, succeeded.
		veleroObj{name: "nightly-a", schedule: "nightly", phase: "Completed", createdMinsAgo: 65, startedMinsAgo: 5},
		// Created after A, rejected in validation, never started.
		veleroObj{name: "nightly-b", schedule: "nightly", phase: "FailedValidation", createdMinsAgo: 10,
			validation: []string{"backup template is invalid"}},
	)
	if len(got) != 1 {
		t.Fatalf("the newer FailedValidation run must not be swallowed by the older success; got %d issues %v", len(got), namesOf(got))
	}
	if got[0].Name != "nightly-b" {
		t.Errorf("want nightly-b reported, got %q", got[0].Name)
	}
}

// Ordering must be stable when Velero's second-resolution generated names
// collide on the same startTimestamp, or the reported row would flip between
// polls and the issue identity would churn.
func TestVeleroBackupSupersessionTieBreak(t *testing.T) {
	first := detectBackups(
		veleroObj{name: "nightly-20260806010000", schedule: "nightly", phase: "Failed", startedMinsAgo: 60},
		veleroObj{name: "nightly-20260806010001", schedule: "nightly", phase: "Failed", startedMinsAgo: 60},
	)
	second := detectBackups(
		veleroObj{name: "nightly-20260806010001", schedule: "nightly", phase: "Failed", startedMinsAgo: 60},
		veleroObj{name: "nightly-20260806010000", schedule: "nightly", phase: "Failed", startedMinsAgo: 60},
	)
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("want one issue from each ordering, got %d and %d", len(first), len(second))
	}
	if first[0].Name != second[0].Name {
		t.Errorf("tie-break is order-dependent: %q vs %q", first[0].Name, second[0].Name)
	}
	if first[0].Name != "nightly-20260806010001" {
		t.Errorf("want the lexically-later name to win the tie, got %q", first[0].Name)
	}
}

// A FailedValidation backup carries its cause in status.validationErrors, not
// in failureReason — surfacing "phase Failed" there would hide the reason.
func TestVeleroValidationErrorsBecomeTheMessage(t *testing.T) {
	got := detectBackups(veleroObj{
		name:           "bad",
		phase:          "FailedValidation",
		startedMinsAgo: 10,
		validation:     []string{"Invalid included/excluded resource lists", "storage location not found"},
	})
	if len(got) != 1 {
		t.Fatalf("want 1 issue, got %d", len(got))
	}
	want := "Invalid included/excluded resource lists; storage location not found"
	if got[0].Message != want {
		t.Errorf("message = %q, want %q", got[0].Message, want)
	}
}

// The error-count suffix must survive both numeric shapes an unstructured object
// can carry. The veleroObj fixtures above store int64, so an int64-only reader
// passes them while silently dropping the count on any object whose numbers came
// through a plain JSON decode.
func TestVeleroErrorCountAcceptsBothNumericShapes(t *testing.T) {
	build := func(errors any, omit bool) []*unstructured.Unstructured {
		status := map[string]any{"phase": "Failed"}
		if !omit {
			status["errors"] = errors
		}
		return []*unstructured.Unstructured{{Object: map[string]any{
			"apiVersion": "velero.io/v1",
			"kind":       "Backup",
			"metadata":   map[string]any{"name": "b1", "namespace": "velero"},
			"status":     status,
		}}}
	}
	for _, tc := range []struct {
		name   string
		errors any
		omit   bool
		want   string
	}{
		{name: "int64 (typed decode)", errors: int64(3), want: "(3 errors)"},
		{name: "one error reads singular", errors: int64(1), want: "(1 error)"},
		{name: "float64 (plain JSON decode)", errors: float64(3), want: "(3 errors)"},
		{name: "int", errors: 3, want: "(3 errors)"},
		{name: "zero is omitted", errors: int64(0)},
		{name: "fractional is not a count", errors: 3.5},
		{name: "absent", omit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := detectVeleroIssues(veleroGVR("backups"), "Backup", build(tc.errors, tc.omit), nil)
			if len(got) != 1 {
				t.Fatalf("want 1 issue (the failure must surface regardless), got %d", len(got))
			}
			if tc.want == "" {
				if strings.Contains(got[0].Message, "errors)") {
					t.Errorf("message should carry no count, got %q", got[0].Message)
				}
				return
			}
			if !strings.Contains(got[0].Message, tc.want) {
				t.Errorf("message = %q, want it to contain %q", got[0].Message, tc.want)
			}
		})
	}
}

func TestVeleroRestoreIssues(t *testing.T) {
	detect := func(objs ...veleroObj) []Issue {
		return detectVeleroIssues(veleroGVR("restores"), "Restore", buildVelero("Restore", objs...), nil)
	}

	t.Run("failed and partially-failed restores both surface", func(t *testing.T) {
		got := detect(
			veleroObj{name: "r1", phase: "Failed", startedMinsAgo: 30},
			veleroObj{name: "r2", phase: "PartiallyFailed", startedMinsAgo: 20},
			veleroObj{name: "r3", phase: "FailedValidation", startedMinsAgo: 10},
			veleroObj{name: "r4", phase: "Completed", startedMinsAgo: 5},
		)
		if len(got) != 3 {
			t.Fatalf("want 3 issues, got %d %v", len(got), namesOf(got))
		}
	})

	t.Run("restores are not superseded", func(t *testing.T) {
		// A restore is a deliberate one-off action; a later restore of something
		// else does not vindicate an earlier failure the way a backup run does.
		got := detect(
			veleroObj{name: "r1", phase: "Failed", startedMinsAgo: 60},
			veleroObj{name: "r2", phase: "Completed", startedMinsAgo: 30},
		)
		if len(got) != 1 || got[0].Name != "r1" {
			t.Fatalf("want the failed restore still reported, got %v", namesOf(got))
		}
	})
}

func TestVeleroScheduleIssues(t *testing.T) {
	detect := func(objs ...veleroObj) []Issue {
		return detectVeleroIssues(veleroGVR("schedules"), "Schedule", buildVelero("Schedule", objs...), nil)
	}

	t.Run("FailedValidation phase raises critical", func(t *testing.T) {
		got := detect(veleroObj{name: "s1", phase: "FailedValidation"})
		if len(got) != 1 || got[0].Reason != ReasonVeleroScheduleValidationFailed || got[0].Severity != SeverityCritical {
			t.Fatalf("want one critical ScheduleValidationFailed, got %v", got)
		}
	})

	t.Run("validationErrors raise even with an empty phase", func(t *testing.T) {
		got := detect(veleroObj{name: "s1", validation: []string{"invalid cron"}})
		if len(got) != 1 {
			t.Fatalf("want 1 issue, got %d", len(got))
		}
		if got[0].Message != "invalid cron" {
			t.Errorf("message = %q, want the validation error", got[0].Message)
		}
	})

	t.Run("a paused schedule is operator intent, not an issue", func(t *testing.T) {
		got := detect(veleroObj{name: "s1", phase: "Enabled", paused: true})
		if len(got) != 0 {
			t.Fatalf("pausing is deliberate and must not raise an issue, got %v", reasonsOf(got))
		}
	})

	t.Run("a paused schedule with stale validation errors stays quiet", func(t *testing.T) {
		// The controller leaves validationErrors in place rather than
		// re-evaluating a paused schedule, so they describe a hypothetical
		// future run. Raising critical would contradict the paused rule; the
		// detail page still shows the errors so they can be fixed before
		// resuming.
		got := detect(veleroObj{name: "s1", phase: "FailedValidation", paused: true, validation: []string{"invalid cron"}})
		if len(got) != 0 {
			t.Fatalf("a paused schedule must not raise regardless of validation errors, got %v", reasonsOf(got))
		}
	})

	t.Run("an enabled schedule is silent", func(t *testing.T) {
		if got := detect(veleroObj{name: "s1", phase: "Enabled"}); len(got) != 0 {
			t.Fatalf("want no issue, got %v", reasonsOf(got))
		}
	})
}

func TestVeleroTargetIssues(t *testing.T) {
	t.Run("Unavailable BSL is critical and categorised as a target failure", func(t *testing.T) {
		got := detectVeleroIssues(veleroGVR("backupstoragelocations"), "BackupStorageLocation",
			buildVelero("BackupStorageLocation", veleroObj{name: "default", phase: "Unavailable", message: "bucket not found"}), nil)
		if len(got) != 1 {
			t.Fatalf("want 1 issue, got %d", len(got))
		}
		if got[0].Reason != ReasonVeleroBSLUnavailable || got[0].Severity != SeverityCritical {
			t.Errorf("got reason=%q severity=%q", got[0].Reason, got[0].Severity)
		}
		if got[0].Category != issuesapi.CategoryBackupTargetUnavailable {
			t.Errorf("category = %q, want %q", got[0].Category, issuesapi.CategoryBackupTargetUnavailable)
		}
		if got[0].Message != "bucket not found" {
			t.Errorf("message = %q, want the controller's failureReason", got[0].Message)
		}
	})

	t.Run("Available BSL is silent", func(t *testing.T) {
		got := detectVeleroIssues(veleroGVR("backupstoragelocations"), "BackupStorageLocation",
			buildVelero("BackupStorageLocation", veleroObj{name: "default", phase: "Available"}), nil)
		if len(got) != 0 {
			t.Fatalf("want no issue, got %v", reasonsOf(got))
		}
	})

	t.Run("NotReady BackupRepository is a warning", func(t *testing.T) {
		got := detectVeleroIssues(veleroGVR("backuprepositories"), "BackupRepository",
			buildVelero("BackupRepository", veleroObj{name: "repo-1", phase: "NotReady"}), nil)
		if len(got) != 1 || got[0].Severity != SeverityWarning || got[0].Reason != ReasonVeleroRepositoryNotReady {
			t.Fatalf("want one warning BackupRepositoryNotReady, got %v", got)
		}
	})

	t.Run("Ready BackupRepository is silent", func(t *testing.T) {
		got := detectVeleroIssues(veleroGVR("backuprepositories"), "BackupRepository",
			buildVelero("BackupRepository", veleroObj{name: "repo-1", phase: "Ready"}), nil)
		if len(got) != 0 {
			t.Fatalf("want no issue, got %v", reasonsOf(got))
		}
	})
}

// Kinds Radar watches but has no Velero detector for (VolumeSnapshotLocation
// has no populated status.phase; the data-mover kinds are v2 work) must not
// fabricate issues.
func TestVeleroUnhandledKindsAreSilent(t *testing.T) {
	for _, kind := range []string{"VolumeSnapshotLocation", "DataUpload", "PodVolumeBackup", "DeleteBackupRequest"} {
		got := detectVeleroIssues(veleroGVR("x"), kind, buildVelero(kind, veleroObj{name: "o1", phase: "Failed"}), nil)
		if len(got) != 0 {
			t.Errorf("kind %s must be silent, got %v", kind, reasonsOf(got))
		}
	}
}

// ownedSubjects is how the compose layer suppresses rows already reported by a
// higher-level detector; the Velero adapter must honour it before grouping, or
// a suppressed backup could still win supersession and re-report.
func TestVeleroRespectsOwnedSubjects(t *testing.T) {
	owned := map[string]bool{
		resourceKey(VeleroGroup, "Backup", "velero", "nightly-2"): true,
	}
	items := buildVelero("Backup",
		veleroObj{name: "nightly-1", schedule: "nightly", phase: "Failed", startedMinsAgo: 120},
		veleroObj{name: "nightly-2", schedule: "nightly", phase: "Failed", startedMinsAgo: 60},
	)
	got := detectVeleroIssues(veleroGVR("backups"), "Backup", items, owned)
	if len(got) != 1 || got[0].Name != "nightly-1" {
		t.Fatalf("want the non-owned backup reported, got %v", namesOf(got))
	}
}

// FirstSeen must anchor on when the run actually failed, not on compose time —
// otherwise a week-old failure looks brand new on every poll and jumps the queue.
func TestVeleroIssueAnchorsOnRunTime(t *testing.T) {
	got := detectBackups(veleroObj{name: "b1", phase: "Failed", startedMinsAgo: 180})
	if len(got) != 1 {
		t.Fatalf("want 1 issue, got %d", len(got))
	}
	age := time.Since(got[0].FirstSeen)
	if age < 170*time.Minute || age > 190*time.Minute {
		t.Errorf("FirstSeen age = %v, want ~180m (the completion time)", age)
	}
}

func TestVeleroFailedRunWithoutRunTimestampHasUnknownOnset(t *testing.T) {
	got := detectBackups(veleroObj{name: "b1", phase: "Failed"})
	if len(got) != 1 || !got[0].OnsetUnknown || !got[0].FirstSeen.IsZero() || got[0].ResourceCreatedAt.IsZero() {
		t.Fatalf("timestamp-less failed run fabricated onset: %+v", got)
	}
}

// Schedule, BackupStorageLocation and BackupRepository record no transition
// timestamp at all (their status carries only phase, message and last-check
// times), so there is no honest age to report and FirstSeen must be omitted.
//
// Both alternatives are fabrications. creationTimestamp would claim a BSL that
// broke two minutes ago had been broken since it was created. Compose time is
// worse: issues are recomposed on every poll, so the age re-derives from "now"
// each read and sits at 0s permanently — an operator asking "how long has this
// been broken" is told it is always brand new. `first_seen` is `omitzero`, so a
// zero value drops the field from the wire entirely.
func TestVeleroStateKindsOmitFirstSeen(t *testing.T) {
	cases := []struct {
		kind, resource string
		obj            veleroObj
	}{
		{"BackupStorageLocation", "backupstoragelocations", veleroObj{name: "bsl", phase: "Unavailable"}},
		{"BackupRepository", "backuprepositories", veleroObj{name: "repo", phase: "NotReady"}},
		{"Schedule", "schedules", veleroObj{name: "sched", phase: "FailedValidation"}},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			got := detectVeleroIssues(veleroGVR(tc.resource), tc.kind, buildVelero(tc.kind, tc.obj), nil)
			if len(got) != 1 {
				t.Fatalf("want 1 issue, got %d", len(got))
			}
			if !got[0].FirstSeen.IsZero() {
				t.Errorf("FirstSeen = %v, want zero so the field is omitted — neither the object's age nor compose time is how long this has been broken", got[0].FirstSeen)
			}
			// LastSeen is the one timestamp we can stand behind: when we looked.
			if age := time.Since(got[0].LastSeen); age > time.Minute {
				t.Errorf("LastSeen age = %v, want ~now (the observation time)", age)
			}
			if got[0].IssueTiming != "" {
				t.Errorf("issue_timing = %q, want empty: there is no transition timestamp to derive it from", got[0].IssueTiming)
			}
		})
	}
}

// The zero FirstSeen must actually disappear from the wire, not serialize as a
// zero date — that is the whole point of clearing it.
func TestVeleroStateKindsOmitFirstSeenOnTheWire(t *testing.T) {
	got := detectVeleroIssues(veleroGVR("backupstoragelocations"), "BackupStorageLocation",
		buildVelero("BackupStorageLocation", veleroObj{name: "bsl", phase: "Unavailable"}), nil)
	if len(got) != 1 {
		t.Fatalf("want 1 issue, got %d", len(got))
	}
	blob, err := json.Marshal(got[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(blob), "first_seen") {
		t.Errorf("first_seen must be omitted from the wire, got %s", blob)
	}
	if !strings.Contains(string(blob), "last_seen") {
		t.Errorf("last_seen must still be present, got %s", blob)
	}
}

// The issue identity must not change between polls for an unchanged failure.
func TestVeleroIssueIdentityIsStable(t *testing.T) {
	obj := veleroObj{name: "nightly-1", schedule: "nightly", phase: "Failed", startedMinsAgo: 60}
	first := detectBackups(obj)
	second := detectBackups(obj)
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("want 1 issue each, got %d and %d", len(first), len(second))
	}
	if first[0].ID == "" {
		t.Fatal("issue ID must be populated by enrichIdentity")
	}
	if first[0].ID != second[0].ID {
		t.Errorf("issue ID is unstable: %q vs %q", first[0].ID, second[0].ID)
	}
}
