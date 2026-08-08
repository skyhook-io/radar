package issues

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Velero reports every outcome through status.phase and has no status.conditions
// on any of its CRDs, so the generic False-condition fallback in
// detectGenericCRDIssues structurally cannot see a failed backup. This adapter
// hangs off the same seam (same list, same RBAC gating, same namespace filter)
// and translates phases into Issues.
//
// Issues attribute to the object's own namespace — velero, or kommander on NKP.
// That is admin-visible but invisible to a user scoped to the namespace whose
// data was lost; surfacing a failure against the *protected* namespaces is the
// coverage work's job, not this adapter's.
const VeleroGroup = "velero.io"

const (
	ReasonVeleroBackupFailed             = "BackupFailed"
	ReasonVeleroBackupPartiallyFailed    = "BackupPartiallyFailed"
	ReasonVeleroBackupValidationFailed   = "BackupValidationFailed"
	ReasonVeleroRestoreFailed            = "RestoreFailed"
	ReasonVeleroRestorePartiallyFailed   = "RestorePartiallyFailed"
	ReasonVeleroRestoreValidationFailed  = "RestoreValidationFailed"
	ReasonVeleroScheduleValidationFailed = "ScheduleValidationFailed"
	ReasonVeleroBSLUnavailable           = "BackupStorageLocationUnavailable"
	ReasonVeleroRepositoryNotReady       = "BackupRepositoryNotReady"
)

// veleroScheduleLabel is what the schedule controller stamps on every backup it
// creates. Generated names also carry a `<schedule>-<timestamp>` convention, but
// the label is the authoritative link — a manually-created backup can be named
// anything.
const veleroScheduleLabel = "velero.io/schedule-name"

// Phases in which a Backup or Restore has reached a verdict. Anything else
// (New, Queued, ReadyToStart, InProgress, WaitingForPluginOperations,
// Finalizing, Deleting) is still in flight and neither raises nor clears an
// issue — an in-progress run is not yet evidence of recovery.
var veleroDecidedPhases = map[string]bool{
	"Completed":                 true,
	"Failed":                    true,
	"FailedValidation":          true,
	"PartiallyFailed":           true,
	"FinalizingPartiallyFailed": true,
	"WaitingForPluginOperationsPartiallyFailed": true,
}

var veleroPartialPhases = map[string]bool{
	"PartiallyFailed":                           true,
	"FinalizingPartiallyFailed":                 true,
	"WaitingForPluginOperationsPartiallyFailed": true,
}

// detectVeleroIssues turns one GVR's worth of Velero objects into Issues. It
// takes the whole list rather than one object at a time because supersession is
// a property of the series, not of a single backup.
func detectVeleroIssues(gvr schema.GroupVersionResource, kind string, items []*unstructured.Unstructured, ownedSubjects map[string]bool) []Issue {
	visible := make([]*unstructured.Unstructured, 0, len(items))
	for _, u := range items {
		if ownedSubjects[resourceKey(gvr.Group, kind, u.GetNamespace(), u.GetName())] {
			continue
		}
		visible = append(visible, u)
	}
	if len(visible) == 0 {
		return nil
	}

	switch kind {
	case "Backup":
		return veleroBackupIssues(gvr, kind, visible)
	case "Restore":
		return veleroRunIssues(gvr, kind, visible,
			ReasonVeleroRestoreFailed, ReasonVeleroRestorePartiallyFailed, ReasonVeleroRestoreValidationFailed)
	case "Schedule":
		return veleroScheduleIssues(gvr, kind, visible)
	case "BackupStorageLocation":
		return veleroBSLIssues(gvr, kind, visible)
	case "BackupRepository":
		return veleroRepositoryIssues(gvr, kind, visible)
	}
	return nil
}

