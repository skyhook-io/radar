package issues

import (
	"fmt"
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
	ReasonVeleroRunStalled               = "VeleroRunStalled"
)

// veleroScheduleLabel is what the schedule controller stamps on every backup it
// creates. Generated names also carry a `<schedule>-<timestamp>` convention, but
// the label is the authoritative link — a manually-created backup can be named
// anything.
const veleroScheduleLabel = "velero.io/schedule-name"

// Phases in which a Backup or Restore has already produced an outcome — which
// is not the same as having stopped. The two *PartiallyFailed variants are in
// here because the partial failure is already a fact, but Velero is still
// finalizing or waiting on plugin operations in both, so calling them
// "finished" would be wrong. Supersession is the only thing that reads this,
// and an outcome is what it needs: a run that has already failed part of its
// work cannot be cleared by being unfinished. Anything else
// (New, Queued, ReadyToStart, InProgress, WaitingForPluginOperations,
// Finalizing, Deleting) is still in flight and neither raises nor clears an
// issue — an in-progress run is not yet evidence of recovery.
var veleroPhasesWithOutcome = map[string]bool{
	"Completed":                 true,
	"Failed":                    true,
	"FailedValidation":          true,
	"PartiallyFailed":           true,
	"FinalizingPartiallyFailed": true,
	"WaitingForPluginOperationsPartiallyFailed": true,
}

// Phases in which work is still moving — or is supposed to be. Velero advances
// these only while its controller is running, so past a point they stop meaning
// "in flight" and start meaning "nothing is advancing this".
//
// Deliberately excludes two groups.
//
// The *PartiallyFailed variants already carry an outcome (see
// veleroPhasesWithOutcome) and
// already raise an issue of their own. Counting them here too put two issues on
// one object that disagreed: one naming the outcome it already has, the other that
// nothing was advancing it.
//
// Deleting is excluded because nothing here can time it. The only timestamp a
// Backup carries is startTimestamp, which records when the ORIGINAL run started
// and is never rewritten — and Velero only deletes backups that already ran, so
// that value is old by definition. Measuring a just-started deletion against it
// announces "nothing is advancing this" about a deletion proceeding normally.
var veleroInFlightPhases = map[string]bool{
	"InProgress":                 true,
	"WaitingForPluginOperations": true,
	"Finalizing":                 true,
}

// The phases itemOperationTimeout actually governs. Velero's own description of
// the field is "the time used to wait for asynchronous BackupItemAction
// operations" — it bounds those operations, not the run.
//
// That distinction is the difference between evidence and a guess. Sitting in
// WaitingForPluginOperations past the timeout means Velero should have given up
// on the operation and moved on, so something is wrong. Sitting in InProgress
// past it means only that time has passed: Velero puts no limit on how long
// walking and writing items may take, and a large cluster legitimately runs for
// hours. Both are worth surfacing; only one may be described as past a limit.
var veleroOperationBudgetPhases = map[string]bool{
	"WaitingForPluginOperations":                true,
	"WaitingForPluginOperationsPartiallyFailed": true,
}

// The upstream built-in when a run sets no timeout of its own. It is NOT
// necessarily the budget in force: the controller takes
// `--default-item-operation-timeout`, and distributions set their own. So a run
// that does not carry the field is measured against this, and the message has
// to say where the number came from rather than assert it as what Velero
// allows here.
const veleroDefaultItemOperationTimeout = 4 * time.Hour

// veleroStalledSentence is the whole claim, worded for the phase.
//
// One function because two paths report this — a run that is only stalled, and
// one that has also partially failed — and wording it twice is how they drift.
//
// The split is Velero's own: itemOperationTimeout is "the time used to wait for
// asynchronous BackupItemAction operations", so it governs the phase spent
// waiting on them and nothing else. Past it there, Velero should have given up
// and moved on, which is a real breach. Past it in InProgress means only that
// time has passed — Velero puts no bound on walking and writing items, and a
// large cluster legitimately runs for hours. Both are worth surfacing; only one
// may be called a breach, and an operator who knows the field would catch the
// difference immediately.
func veleroStalledSentence(phase string, age, budget time.Duration, fromSpec bool) string {
	head := "phase " + phase + " for " + formatVeleroDuration(age)
	limit := formatVeleroDuration(budget)

	if veleroOperationBudgetPhases[phase] {
		if fromSpec {
			return head + " — past the " + limit + " this run allows for a single operation, " +
				"so nothing appears to be advancing it"
		}
		return head + " — past the " + limit + " Velero allows for a single operation unless an install " +
			"changes it, so nothing appears to be advancing it"
	}

	whose := "this run allows"
	if !fromSpec {
		whose = "Velero allows"
	}
	return head + ", and nothing appears to be advancing it. Velero sets no limit on this phase; " +
		"for scale, " + whose + " " + limit + " for a single operation."
}

