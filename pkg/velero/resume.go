package velero

import (
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ResumeOutcome is what Velero's schedule controller does on its first pass
// over a Schedule after it is resumed.
type ResumeOutcome string

const (
	// ResumeNothing: the server runs without its schedule controller.
	ResumeNothing ResumeOutcome = "nothing"
	// ResumeWontRun: Velero rejects the schedule, or every run of it fails.
	ResumeWontRun ResumeOutcome = "wontRun"
	// ResumeWaits: no run is due; the next one comes at its scheduled time.
	ResumeWaits ResumeOutcome = "waits"
	// ResumeSkips: the run due now, if any, is skipped once.
	ResumeSkips ResumeOutcome = "skips"
	// ResumeHeld: a run is due, but one of the schedule's runs is still New or
	// InProgress, so nothing is submitted while it is.
	ResumeHeld ResumeOutcome = "held"
	// ResumeRunsNow: a run is due and Velero submits it at once.
	ResumeRunsNow ResumeOutcome = "runsNow"
	// ResumeMayRunNow: Velero may submit a run at once; Reason says what Radar
	// could not establish.
	ResumeMayRunNow ResumeOutcome = "mayRunNow"
	// ResumeRunsRepeatedly: the cron has no future time, so every pass finds
	// a run due. A skip only moves lastSkipped, after which the next run is
	// still due at once, so there is no skip to offer.
	ResumeRunsRepeatedly ResumeOutcome = "runsRepeatedly"
)

// Reasons for ResumeWontRun and ResumeMayRunNow.
const (
	ResumeReasonRejected = "rejected"
	// ResumeReasonSkipUnknown: the server's --schedule-skip-immediately could
	// not be read.
	ResumeReasonSkipUnknown = "skipUnknown"
	// ResumeReasonSkipOnce: the server sets --schedule-skip-immediately, but
	// Velero 1.16–1.18.3 apply it to only the first unset Schedule each server
	// process reconciles: the reconciler stores a pointer to its own default
	// and clears it when consuming the skip (schedule_controller.go Reconcile).
	ResumeReasonSkipOnce     = "skipServerOnce"
	ResumeReasonTiming       = "timingUncertain"
	ResumeReasonNotEvaluated = "notEvaluated"
)

// ResumeOutlook says what resuming a paused Schedule does.
type ResumeOutlook struct {
	Outcome ResumeOutcome `json:"outcome"`
	Reason  string        `json:"reason,omitempty"`
	// Due: a run is due now, or may be, so skipping it would change something.
	Due     bool   `json:"due"`
	Summary string `json:"summary"`
	// NextRun is the run Velero waits for, when nothing is due.
	NextRun        *time.Time `json:"nextRun,omitempty"`
	NextRunCertain bool       `json:"nextRunCertain,omitempty"`
	// Blocker is the run that holds a due run.
	Blocker *ScheduleRun `json:"blocker,omitempty"`
}

// DescribeResume says what resuming in.Schedule does when the resume writes
// skip into spec.skipImmediately (nil leaves it unset, as `velero schedule
// unpause` does without --skip-immediately). It follows the controller's
// order: an unset field takes the server's flag, and a skip is consumed (moving
// lastSkipped to now) before validation and before the check for a run still
// New or InProgress.
func DescribeResume(in ScheduleInput, skip *bool) ResumeOutlook {
	if in.Server.SchedulingDisabled {
		return ResumeOutlook{Outcome: ResumeNothing, Summary: "The Velero server runs with scheduling turned off, so resuming runs nothing."}
	}
	// The schedule as the controller sees it once resumed. Velero validates
	// it again on that pass with the same parser Radar uses, so a cron Radar
	// parses is enabled whatever the status says now (a paused schedule is
	// never validated, and its errors may predate an edit).
	resumed := in.Schedule.DeepCopy()
	unstructured.RemoveNestedField(resumed.Object, "spec", "paused")
	unstructured.RemoveNestedField(resumed.Object, "spec", "skipImmediately")
	cronExpr, _, _ := unstructured.NestedString(resumed.Object, "spec", "schedule")
	if _, err := parseCron(cronExpr, in.Server.dayMatch()); err == nil {
		_ = unstructured.SetNestedField(resumed.Object, "Enabled", "status", "phase")
		unstructured.RemoveNestedField(resumed.Object, "status", "validationErrors")
	}
	in.Schedule = resumed
	r := readSchedule(in)

	due, certain := false, true
	switch r.State {
	case StateRejected:
		return ResumeOutlook{Outcome: ResumeWontRun, Reason: ResumeReasonRejected, Summary: "Velero rejects this schedule, so resuming runs nothing until it is fixed."}
	case StateCannotRun:
		if r.Reason == ReasonLocationReadOnly {
			return ResumeOutlook{Outcome: ResumeWontRun, Reason: string(ReasonLocationReadOnly), Summary: "Its storage location " + r.Location.Name + " is read-only, so Velero rejects every backup this schedule submits."}
		}
		return ResumeOutlook{Outcome: ResumeWontRun, Reason: string(ReasonNameTooLong), Summary: "This schedule's name is longer than 63 characters, so every run Velero submits fails."}
	case StateAlwaysDue:
		// Before any skip: Velero would consume it, find no next time and
		// submit at once anyway, unless a run still new or in progress holds
		// it, as it holds any due run.
		out := ResumeOutlook{Outcome: ResumeRunsRepeatedly, Reason: string(ReasonNoFutureTime), Summary: "This schedule's cron expression has no future time, so once resumed Velero submits runs immediately and repeatedly until the cron is fixed. Skipping does not prevent it."}
		if b := blockerOf(runsOf(r.Name, in.Backups)); b != nil {
			out.Blocker = b
			out.Summary = "This schedule's cron expression has no future time. Velero submits nothing while " + b.Name + " is new or in progress; after that it submits runs immediately and repeatedly until the cron is fixed. Skipping does not prevent it."
		} else if !in.BackupsRead {
			out.Summary += " Radar could not read this schedule's backups, so one still in progress may hold it until it finishes."
		}
		return out
	case StateOnSchedule:
		if r.NextRunCertain {
			out := ResumeOutlook{Outcome: ResumeWaits, Summary: "No run is due, so Velero submits the next one at its scheduled time.", NextRun: r.NextRun, NextRunCertain: true}
			// A consumed skip moves the anchor to now. That changes nothing for
			// a cron expression with no time between the last run and now, but
			// an @every schedule then counts its interval from the resume.
			switch {
			case !fixedDelay(cronExpr):
			case skip != nil && *skip:
				out.NextRun, out.NextRunCertain = nextAfterSkip(in, resumed)
				out.Summary = "No run is due. Skipping restarts this schedule's interval, so Velero submits the next run one interval after it resumes."
			case skipMayApply(in, skip):
				out.NextRunCertain = false
				out.Summary = "No run is due. Velero submits the next one at its scheduled time, or one interval after resuming if the Velero server skips it (--schedule-skip-immediately)."
			}
			return out
		}
		// Due in the zone Radar assumed is not due in every zone the
		// controller might use.
		due, certain = true, false
	case StateDue, StateLate, StateProbablyLate, StateLateOrHeld, StateHeld:
		due = true
	default:
		// StatePossiblyLate, and a cron Velero accepted that Radar cannot
		// evaluate (StateUnknown, StateNotValidated).
		due, certain = true, false
	}
	timing := r.State == StatePossiblyLate || r.State == StateOnSchedule
	dueText := "A run is due."
	switch {
	case !certain && timing:
		dueText = "A run may be due: Radar is not certain which time zone the schedule runs in."
	case !certain:
		dueText = "Radar cannot evaluate this schedule's cron expression, so a run may be due."
	}

	out := ResumeOutlook{Due: due}
	if skip != nil && *skip {
		out.Outcome, out.Summary = ResumeSkips, "Velero skips the run due now, if any, and submits the next one at its scheduled time."
		out.NextRun, out.NextRunCertain = nextAfterSkip(in, resumed)
		return out
	}
	runs := runsOf(r.Name, in.Backups)
	if b := blockerOf(runs); b != nil {
		out.Outcome, out.Blocker = ResumeHeld, b
		out.Summary = dueText + " " + b.Name + " has not finished, so Velero submits nothing while it is new or in progress."
		return out
	}
	out.Outcome = ResumeMayRunNow
	switch {
	case skip == nil && in.Server.ScheduleSkipImmediately == nil:
		out.Reason = ResumeReasonSkipUnknown
		out.Summary = dueText + " Radar could not read whether the Velero server skips it (--schedule-skip-immediately), so Velero may submit it at once."
	case skip == nil && *in.Server.ScheduleSkipImmediately:
		out.Reason = ResumeReasonSkipOnce
		out.Summary = dueText + " The Velero server is set to skip it, but Velero 1.16 to 1.18.3 skip only the first such schedule after the server starts, so Velero may submit it at once."
	case !certain && timing:
		out.Reason = ResumeReasonTiming
		out.Summary = dueText + " If one is, Velero submits it at once."
	case !certain:
		out.Reason = ResumeReasonNotEvaluated
		out.Summary = "Radar cannot evaluate this schedule's cron expression, so Velero may submit a run as soon as it resumes."
	case !in.BackupsRead:
		out.Reason = string(ReasonBackupsUnread)
		out.Summary = "A run is due. Radar could not read this schedule's backups, so one still in progress may hold it; otherwise Velero submits it at once."
	default:
		out.Outcome, out.Summary = ResumeRunsNow, "A run is due, so Velero submits it as soon as the schedule resumes."
	}
	return out
}

// skipMayApply: resuming with skip leaves the skip to the server's flag, and
// that flag is set or could not be read.
func skipMayApply(in ScheduleInput, skip *bool) bool {
	if skip != nil {
		return *skip
	}
	return in.Server.ScheduleSkipImmediately == nil || *in.Server.ScheduleSkipImmediately
}

// nextAfterSkip is the next run once Velero has consumed a skip, which moves
// lastSkipped to now before it computes the next time.
func nextAfterSkip(in ScheduleInput, resumed *unstructured.Unstructured) (*time.Time, bool) {
	skipped := resumed.DeepCopy()
	_ = unstructured.SetNestedField(skipped.Object, in.Now.UTC().Format(time.RFC3339), "status", "lastSkipped")
	in.Schedule = skipped
	if after := readSchedule(in); after.State == StateOnSchedule {
		return after.NextRun, after.NextRunCertain
	}
	return nil, false
}

func fixedDelay(expr string) bool {
	// @every parses the same under both libraries, so the day-matching rule doesn't matter.
	parsed, err := parseCron(expr, DayMatchUnknown)
	return err == nil && parsed.fixedDelay
}