// veleroBackupIssues applies supersession before emitting. Velero keeps failed
// Backup objects until their TTL expires, so without this a single bad night
// stays red on the Problems surface for days after the schedule recovered.
//
// Backups are grouped by the schedule that created them; a backup with no
// schedule label is its own group keyed on its own name, because an ad-hoc
// backup is a one-off run and nothing later supersedes it. Within a group only
// the newest *decided* run is considered: if it succeeded, the group is clear;
// otherwise that one run drives the issue and the older failures stay quiet.
func veleroBackupIssues(gvr schema.GroupVersionResource, kind string, items []*unstructured.Unstructured) []Issue {
	groups := map[string][]*unstructured.Unstructured{}
	var order []string
	for _, u := range items {
		key := u.GetNamespace() + "\x00" + veleroBackupSeriesKey(u)
		if _, seen := groups[key]; !seen {
			order = append(order, key)
		}
		groups[key] = append(groups[key], u)
	}

	var out []Issue
	for _, key := range order {
		newest := newestDecidedVeleroRun(groups[key])
		if newest == nil {
			continue
		}
		if iss, ok := veleroRunIssue(gvr, kind, newest,
			ReasonVeleroBackupFailed, ReasonVeleroBackupPartiallyFailed, ReasonVeleroBackupValidationFailed); ok {
			out = append(out, iss)
		}
	}
	return out
}

// veleroBackupSeriesKey identifies the recurring series a backup belongs to.
func veleroBackupSeriesKey(u *unstructured.Unstructured) string {
	if schedule := u.GetLabels()[veleroScheduleLabel]; schedule != "" {
		return "schedule/" + schedule
	}
	return "backup/" + u.GetName()
}

// newestDecidedVeleroRun returns the most recent run that reached a verdict, or
// nil when every run in the group is still in flight. Ordering is by
// creationTimestamp (see veleroRunTime for why not start time); the name breaks
// ties so the result is stable across polls, since creationTimestamp has
// second resolution and Velero's generated names can collide on the second.
func newestDecidedVeleroRun(group []*unstructured.Unstructured) *unstructured.Unstructured {
	decided := make([]*unstructured.Unstructured, 0, len(group))
	for _, u := range group {
		if veleroDecidedPhases[veleroPhase(u)] {
			decided = append(decided, u)
		}
	}
	if len(decided) == 0 {
		return nil
	}
	sort.SliceStable(decided, func(i, j int) bool {
		ti, tj := veleroRunTime(decided[i]), veleroRunTime(decided[j])
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return decided[i].GetName() > decided[j].GetName()
	})
	return decided[0]
}

// veleroRunIssues emits one issue per failed object with no supersession —
// correct for Restores, which are deliberate one-off operator actions rather
// than a recurring series that a later run can vindicate.
func veleroRunIssues(gvr schema.GroupVersionResource, kind string, items []*unstructured.Unstructured, failedReason, partialReason, validationReason string) []Issue {
	var out []Issue
	for _, u := range items {
		if iss, ok := veleroRunIssue(gvr, kind, u, failedReason, partialReason, validationReason); ok {
			out = append(out, iss)
		}
	}
	return out
}

func veleroRunIssue(gvr schema.GroupVersionResource, kind string, u *unstructured.Unstructured, failedReason, partialReason, validationReason string) (Issue, bool) {
	phase := veleroPhase(u)
	validationErrors := veleroStringSlice(u, "status", "validationErrors")

	var severity Severity
	var reason, message string
	switch {
	case phase == "FailedValidation":
		severity, reason = SeverityCritical, validationReason
		message = veleroFailureMessage(u, "rejected in validation — nothing ran")
		if len(validationErrors) > 0 {
			message = strings.Join(validationErrors, "; ")
		}
	case phase == "Failed":
		severity, reason = SeverityCritical, failedReason
		message = veleroFailureMessage(u, "phase Failed")
	case veleroPartialPhases[phase]:
		// Partial failure is a warning, not critical: some of the data made it.
		// It must still surface — "partially failed" is where silent data loss
		// hides, because the run looks like it worked.
		severity, reason = SeverityWarning, partialReason
		message = veleroFailureMessage(u, "phase "+phase+" — some items were not processed")
	default:
		return Issue{}, false
	}

	if errCount := veleroInt(u, "status", "errors"); errCount > 0 {
		noun := " errors)"
		if errCount == 1 {
			noun = " error)"
		}
		message = message + " (" + strconv.FormatInt(errCount, 10) + noun
	}
	return newConditionIssue(gvr, kind, u.GetNamespace(), u.GetName(), severity, reason, message,
		veleroRunSince(u), "", u.GetCreationTimestamp().Time), true
}