// veleroStalledFor reports how long a run has been in flight once it is past
// its budget. The caller appends a clause to a message that already names a
// partial failure, so it stays to what is known — how long the run has been
// going — rather than restating the budget in a sentence that is long already.
func veleroStalledFor(u *unstructured.Unstructured) (time.Duration, bool) {
	started := veleroRunStart(u)
	if started.IsZero() {
		return 0, false
	}
	budget, _ := veleroInFlightBudget(u)
	age := time.Since(started)
	if !veleroPastBudget(age, budget) {
		return 0, false
	}
	return age, true
}

// veleroPastBudget is the comparison both stall paths make, in one place so
// they cannot disagree about the boundary.
//
// Past the budget, not at it: a run that has used exactly the time it was
// allowed has not yet outlived it. Taking the durations as arguments is what
// makes that boundary testable at all — derived from time.Since at the call
// sites, "exactly the budget" is not a value a test can construct.
func veleroPastBudget(age, budget time.Duration) bool {
	return age > budget
}

// veleroInFlightBudget returns the budget and whether it came from the object.
// The caller needs the second value: one is a fact about this run, the other is
// an assumption about the install.
func veleroInFlightBudget(u *unstructured.Unstructured) (time.Duration, bool) {
	if raw, ok, _ := unstructured.NestedString(u.Object, "spec", "itemOperationTimeout"); ok && raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d, true
		}
	}
	return veleroDefaultItemOperationTimeout, false
}

// The partial phases in which Velero is still working. Plain PartiallyFailed is
// terminal; these two are not, which is why only these can also be stalled.
var veleroInFlightPartialPhases = map[string]bool{
	"FinalizingPartiallyFailed":                 true,
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
		// A restore stops moving for the same reason a backup does, and it is the
		// more urgent of the two: someone is waiting on it to get their data back.
		// No supersession grouping here — restores are one-off runs, nothing
		// later supersedes one.
		out := veleroRunIssues(gvr, kind, visible,
			ReasonVeleroRestoreFailed, ReasonVeleroRestorePartiallyFailed, ReasonVeleroRestoreValidationFailed)
		return append(out, veleroStalledRunIssues(gvr, kind, visible)...)
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
	return append(out, veleroStalledRunIssues(gvr, kind, items)...)
}

// veleroStalledRunIssues reports runs that are still in flight long past the
// window Velero allows for one.
//
// Deliberately separate from the supersession grouping above, which considers
// only DECIDED runs: a backup that never reaches a verdict is invisible to that
// logic by design, and it is exactly the run worth reporting. Velero advances an
// in-flight phase only while its controller is running, so a backup that has sat
// in one for longer than its own itemOperationTimeout is not slow — nothing is
// moving it, and every screen showing it as "in progress" is describing work
// that stopped.
//
// The message says how long and what the budget was, and stops there. Naming the
// cause would mean asserting something about the controller, which this source
// cannot see.
func veleroStalledRunIssues(gvr schema.GroupVersionResource, kind string, items []*unstructured.Unstructured) []Issue {
	var out []Issue
	for _, u := range items {
		phase := veleroPhase(u)
		if !veleroInFlightPhases[phase] {
			continue
		}
		started := veleroRunStart(u)
		if started.IsZero() {
			continue
		}
		budget, fromSpec := veleroInFlightBudget(u)
		age := time.Since(started)
		if !veleroPastBudget(age, budget) {
			continue
		}
		// Say where the number came from. Unset means Velero's built-in applies —
		// which the controller flag `--default-item-operation-timeout` can change,
		// so stating it as "what Velero allows" would assert an install setting
		// this source cannot read.
		// "run", not "backup": this function serves Restores too, and a Restore
		// carries its own itemOperationTimeout.
		msg := veleroStalledSentence(phase, age, budget, fromSpec)
		// How long the CLAIM has been true, not how long the run has existed. The
		// run was not stalled when it started — it became stalled when it passed
		// its budget, and `since` is what the Issues page renders as the age of
		// the problem. Passing the run's age would date a one-hour problem to
		// five hours ago on a run with a four-hour budget.
		out = append(out, newConditionIssue(gvr, kind, u.GetNamespace(), u.GetName(),
			SeverityWarning, ReasonVeleroRunStalled, msg,
			started.Add(budget), true, "", u.GetCreationTimestamp().Time))
	}
	return out
}

// veleroRunStart is when the run began. Only startTimestamp is meaningful here:
// creationTimestamp would call a queued backup stalled the moment it is created,
// before Velero has had any chance to pick it up.
func veleroRunStart(u *unstructured.Unstructured) time.Time {
	if ts, ok, _ := unstructured.NestedString(u.Object, "status", "startTimestamp"); ok && ts != "" {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			return t
		}
	}
	return time.Time{}
}

