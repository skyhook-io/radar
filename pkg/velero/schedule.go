package velero

import (
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Group is Velero's API group.
const Group = "velero.io"

// ScheduleNameLabel is the label Velero puts on every Backup a Schedule
// submits. Its value is the Schedule's name, unshortened
// (pkg/builder/backup_builder.go FromSchedule).
const ScheduleNameLabel = "velero.io/schedule-name"

// maxLabelValue is Kubernetes' label value limit. Velero writes the schedule
// name into a label without shortening it, so a longer name makes every
// submission fail.
const maxLabelValue = 63

const (
	// dueGrace is how long a due run may go unsubmitted before it counts: the
	// controller requeues schedules about once a minute.
	dueGrace = 5 * time.Minute
	// notValidatedAfter: the controller validates a Schedule as soon as it is
	// created; still unvalidated after this, the controller is not processing it.
	notValidatedAfter = 5 * time.Minute
	// unstartedHoldAfter: a backup the controller has not started (phase "" or
	// New) holds its schedule; past this the backup controller is not
	// processing it. A started backup that runs long is the stalled-run Issue's.
	unstartedHoldAfter = 10 * time.Minute
	// inferredZoneMargin: with the server's offset read from a backup name,
	// only a daylight-saving change (an hour) or a skipped local time can move
	// a run, so past this it is probably late.
	inferredZoneMargin = 2 * time.Hour
	// unknownZoneMargin is added to a cadence when nothing establishes the
	// zone: an offset of up to fourteen hours plus a skipped local time move a
	// run by at most about a day.
	unknownZoneMargin = 24 * time.Hour
	maxMissedRuns     = 100
	maxRecentRuns     = 10
)

// ScheduleState is a Schedule's state as its controller would act on it.
type ScheduleState string

const (
	StatePaused             ScheduleState = "paused"
	StateSchedulingDisabled ScheduleState = "schedulingDisabled"
	StateRejected           ScheduleState = "rejected"
	StateCannotRun          ScheduleState = "cannotRun"
	StateNotValidated       ScheduleState = "notValidated"
	StateUnknown            ScheduleState = "unknown"
	// StateAlwaysDue: the cron has no future time. Velero's getNextRunTime
	// then compares now against the zero time, so the schedule is always due
	// and Velero submits a new run whenever no run of it is New or InProgress.
	StateAlwaysDue    ScheduleState = "alwaysDue"
	StateSkipPending  ScheduleState = "skipPending"
	StateHeld         ScheduleState = "held"
	StateLate         ScheduleState = "late"
	StateProbablyLate ScheduleState = "probablyLate"
	StateLateOrHeld   ScheduleState = "lateOrHeld"
	StatePossiblyLate ScheduleState = "possiblyLate"
	StateDue          ScheduleState = "due"
	StateOnSchedule   ScheduleState = "onSchedule"
)

// Reason codes qualify a state.
const (
	ReasonNameTooLong       = "nameTooLong"
	ReasonUnparseable       = "unparseable"
	ReasonUnstartedBlocker  = "unstartedBlocker"
	ReasonZoneUnconfirmed   = "zoneUnconfirmed"
	ReasonBackupsUnread     = "backupsUnread"
	ReasonNoFutureTime      = "noFutureTime"
	ReasonValidationPending = "validationPending"
	// ReasonServerDown: the Velero server Deployment has no available pod,
	// which explains a late or unvalidated schedule.
	ReasonServerDown = "serverDown"
)

// RunsRead says whether a Schedule's backups were read.
type RunsRead string

const (
	// RunsReadFull: every Backup in the schedule's namespace was read.
	RunsReadFull RunsRead = "full"
	// RunsReadUnknown: the namespace's Backups were not read, so nothing is
	// known about this schedule's runs.
	RunsReadUnknown RunsRead = "unknown"
)

// Run is one Backup a Schedule submitted.
type Run struct {
	Name    string    `json:"name"`
	Phase   string    `json:"phase,omitempty"`
	Created time.Time `json:"created"`
	// RanAt is when the run started (startTimestamp), else when it was created.
	// Velero's backup sync recreates Backups from object storage with a fresh
	// creationTimestamp but their original startTimestamp, so creation is not
	// when a run happened.
	RanAt     time.Time  `json:"ranAt"`
	Started   *time.Time `json:"started,omitempty"`
	Completed *time.Time `json:"completed,omitempty"`
	Expires   *time.Time `json:"expires,omitempty"`
	Errors    int64      `json:"errors,omitempty"`
	Warnings  int64      `json:"warnings,omitempty"`
}

// ScheduleRuns is what the backups Velero still keeps say about a schedule.
// Velero deletes backups past their TTL, so "none" means none among those kept.
type ScheduleRuns struct {
	Read            RunsRead `json:"read"`
	Kept            int      `json:"kept"`
	Newest          *Run     `json:"newest,omitempty"`
	NewestCompleted *Run     `json:"newestCompleted,omitempty"`
	// Recent is filled only on request (the schedule activity endpoint).
	Recent []Run `json:"recent,omitempty"`
}

// ScheduleLocation is where a schedule's next backup will be written.
type ScheduleLocation struct {
	Name string `json:"name,omitempty"`
	// ViaDefault: the template names no location, so the installation's
	// default receives it; Name is that default when it could be resolved.
	ViaDefault bool `json:"viaDefault,omitempty"`
}

// ScheduleReading is one Schedule read against its controller's rules.
type ScheduleReading struct {
	Namespace        string        `json:"namespace"`
	Name             string        `json:"name"`
	Created          time.Time     `json:"created"`
	Cron             string        `json:"cron"`
	Zone             Zone          `json:"zone"`
	State            ScheduleState `json:"state"`
	Reason           string        `json:"reason,omitempty"`
	Attention        bool          `json:"attention"`
	Paused           bool          `json:"paused,omitempty"`
	ValidationErrors []string      `json:"validationErrors,omitempty"`
	LastSubmitted    *time.Time    `json:"lastSubmitted,omitempty"`
	LastSkipped      *time.Time    `json:"lastSkipped,omitempty"`
	NextRun          *time.Time    `json:"nextRun,omitempty"`
	// NextRunCertain: NextRun is evaluated in the zone the controller uses.
	NextRunCertain bool `json:"nextRunCertain"`
	// MissedRuns counts scheduled times that passed with no submission, when
	// the schedule is late. Velero does not catch up on them.
	MissedRuns int              `json:"missedRuns,omitempty"`
	Blocker    *Run             `json:"blocker,omitempty"`
	Runs       ScheduleRuns     `json:"runs"`
	Location   ScheduleLocation `json:"location"`
	// TTL is the template's spec.ttl as written, how long each run is kept;
	// always sent, "" when the server's --default-backup-ttl applies (see
	// DeclaredTTL). Read from the schedule, not from a run: backup sync
	// recreates a run's object with a new creation time, so expiry less
	// creation is not its TTL.
	TTL string `json:"ttl"`
	// IncludedNamespaces and ExcludedNamespaces are the template's namespace
	// selection as written, always sent: an empty or "*" include list is every
	// namespace, and entries may be glob patterns, as Velero matches them.
	IncludedNamespaces []string `json:"includedNamespaces"`
	ExcludedNamespaces []string `json:"excludedNamespaces"`
}

// DeclaredTTL is a spec.ttl as Velero applies it: "" when the server's
// --default-backup-ttl applies, which Velero's backup controller substitutes
// for an absent or zero duration ("0s" keeps nothing for no time; it means
// the default). Any other value is returned as written.
func DeclaredTTL(ttl string) string {
	if ttl == "" {
		return ""
	}
	if d, err := time.ParseDuration(ttl); err == nil && d == 0 {
		return ""
	}
	return ttl
}

// ScheduleInput is what ReadSchedule needs. Backups are the namespace's
// Backups; BackupsRead says they were all read.
type ScheduleInput struct {
	Schedule    *unstructured.Unstructured
	Backups     []*unstructured.Unstructured
	BackupsRead bool
	Server      ServerConfig
	// DefaultLocation is the installation's default storage location name, ""
	// when it could not be resolved.
	DefaultLocation string
	Now             time.Time
	WithRecent      bool
}

// ReadSchedule reads one Schedule. Backups not of this schedule are ignored,
// so callers may pass the whole namespace.
func ReadSchedule(in ScheduleInput) ScheduleReading {
	r := readSchedule(in)
	switch r.State {
	case StateLate, StateProbablyLate, StateLateOrHeld, StateNotValidated:
		if r.Attention && in.Server.Down() {
			r.Reason = ReasonServerDown
		}
	case StateDue:
		// Due needs no attention while the server runs; stopped, nothing submits it.
		if in.Server.Down() {
			r.Reason = ReasonServerDown
		}
	}
	return r
}

func readSchedule(in ScheduleInput) ScheduleReading {
	s := in.Schedule
	r := ScheduleReading{Namespace: s.GetNamespace(), Name: s.GetName(), Created: s.GetCreationTimestamp().UTC()}
	r.Cron, _, _ = unstructured.NestedString(s.Object, "spec", "schedule")
	r.Paused, _, _ = unstructured.NestedBool(s.Object, "spec", "paused")
	r.ValidationErrors, _, _ = unstructured.NestedStringSlice(s.Object, "status", "validationErrors")
	phase, _, _ := unstructured.NestedString(s.Object, "status", "phase")
	r.LastSubmitted = timeField(s, "status", "lastBackup")
	r.LastSkipped = timeField(s, "status", "lastSkipped")
	r.Location = scheduleLocation(s, in.DefaultLocation)
	ttl, _, _ := unstructured.NestedString(s.Object, "spec", "template", "ttl")
	r.TTL = DeclaredTTL(ttl)
	r.IncludedNamespaces, _, _ = unstructured.NestedStringSlice(s.Object, "spec", "template", "includedNamespaces")
	r.ExcludedNamespaces, _, _ = unstructured.NestedStringSlice(s.Object, "spec", "template", "excludedNamespaces")
	if r.IncludedNamespaces == nil {
		r.IncludedNamespaces = []string{}
	}
	if r.ExcludedNamespaces == nil {
		r.ExcludedNamespaces = []string{}
	}

	runs := runsOf(s.GetName(), in.Backups)
	r.Runs = summarizeRuns(runs, in.BackupsRead, in.WithRecent)

	parsed, perr := parseCron(r.Cron)
	r.Zone = scheduleZone(r.Cron, parsed, perr, in.Server, s.GetName(), r.LastSubmitted, runs)

	switch {
	case r.Paused:
		r.State = StatePaused
		return r
	case in.Server.SchedulingDisabled:
		r.State = StateSchedulingDisabled
		return r
	case phase == "FailedValidation" || len(r.ValidationErrors) > 0:
		// The Issues engine already raises this (ScheduleValidationFailed),
		// so it is not repeated as attention here.
		r.State = StateRejected
		return r
	case len(s.GetName()) > maxLabelValue:
		r.State, r.Reason, r.Attention = StateCannotRun, ReasonNameTooLong, true
		return r
	case phase != "Enabled":
		r.State, r.Reason = StateNotValidated, ReasonValidationPending
		r.Attention = in.Now.Sub(s.GetCreationTimestamp().Time) > notValidatedAfter
		return r
	case perr != nil:
		r.State, r.Reason = StateUnknown, ReasonUnparseable
		return r
	}
	if skip, _, _ := unstructured.NestedBool(s.Object, "spec", "skipImmediately"); skip {
		// The controller moves lastSkipped to now on its next pass, before
		// checking whether the schedule is due.
		r.State = StateSkipPending
		return r
	}

	anchor := s.GetCreationTimestamp().Time
	if r.LastSubmitted != nil {
		anchor = *r.LastSubmitted
	}
	if r.LastSkipped != nil && r.LastSkipped.After(anchor) {
		anchor = *r.LastSkipped
	}
	loc := zoneLocation(r.Zone, parsed)
	next := parsed.next(anchor, loc)
	if next.IsZero() {
		r.State, r.Reason, r.Attention = StateAlwaysDue, ReasonNoFutureTime, true
		return r
	}
	nextUTC := next.UTC()
	// An @every schedule's times do not depend on the zone.
	timingCertain := r.Zone.Certain() || parsed.fixedDelay
	r.NextRun, r.NextRunCertain = &nextUTC, timingCertain
	r.Blocker = blockerOf(runs)

	if !in.Now.After(next) {
		r.State = StateOnSchedule
		return r
	}
	if !timingCertain {
		switch {
		case in.Now.After(next.Add(uncertainMargin(r.Zone, parsed, anchor, loc))):
			if r.Blocker != nil {
				r.State = StateHeld
				r.Reason, r.Attention = heldReason(r.Blocker, in.Now)
				return r
			}
			if !in.BackupsRead {
				// A run Radar cannot see may hold it; the estimated next run
				// keeps its "~".
				r.State, r.Reason, r.Attention = StateLateOrHeld, ReasonBackupsUnread, true
				return r
			}
			r.State, r.Reason, r.Attention = StateProbablyLate, ReasonZoneUnconfirmed, true
		default:
			r.State, r.Reason = StatePossiblyLate, ReasonZoneUnconfirmed
		}
		return r
	}
	if r.Blocker != nil {
		r.State = StateHeld
		r.Reason, r.Attention = heldReason(r.Blocker, in.Now)
		return r
	}
	if !in.Now.After(next.Add(dueGrace)) {
		r.State = StateDue
		return r
	}
	if !in.BackupsRead {
		r.State, r.Reason, r.Attention = StateLateOrHeld, ReasonBackupsUnread, true
		return r
	}
	r.State, r.Attention = StateLate, true
	r.MissedRuns = missedRuns(parsed, next, loc, in.Now)
	return r
}

func uncertainMargin(z Zone, p parsedCron, anchor time.Time, loc *time.Location) time.Duration {
	if z.Source == ZoneInferred {
		return inferredZoneMargin + dueGrace
	}
	return p.cronGap(anchor, loc) + unknownZoneMargin
}

func heldReason(b *Run, now time.Time) (string, bool) {
	if (b.Phase == "" || b.Phase == "New") && now.Sub(b.Created) > unstartedHoldAfter {
		return ReasonUnstartedBlocker, true
	}
	return "", false
}

func missedRuns(p parsedCron, first time.Time, loc *time.Location, now time.Time) int {
	n, at := 0, first
	for n < maxMissedRuns && !at.IsZero() && now.After(at.Add(dueGrace)) {
		n++
		next := p.next(at, loc)
		if !next.After(at) {
			break
		}
		at = next
	}
	return n
}

// scheduleZone decides the zone a schedule's times are read in.
func scheduleZone(expr string, p parsedCron, perr error, server ServerConfig, name string, lastBackup *time.Time, runs []Run) Zone {
	if perr == nil && p.zoned {
		return Zone{Name: cronZoneName(expr), Source: ZoneFromCron}
	}
	if server.zone != nil {
		src := ZoneFromServer
		if server.TimeZone == "UTC" {
			src = ZoneServerDefault
		}
		return Zone{Name: server.TimeZone, Source: src}
	}
	if offset, ok := inferOffset(name, lastBackup, runs); ok {
		return Zone{Name: fixedZoneName(offset), Source: ZoneInferred}
	}
	return Zone{Name: "UTC", Source: ZoneUnknown}
}

func cronZoneName(expr string) string {
	head, _, _ := strings.Cut(strings.TrimSpace(expr), " ")
	_, zone, _ := strings.Cut(head, "=")
	return zone
}

func zoneLocation(z Zone, p parsedCron) *time.Location {
	if p.zoned {
		return nil
	}
	switch z.Source {
	case ZoneFromServer, ZoneServerDefault:
		if loc, err := time.LoadLocation(z.Name); err == nil {
			return loc
		}
	case ZoneInferred:
		if off, ok := parseFixedZone(z.Name); ok {
			return time.FixedZone(z.Name, int(off/time.Second))
		}
	}
	return time.UTC
}

// Only the controller's submission matches lastBackup. CLI and on-demand runs
// use UTC names even when the server uses a different local zone.
func inferOffset(name string, lastBackup *time.Time, runs []Run) (time.Duration, bool) {
	if lastBackup == nil {
		return 0, false
	}
	prefix := name + "-"
	for _, run := range runs {
		suffix, ok := strings.CutPrefix(run.Name, prefix)
		if !ok || len(suffix) != 14 {
			continue
		}
		local, err := time.Parse("20060102150405", suffix)
		if err != nil {
			continue
		}
		// The API stamps creation just after the controller records its now.
		// Rehydrated objects and later manual submissions cannot establish a zone.
		delay := run.Created.Sub(*lastBackup)
		if delay < 0 || delay > time.Minute {
			continue
		}
		offset := local.Sub(lastBackup.UTC().Truncate(time.Second))
		rounded := offset.Round(15 * time.Minute)
		if offset != rounded || rounded < -12*time.Hour || rounded > 14*time.Hour {
			continue
		}
		return rounded, true
	}
	return 0, false
}

func parseFixedZone(name string) (time.Duration, bool) {
	rest, ok := strings.CutPrefix(name, "UTC")
	if !ok || len(rest) != 6 {
		return 0, false
	}
	t, err := time.Parse("15:04", rest[1:])
	if err != nil {
		return 0, false
	}
	d := time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute
	if rest[0] == '-' {
		d = -d
	}
	return d, true
}

func scheduleLocation(s *unstructured.Unstructured, defaultLocation string) ScheduleLocation {
	name, _, _ := unstructured.NestedString(s.Object, "spec", "template", "storageLocation")
	if name != "" {
		return ScheduleLocation{Name: name}
	}
	return ScheduleLocation{Name: defaultLocation, ViaDefault: true}
}

// runsOf returns the schedule's backups, newest first.
func runsOf(schedule string, backups []*unstructured.Unstructured) []Run {
	var runs []Run
	for _, b := range backups {
		if b == nil || b.GetLabels()[ScheduleNameLabel] != schedule {
			continue
		}
		runs = append(runs, runFrom(b))
	}
	sort.Slice(runs, func(i, j int) bool {
		if !runs[i].RanAt.Equal(runs[j].RanAt) {
			return runs[i].RanAt.After(runs[j].RanAt)
		}
		return runs[i].Name > runs[j].Name
	})
	return runs
}

func runFrom(b *unstructured.Unstructured) Run {
	run := Run{Name: b.GetName(), Created: b.GetCreationTimestamp().UTC()}
	run.Phase, _, _ = unstructured.NestedString(b.Object, "status", "phase")
	run.Started = timeField(b, "status", "startTimestamp")
	run.RanAt = run.Created
	if run.Started != nil {
		run.RanAt = *run.Started
	}
	run.Completed = timeField(b, "status", "completionTimestamp")
	run.Expires = timeField(b, "status", "expiration")
	run.Errors, _, _ = unstructured.NestedInt64(b.Object, "status", "errors")
	run.Warnings, _, _ = unstructured.NestedInt64(b.Object, "status", "warnings")
	return run
}

func summarizeRuns(runs []Run, read, withRecent bool) ScheduleRuns {
	if !read {
		return ScheduleRuns{Read: RunsReadUnknown}
	}
	out := ScheduleRuns{Read: RunsReadFull, Kept: len(runs)}
	for i := range runs {
		if out.Newest == nil {
			out.Newest = &runs[i]
		}
		if out.NewestCompleted == nil && runs[i].Phase == "Completed" {
			out.NewestCompleted = &runs[i]
		}
	}
	if withRecent {
		out.Recent = runs[:min(len(runs), maxRecentRuns)]
		if out.Recent == nil {
			out.Recent = []Run{}
		}
	}
	return out
}

// blockerOf is the newest run the controller counts as still running: phase
// "", New or InProgress (schedule_controller.go checkIfBackupInNewOrProgress).
func blockerOf(runs []Run) *Run {
	for i := range runs {
		switch runs[i].Phase {
		case "", "New", "InProgress":
			return &runs[i]
		}
	}
	return nil
}

func timeField(u *unstructured.Unstructured, fields ...string) *time.Time {
	v, ok, _ := unstructured.NestedString(u.Object, fields...)
	if !ok || v == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return nil
	}
	t = t.UTC()
	return &t
}