// veleroScheduleIssues reports schedules Velero refused to accept. A paused
// schedule is deliberately NOT an issue: pausing is operator intent, and the
// engine drops info-tier rows anyway (see the Severity contract in types.go) —
// the Schedule detail view and list badge already say "Paused".
func veleroScheduleIssues(gvr schema.GroupVersionResource, kind string, items []*unstructured.Unstructured) []Issue {
	var out []Issue
	for _, u := range items {
		// A paused schedule is not producing backups by intent, so its
		// validation errors — which the controller leaves in place rather than
		// re-evaluating while paused — describe a hypothetical future run, not
		// a live failure. Raising critical here would contradict the rule above
		// and light up the queue for a deliberate state.
		if paused, ok, _ := unstructured.NestedBool(u.Object, "spec", "paused"); ok && paused {
			continue
		}
		validationErrors := veleroStringSlice(u, "status", "validationErrors")
		// Velero leaves the phase empty on some validation failures and records
		// only the errors, so the array is checked independently of the phase.
		if veleroPhase(u) != "FailedValidation" && len(validationErrors) == 0 {
			continue
		}
		message := "Schedule failed validation — no backups are being created"
		if len(validationErrors) > 0 {
			message = strings.Join(validationErrors, "; ")
		}
		out = append(out, veleroStateIssue(gvr, kind, u.GetNamespace(), u.GetName(),
			SeverityCritical, ReasonVeleroScheduleValidationFailed, message, u.GetCreationTimestamp().Time))
	}
	return out
}

func veleroBSLIssues(gvr schema.GroupVersionResource, kind string, items []*unstructured.Unstructured) []Issue {
	var out []Issue
	for _, u := range items {
		if veleroPhase(u) != "Unavailable" {
			continue
		}
		message := veleroFailureMessage(u, "backup storage location is Unavailable — Velero cannot read or write backups here")
		out = append(out, veleroStateIssue(gvr, kind, u.GetNamespace(), u.GetName(),
			SeverityCritical, ReasonVeleroBSLUnavailable, message, u.GetCreationTimestamp().Time))
	}
	return out
}

func veleroRepositoryIssues(gvr schema.GroupVersionResource, kind string, items []*unstructured.Unstructured) []Issue {
	var out []Issue
	for _, u := range items {
		if veleroPhase(u) != "NotReady" {
			continue
		}
		message := veleroFailureMessage(u, "backup repository is NotReady — filesystem and data-mover backups to it will fail")
		out = append(out, veleroStateIssue(gvr, kind, u.GetNamespace(), u.GetName(),
			SeverityWarning, ReasonVeleroRepositoryNotReady, message, u.GetCreationTimestamp().Time))
	}
	return out
}

func veleroPhase(u *unstructured.Unstructured) string {
	phase, _, _ := unstructured.NestedString(u.Object, "status", "phase")
	return phase
}

// veleroFailureMessage prefers whatever the controller wrote, falling back to a
// description of the phase so the issue is never empty.
func veleroFailureMessage(u *unstructured.Unstructured, fallback string) string {
	for _, field := range []string{"failureReason", "message"} {
		if msg, ok, _ := unstructured.NestedString(u.Object, "status", field); ok && msg != "" {
			return msg
		}
	}
	return fallback
}