// formatVeleroDuration renders a duration the way an operator reads one, rather
// than the way Go prints one (31h9m22.6s).
//
// It carries the next unit down whenever that unit is non-zero, because the
// stall message puts an elapsed time and a timeout side by side and truncating
// collapses the gap between them: a run 4h30m into Velero's 4h built-in reads
// "for 4h — past the 4h Velero allows", two identical numbers making a claim
// about the difference between them. That is the default configuration, not an
// edge case.
func formatVeleroDuration(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		return withUnit(int(d.Hours())/24, "d", int(d.Hours())%24, "h")
	case d >= time.Hour:
		return withUnit(int(d.Hours()), "h", int(d.Minutes())%60, "m")
	case d >= time.Minute:
		return withUnit(int(d.Minutes()), "m", int(d.Seconds())%60, "s")
	default:
		// Velero accepts a sub-minute itemOperationTimeout, and both numbers in
		// the stall message run through here — so without this a run 40s past a
		// 30s timeout reads "for 0m — past the 0m".
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}

func withUnit(major int, majorUnit string, minor int, minorUnit string) string {
	if minor == 0 {
		return fmt.Sprintf("%d%s", major, majorUnit)
	}
	return fmt.Sprintf("%d%s %d%s", major, majorUnit, minor, minorUnit)
}

// veleroBackupSeriesKey identifies the recurring series a backup belongs to.
func veleroBackupSeriesKey(u *unstructured.Unstructured) string {
	if schedule := u.GetLabels()[veleroScheduleLabel]; schedule != "" {
		return "schedule/" + schedule
	}
	return "backup/" + u.GetName()
}

// newestDecidedVeleroRun returns the most recent run that already has an outcome, or
// nil when every run in the group is still in flight. Ordering is by
// creationTimestamp (see veleroRunTime for why not start time); the name breaks
// ties so the result is stable across polls, since creationTimestamp has
// second resolution and Velero's generated names can collide on the second.
func newestDecidedVeleroRun(group []*unstructured.Unstructured) *unstructured.Unstructured {
	decided := make([]*unstructured.Unstructured, 0, len(group))
	for _, u := range group {
		if veleroPhasesWithOutcome[veleroPhase(u)] {
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
		detail := "phase " + phase + " — some items were not processed"
		// Two of these three phases are still executing: Velero is finalizing or
		// waiting on plugin operations while already carrying a partial failure.
		// One of those wedged forever is exactly what the stall check exists to
		// catch, and it was invisible — excluded from that check to avoid two
		// issues on one object. Said here instead, in the issue this object
		// already raises, so it is neither duplicated nor lost.
		if veleroInFlightPartialPhases[phase] {
			if over, stalled := veleroStalledFor(u); stalled {
				detail += ", and it has been in flight " + formatVeleroDuration(over) + " with nothing appearing to advance it"
			}
		}
		message = veleroFailureMessage(u, detail)
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
	transitionAt, transitionKnown := veleroRunTimestamp(u, phase)
	return newConditionIssue(gvr, kind, u.GetNamespace(), u.GetName(), severity, reason, message,
		transitionAt, transitionKnown, "", u.GetCreationTimestamp().Time), true
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
		// Checked independently of the phase, defensively rather than because
		// Velero is known to need it: in v1.18 the schedule controller writes
		// the phase and the errors in one assignment, so an object carrying
		// errors without FailedValidation is not a shape it produces. Older
		// versions may differ and the check costs nothing, but it should not be
		// read as documenting a behaviour anyone has observed.
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

// veleroRunTimestamp is when a Backup or Restore reached its verdict.
// Velero has no lastTransitionTime, so the completion time is the closest thing
// to "when this became true". A FailedValidation run never starts and so has
// neither timestamp, but it is created and rejected in the same instant, which
// makes creationTimestamp an accurate anchor for that case specifically.
func veleroRunTimestamp(u *unstructured.Unstructured, phase string) (time.Time, bool) {
	for _, field := range []string{"completionTimestamp", "startTimestamp"} {
		if ts, ok, _ := unstructured.NestedString(u.Object, "status", field); ok && ts != "" {
			if t, err := time.Parse(time.RFC3339, ts); err == nil {
				return t, true
			}
		}
	}
	if created := u.GetCreationTimestamp().Time; phase == "FailedValidation" && !created.IsZero() {
		return created, true
	}
	return time.Time{}, false
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
	iss := newConditionIssue(gvr, kind, namespace, name, severity, reason, message, time.Time{}, false, "", createdAt)
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
