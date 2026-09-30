package server

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/internal/issues"
)

const (
	cnpgSchedulePreviewRuns = 3
	cnpgScheduleMaxLen      = 256
)

// CNPGSchedulePreview explains a ScheduledBackup schedule and when the
// operator would run it. Runs are computed as the operator does: from
// status.lastCheckTime when it has one (so a schedule whose next time since
// that check has passed runs as soon as the operator sees it), otherwise from
// now. Times are UTC: the operator evaluates the schedule on its own clock,
// which is UTC unless its Pod sets TZ.
type CNPGSchedulePreview struct {
	Schedule    string   `json:"schedule"`
	Valid       bool     `json:"valid"`
	Error       string   `json:"error,omitempty"`
	Description string   `json:"description,omitempty"`
	NextRuns    []string `json:"nextRuns,omitempty"`
	// RunsImmediately: the first run is due already, so the operator creates
	// a backup as soon as it reconciles this schedule.
	RunsImmediately bool `json:"runsImmediately,omitempty"`
	// Basis is what the first run is counted from: lastCheckTime | now.
	Basis         string `json:"basis"`
	LastCheckTime string `json:"lastCheckTime,omitempty"`
	Suspended     bool   `json:"suspended,omitempty"`
}

func cnpgSchedulePreview(spec string, lastCheck *time.Time, suspended bool, now time.Time) CNPGSchedulePreview {
	p := CNPGSchedulePreview{Schedule: spec, Basis: "now", Suspended: suspended}
	if lastCheck != nil {
		p.Basis, p.LastCheckTime = "lastCheckTime", lastCheck.UTC().Format(time.RFC3339)
	}
	if strings.TrimSpace(spec) == "" {
		p.Error = "the schedule is empty"
		return p
	}
	if len(spec) > cnpgScheduleMaxLen {
		p.Error = fmt.Sprintf("the schedule is longer than %d characters", cnpgScheduleMaxLen)
		return p
	}
	sched, err := issues.ParseCNPGSchedule(spec)
	if err != nil {
		p.Error = err.Error()
		return p
	}
	now = now.UTC()
	from := now
	if lastCheck != nil {
		from = lastCheck.UTC()
	}
	next := sched.Next(from)
	if next.IsZero() || sched.Next(now).IsZero() {
		p.Error = "no time satisfies this schedule: the operator would never run it"
		return p
	}
	p.Valid = true
	p.Description = describeCNPGSchedule(spec)
	if !next.After(now) {
		p.RunsImmediately = !suspended
		next = now
		p.NextRuns = append(p.NextRuns, next.Format(time.RFC3339))
		next = sched.Next(now)
	}
	for len(p.NextRuns) < cnpgSchedulePreviewRuns && !next.IsZero() {
		p.NextRuns = append(p.NextRuns, next.Format(time.RFC3339))
		next = sched.Next(next)
	}
	return p
}

func cnpgScheduleLastCheck(sched *unstructured.Unstructured) *time.Time {
	raw, _, _ := unstructured.NestedString(sched.Object, "status", "lastCheckTime")
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil
	}
	return &t
}

// GET /api/cnpg/scheduledbackups/{ns}/{name}/schedule-preview?schedule=
// Pure computation over the schedule read as the caller; writes nothing.
func (s *Server) handleCNPGSchedulePreview(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	dyn, _ := s.getDynamicClientSnapshotForRequest(r)
	if dyn == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
		return
	}
	sched, err := dyn.Resource(cnpgScheduleGVR).Namespace(namespace).Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		s.writeCNPGActionError(w, err, "schedule-preview", namespace, name)
		return
	}
	spec := r.URL.Query().Get("schedule")
	suspended, _, _ := unstructured.NestedBool(sched.Object, "spec", "suspend")
	s.writeJSON(w, cnpgSchedulePreview(spec, cnpgScheduleLastCheck(sched), suspended, time.Now()))
}

var (
	cnpgCronMonths   = []string{"", "January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"}
	cnpgCronWeekdays = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}
	cnpgCronMonthIdx = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}
	cnpgCronDowIdx   = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}
)

