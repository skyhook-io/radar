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
		return "every year on 1 January at 00:00 UTC"
	case "@monthly":
		return "on day 1 of every month at 00:00 UTC"
	case "@weekly":
		return "every Sunday at 00:00 UTC"
	case "@daily", "@midnight":
		return "every day at 00:00 UTC"
	case "@hourly":
		return "every hour, on the hour"
	}
	if d, ok := strings.CutPrefix(spec, "@every "); ok {
		dur, err := time.ParseDuration(strings.TrimSpace(d))
		if err != nil {
			return "cron " + spec
		}
		return "every " + cnpgEveryDuration(dur) + ", counted from the operator's last check"
	}
	f := strings.Fields(spec)
	if len(f) == 5 {
		f = append(f, "*")
	}
	if len(f) != 6 {
		return ""
	}
	// Steps are worded only as "*/N" on a field N divides (the step restarts
	// each minute, hour or day, so */5 hours runs 20:00 then 00:00); others
	// become the values they select. Any other step shape is shown literally.
	periods := []int{60, 60, 24}
	for i, field := range f {
		if !strings.Contains(field, "/") {
			continue
		}
		n, ok := strings.CutPrefix(field, "*/")
		step, err := strconv.Atoi(n)
		if !ok || err != nil || step <= 0 {
			return "cron " + spec
		}
		switch {
		case i < 3 && periods[i]%step != 0:
			f[i] = cronStepValues(step, periods[i])
		case i == 4 || i == 5:
			return "cron " + spec
		}
	}
	sec, min, hour, dom, mon, dow := f[0], f[1], f[2], f[3], f[4], f[5]

	fixedTime := isCronNumber(sec) && isCronNumber(min) && isCronNumber(hour)
	var when string
	if isCronNumber(sec) && isCronNumber(min) && cronNumberList(hour) {
		when = "at " + cronTimesPhrase(hour, min, sec)
		fixedTime = true
	} else if fixedTime {
		h, _ := strconv.Atoi(hour)
		m, _ := strconv.Atoi(min)
		sc, _ := strconv.Atoi(sec)
		if sc == 0 {
			when = fmt.Sprintf("at %02d:%02d UTC", h, m)
		} else {
			when = fmt.Sprintf("at %02d:%02d:%02d UTC", h, m, sc)
		}
	} else {
		when = cronIntraDayPhrase(sec, min, hour)
	}

	domAny, dowAny := cronAny(dom), cronAny(dow)
	domPhrase := "on day " + cronListPhrase(dom, nil, nil) + " of the month"
	if step, ok := strings.CutPrefix(dom, "*/"); ok {
		domPhrase = "every " + step + " days of the month from day 1"
	}
	var days string
	switch {
	case domAny && dowAny:
		days = "every day"
	case domAny:
		days = "every " + cronListPhrase(dow, cnpgCronWeekdays, cnpgCronDowIdx)
	case dowAny:
		days = domPhrase
	case cronStarred(dom) || cronStarred(dow):
		// robfig/cron v1 marks a field with any item starting with * or ? (including
		// */n) as a wildcard, and a wildcard on either day field makes both match.
		days = domPhrase + ", when it is a " + cronListPhrase(dow, cnpgCronWeekdays, cnpgCronDowIdx)
	default:
		days = domPhrase + " or every " + cronListPhrase(dow, cnpgCronWeekdays, cnpgCronDowIdx)
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

// cronIntraDayPhrase words the second, minute and hour fields when they do
// not name one time of day: "every hour, on the hour", "every hour at :15",
// "every 6 hours at :30", "every 15 minutes".
func cronIntraDayPhrase(sec, min, hour string) string {
	if isCronNumber(sec) && isCronNumber(min) {
		m, _ := strconv.Atoi(min)
		sc, _ := strconv.Atoi(sec)
		at := fmt.Sprintf(" at :%02d", m)
		if sc != 0 {
			at = fmt.Sprintf(" at :%02d:%02d", m, sc)
		} else if m == 0 {
			at = ", on the hour"
		}
		switch {
		case cronAny(hour):
			return "every hour" + at
		case strings.HasPrefix(hour, "*/"):
			return "every " + hour[2:] + " hours" + at
		}
		return "every hour" + at + ", during " + cronFieldPhrase(hour, "hour", "hours")
	}
	phrase := cronFieldPhrase(min, "minute", "minutes")
	if sec != "0" {
		phrase += ", " + cronFieldPhrase(sec, "second", "seconds")
	}
	return phrase + cronHourSuffix(hour)
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

func cronStarred(s string) bool {
	for _, item := range strings.Split(s, ",") {
		if strings.HasPrefix(item, "*") || strings.HasPrefix(item, "?") {
			return true
		}
	}
	return false
}

// cronStepValues lists the values "*/step" selects in a field restarting every period.
func cronStepValues(step, period int) string {
	var vals []string
	for v := 0; v < period; v += step {
		vals = append(vals, strconv.Itoa(v))
	}
	return strings.Join(vals, ",")
}

// cronNumberList: a comma-separated list of two or more plain numbers.
func cronNumberList(s string) bool {
	items := strings.Split(s, ",")
	if len(items) < 2 {
		return false
	}
	for _, item := range items {
		if !isCronNumber(item) {
			return false
		}
	}
	return true
}

// cronTimesPhrase words the times of day a list of hours selects: "00:00, 05:00 and 10:00 UTC".
func cronTimesPhrase(hours, min, sec string) string {
	m, _ := strconv.Atoi(min)
	sc, _ := strconv.Atoi(sec)
	var times []string
	for _, h := range strings.Split(hours, ",") {
		hh, _ := strconv.Atoi(h)
		if sc == 0 {
			times = append(times, fmt.Sprintf("%02d:%02d", hh, m))
		} else {
			times = append(times, fmt.Sprintf("%02d:%02d:%02d", hh, m, sc))
		}
	}
	return strings.Join(times[:len(times)-1], ", ") + " and " + times[len(times)-1] + " UTC"
}

// cnpgEveryDuration is the interval robfig/cron v1 actually runs an "@every"
// schedule at: whole seconds, at least one.
func cnpgEveryDuration(d time.Duration) string {
	if d < time.Second {
		d = time.Second
	}
	d -= d % time.Second
	out := d.String()
	for _, suffix := range []string{"m0s", "h0m"} {
		if strings.HasSuffix(out, suffix) && len(out) > len(suffix) {
			out = strings.TrimSuffix(out, suffix[1:])
		}
	}
	return out
}

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