// veleroRunSince is how long ago a Backup or Restore reached its verdict.
// Velero has no lastTransitionTime, so the completion time is the closest thing
// to "when this became true". A FailedValidation run never starts and so has
// neither timestamp, but it is created and rejected in the same instant, which
// makes creationTimestamp an accurate anchor for that case specifically.
func veleroRunSince(u *unstructured.Unstructured) time.Duration {
	for _, field := range []string{"completionTimestamp", "startTimestamp"} {
		if ts, ok, _ := unstructured.NestedString(u.Object, "status", field); ok && ts != "" {
			if t, err := time.Parse(time.RFC3339, ts); err == nil {
				return time.Since(t)
			}
		}
	}
	if created := u.GetCreationTimestamp().Time; !created.IsZero() {
		return time.Since(created)
	}
	return 0
}

// veleroStateIssue builds an issue for the kinds that describe standing state
// rather than a run — Schedule, BackupStorageLocation, BackupRepository. None of
// them records when its phase last changed (their status carries only phase,
// message and last-check times), so there is no honest age to report.
//
// FirstSeen is therefore cleared rather than left at compose time. Anchoring on
// creationTimestamp would claim a BSL that ran healthy for six months and broke
// two minutes ago had been broken since the day it was created; anchoring at
// compose time is worse in practice, because issues are recomposed every poll,
// so the age would re-derive from "now" on every read and sit at 0s forever —
// telling an operator asking "how long has this been broken" that it is always
// a brand-new problem. Both are fabrications. `first_seen` is `omitzero` on the
// wire, so a zero value simply omits the field (same treatment
// source_karpenter.go gives an issue whose start it cannot establish).
//
// LastSeen stays at now: that one is true — it is when we observed the state.
func veleroStateIssue(gvr schema.GroupVersionResource, kind, namespace, name string, severity Severity, reason, message string, createdAt time.Time) Issue {
	iss := newConditionIssue(gvr, kind, namespace, name, severity, reason, message, 0, "", createdAt)
	iss.FirstSeen = time.Time{}
	iss.LastSeen = time.Now()
	return iss
}

// veleroRunTime orders runs within a series.
//
// Deliberately creationTimestamp only, never startTimestamp. A run rejected in
// validation never starts and so carries no start time, while a run that
// succeeded does — ordering one against the other compares two different
// lifecycle events, and the comparison can invert. That is not hypothetical:
// the schedule controller does not block a new backup while a prior one sits
// Queued, and the backup controller stamps startTimestamp only after validation
// passes, so an older run can start (and succeed) after a newer run was created
// and rejected. Ordering on start time there lets the success bury the newer
// real failure — the exact silent-failure class this adapter exists to remove.
//
// creationTimestamp is on every object, means the same thing for all of them,
// and for a schedule series is monotonic with run order.
func veleroRunTime(u *unstructured.Unstructured) time.Time {
	return u.GetCreationTimestamp().Time
}

func veleroStringSlice(u *unstructured.Unstructured, fields ...string) []string {
	vals, found, err := unstructured.NestedStringSlice(u.Object, fields...)
	if !found || err != nil {
		return nil
	}
	return vals
}

// veleroInt reads an integer status field, accepting both the int64 shape a
// typed decode produces and the float64 shape a plain JSON decode can leave on
// a dynamic-informer object. NestedInt64 alone rejects the float64 case and
// would silently drop the error count from the message — the same pitfall
// internal/k8s and internal/mcp each guard against locally. A fractional value
// is not a valid count and reads as absent.
func veleroInt(u *unstructured.Unstructured, fields ...string) int64 {
	val, found, err := unstructured.NestedFieldNoCopy(u.Object, fields...)
	if !found || err != nil {
		return 0
	}
	switch n := val.(type) {
	case int64:
		return n
	case int32:
		return int64(n)
	case int:
		return int64(n)
	case float64:
		if n == float64(int64(n)) {
			return int64(n)
		}
	}
	return 0
}