// describeCNPGSchedule words an already-validated schedule. It is an aid next
// to the verbatim spec, so anything it cannot word plainly falls back to a
// field-by-field reading rather than a guess.
func describeCNPGSchedule(spec string) string {
	spec = strings.TrimSpace(spec)
	switch spec {
	case "@yearly", "@annually":
		return "every year on 1 January at 00:00:00 UTC"
	case "@monthly":
		return "on day 1 of every month at 00:00:00 UTC"
	case "@weekly":
		return "every Sunday at 00:00:00 UTC"
	case "@daily", "@midnight":
		return "every day at 00:00:00 UTC"
	case "@hourly":
		return "every hour at minute 00, second 00"
	}
	if d, ok := strings.CutPrefix(spec, "@every "); ok {
		return "every " + strings.TrimSpace(d) + ", counted from the operator's last check"
	}
	f := strings.Fields(spec)
	if len(f) == 5 {
		f = append(f, "*")
	}
	if len(f) != 6 {
		return ""
	}
	sec, min, hour, dom, mon, dow := f[0], f[1], f[2], f[3], f[4], f[5]

	fixedTime := isCronNumber(sec) && isCronNumber(min) && isCronNumber(hour)
	var when string
	if fixedTime {
		h, _ := strconv.Atoi(hour)
		m, _ := strconv.Atoi(min)
		sc, _ := strconv.Atoi(sec)
		when = fmt.Sprintf("at %02d:%02d:%02d UTC", h, m, sc)
	} else {
		when = cronSubHourPhrase(sec, min) + cronHourSuffix(hour)
	}

	domAny, dowAny := cronAny(dom), cronAny(dow)
	var days string
	switch {
	case domAny && dowAny:
		days = "every day"
	case domAny:
		days = "every " + cronListPhrase(dow, cnpgCronWeekdays, cnpgCronDowIdx)
	case dowAny:
		days = "on day " + cronListPhrase(dom, nil, nil) + " of the month"
	default:
		days = "on day " + cronListPhrase(dom, nil, nil) + " of the month or every " + cronListPhrase(dow, cnpgCronWeekdays, cnpgCronDowIdx)
	}
	if !cronAny(mon) {
		days += " in " + cronListPhrase(mon, cnpgCronMonths, cnpgCronMonthIdx)
	}
	if fixedTime {
		return days + " " + when
	}
	if domAny && dowAny && cronAny(mon) {
		return when
	}
	return days + ", " + when
}

// cronSubHourPhrase words the second and minute fields together.
func cronSubHourPhrase(sec, min string) string {
	if isCronNumber(sec) && isCronNumber(min) {
		m, _ := strconv.Atoi(min)
		sc, _ := strconv.Atoi(sec)
		return fmt.Sprintf("at %02d:%02d past the hour", m, sc)
	}
	return cronFieldPhrase(min, "minute", "minutes") + ", " + cronFieldPhrase(sec, "second", "seconds")
}

func cronHourSuffix(hour string) string {
	switch {
	case cronAny(hour):
		return ""
	case strings.HasPrefix(hour, "*/"):
		return ", every " + hour[2:] + " hours"
	}
	return ", during " + cronFieldPhrase(hour, "hour", "hours")
}

func isCronNumber(s string) bool {
	_, err := strconv.Atoi(s)
	return err == nil
}

func cronAny(s string) bool { return s == "*" || s == "?" }

// cronFieldPhrase words one time field: "every minute", "every 15 minutes",
// "minute 5", "hours 9 through 17".
func cronFieldPhrase(field, unit, units string) string {
	switch {
	case cronAny(field):
		return "every " + unit
	case strings.HasPrefix(field, "*/"):
		return "every " + field[2:] + " " + units
	case isCronNumber(field):
		return unit + " " + field
	}
	if rng, step, ok := strings.Cut(field, "/"); ok {
		return "every " + step + " " + units + " in " + units + " " + strings.ReplaceAll(rng, "-", " through ")
	}
	return units + " " + strings.ReplaceAll(strings.ReplaceAll(field, ",", ", "), "-", " through ")
}

// cronListPhrase words a day-of-month, month or weekday field, naming months
// and weekdays.
func cronListPhrase(field string, names []string, idx map[string]int) string {
	name := func(tok string) string {
		if names == nil {
			return tok
		}
		if n, err := strconv.Atoi(tok); err == nil && n >= 0 && n < len(names) && names[n] != "" {
			return names[n]
		}
		if n, ok := idx[strings.ToLower(tok)]; ok {
			return names[n]
		}
		return tok
	}
	if rng, step, ok := strings.Cut(field, "/"); ok {
		base := "every " + step
		if !cronAny(rng) {
			base += " from " + cronListPhrase(rng, names, idx)
		}
		return base
	}
	var parts []string
	for _, item := range strings.Split(field, ",") {
		if a, b, ok := strings.Cut(item, "-"); ok {
			parts = append(parts, name(a)+" through "+name(b))
		} else {
			parts = append(parts, name(item))
		}
	}
	if len(parts) > 1 {
		return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
	}
	return parts[0]
}
