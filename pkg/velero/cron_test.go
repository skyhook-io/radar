package velero

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func serverWithImage(image string) ServerConfig {
	d := veleroDeployment(nil, nil)
	d.Spec.Template.Spec.Containers[0].Image = image
	return ServerConfigFromDeployments([]*appsv1.Deployment{d}, true)
}

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func TestImageVersion(t *testing.T) {
	cases := []struct {
		image string
		want  [3]int
		ok    bool
	}{
		{"velero/velero:v1.18.3", [3]int{1, 18, 3}, true},
		{"registry.example/velero:v1.17.0", [3]int{1, 17, 0}, true},
		{"registry.example:5000/velero/velero:v1.18.2", [3]int{1, 18, 2}, true},
		{"bitnami/velero:1.15.2-debian-12-r3", [3]int{1, 15, 2}, true},
		{"velero/velero:v1.18.3-rc.1", [3]int{1, 18, 3}, true},
		{"velero/velero:v1.18.4@sha256:0123abcd", [3]int{1, 18, 4}, true},
		{"velero/velero@sha256:0123abcd", [3]int{}, false},
		{"registry.example:5000/velero/velero", [3]int{}, false},
		{"velero/velero", [3]int{}, false},
		{"velero/velero:latest", [3]int{}, false},
		{"velero/velero:main", [3]int{}, false},
		{"velero/velero:v1.18", [3]int{}, false},
		{"velero/velero:v1.018.3", [3]int{}, false},
		{"", [3]int{}, false},
	}
	for _, c := range cases {
		got, ok := imageVersion(c.image)
		if ok != c.ok || got != c.want {
			t.Errorf("imageVersion(%q) = %v, %v; want %v, %v", c.image, got, ok, c.want, c.ok)
		}
	}
}

func TestServerDayMatchFollowsTheImageVersion(t *testing.T) {
	cases := []struct {
		image string
		want  DayMatch
	}{
		{"velero/velero:v1.16.0", DayMatchEither},
		{"velero/velero:v1.18.2", DayMatchEither},
		{"velero/velero:v1.18.3-rc.1", DayMatchBoth},
		{"velero/velero:v1.18.3", DayMatchBoth},
		{"velero/velero:v1.19.0", DayMatchBoth},
		{"velero/velero:v2.0.0", DayMatchBoth},
		{"velero/velero@sha256:0123abcd", DayMatchUnknown},
	}
	for _, c := range cases {
		if got := serverWithImage(c.image).dayMatch(); got != c.want {
			t.Errorf("%s: dayMatch %q, want %q", c.image, got, c.want)
		}
	}
	rolling := veleroDeployment(nil, nil)
	rolling.Spec.Template.Spec.Containers[0].Image = "velero/velero:v1.18.3"
	rolling.Status.UpdatedReplicas = 0
	if got := ServerConfigFromDeployments([]*appsv1.Deployment{rolling}, true).dayMatch(); got != DayMatchUnknown {
		t.Errorf("during a rollout the previous image may still run, got %q", got)
	}
	if got := (ServerConfig{Source: ServerUnread}).dayMatch(); got != DayMatchUnknown {
		t.Errorf("unread server: %q", got)
	}
}

// "0 0 1-7 * MON" restricts both day fields: Velero before 1.18.3 runs it on
// days 1–7 and on every Monday, 1.18.3 and later on the first Monday only.
// November 2, 2026 is the first Monday of the month; December 7 the next one.
func TestBothDayFieldsFollowTheServerVersion(t *testing.T) {
	firstMonday := schedule("monthly", "0 0 1-7 * MON", lastBackup(day(2026, 11, 2)))
	old := serverWithImage("velero/velero:v1.18.2")
	newer := serverWithImage("velero/velero:v1.18.3")
	unknown := serverWithImage("velero/velero@sha256:0123abcd")
	if unknown.TimeZone != "UTC" {
		t.Fatalf("the unknown-version server must still have a known zone, got %q", unknown.TimeZone)
	}

	t.Run("before 1.18.3 either field matching runs it", func(t *testing.T) {
		r := read(firstMonday, nil, old, now)
		// November 3–7 and Monday the 9th passed with no submission.
		if r.State != StateLate || !r.Attention || r.MissedRuns != 6 || r.DayMatch != DayMatchEither {
			t.Fatalf("got %s attention=%v missed=%d dayMatch=%q", r.State, r.Attention, r.MissedRuns, r.DayMatch)
		}
		if !r.NextRunCertain || !r.NextRun.Equal(day(2026, 11, 3)) {
			t.Fatalf("next run %v certain=%v", r.NextRun, r.NextRunCertain)
		}
	})

	t.Run("from 1.18.3 both fields must match", func(t *testing.T) {
		r := read(firstMonday, nil, newer, now)
		if r.State != StateOnSchedule || r.Attention || r.DayMatch != DayMatchBoth {
			t.Fatalf("got %s attention=%v dayMatch=%q", r.State, r.Attention, r.DayMatch)
		}
		if !r.NextRunCertain || !r.NextRun.Equal(day(2026, 12, 7)) {
			t.Fatalf("next run %v certain=%v", r.NextRun, r.NextRunCertain)
		}
	})

	t.Run("an unknown version is an estimate, not late", func(t *testing.T) {
		r := read(firstMonday, nil, unknown, now)
		if r.State != StatePossiblyLate || r.Reason != ReasonDayMatchUnconfirmed || r.Attention || r.DayMatch != DayMatchUnknown {
			t.Fatalf("got %s reason=%q attention=%v dayMatch=%q", r.State, r.Reason, r.Attention, r.DayMatch)
		}
		if r.NextRunCertain || !r.NextRun.Equal(day(2026, 11, 3)) {
			t.Fatalf("next run %v certain=%v, want the earlier rule's time as an estimate", r.NextRun, r.NextRunCertain)
		}
		if r.MissedRuns != 0 {
			t.Fatalf("an estimate counts no missed runs, got %d", r.MissedRuns)
		}
	})

	t.Run("an unknown version past both rules' times is probably late", func(t *testing.T) {
		// After October 5, the first Monday of October, both rules ran it by
		// November 2.
		r := read(schedule("monthly", "0 0 1-7 * MON", lastBackup(day(2026, 10, 5))), nil, unknown, now)
		if r.State != StateProbablyLate || r.Reason != ReasonDayMatchUnconfirmed || !r.Attention {
			t.Fatalf("got %s reason=%q attention=%v", r.State, r.Reason, r.Attention)
		}
	})

	t.Run("an unknown version before the estimated time is on schedule, uncertain", func(t *testing.T) {
		r := read(firstMonday, nil, unknown, day(2026, 11, 2).Add(time.Hour))
		if r.State != StateOnSchedule || r.NextRunCertain || r.DayMatch != DayMatchUnknown {
			t.Fatalf("got %s certain=%v dayMatch=%q", r.State, r.NextRunCertain, r.DayMatch)
		}
	})
}

// "0 0 31 2 MON": February has no 31st, so 1.18.3 and later find no time and
// treat the schedule as always due; earlier versions run it on February's
// Mondays.
func TestImpossibleDayWithAWeekdayFollowsTheServerVersion(t *testing.T) {
	s := func() *unstructured.Unstructured {
		return schedule("feb", "0 0 31 2 MON", lastBackup(day(2026, 11, 1)))
	}
	if r := read(s(), nil, serverWithImage("velero/velero:v1.18.3"), now); r.State != StateAlwaysDue || r.Reason != ReasonNoFutureTime || !r.Attention || r.NextRun != nil {
		t.Fatalf("1.18.3: got %s reason=%q attention=%v next=%v", r.State, r.Reason, r.Attention, r.NextRun)
	}
	if r := read(s(), nil, serverWithImage("velero/velero:v1.18.2"), now); r.State != StateOnSchedule || !r.NextRunCertain || !r.NextRun.Equal(day(2027, 2, 1)) {
		t.Fatalf("1.18.2: got %s certain=%v next=%v", r.State, r.NextRunCertain, r.NextRun)
	}
	unknown := serverWithImage("velero/velero@sha256:0123abcd")
	if r := read(s(), nil, unknown, now); r.State != StateOnSchedule || r.NextRunCertain || r.DayMatch != DayMatchUnknown {
		t.Fatalf("unknown: got %s certain=%v dayMatch=%q", r.State, r.NextRunCertain, r.DayMatch)
	}
	// Past February's Mondays the earlier rule would have run it, but 1.18.3
	// never would, so it is never probably late.
	past := read(schedule("feb", "0 0 31 2 MON", lastBackup(day(2026, 1, 31))), nil, unknown, now)
	if past.State != StatePossiblyLate || past.Reason != ReasonDayMatchUnconfirmed || past.Attention {
		t.Fatalf("unknown, past the earlier rule: got %s reason=%q attention=%v", past.State, past.Reason, past.Attention)
	}
}

func TestOneUnrestrictedDayFieldStaysCertain(t *testing.T) {
	unknown := serverWithImage("velero/velero@sha256:0123abcd")
	for _, cron := range []string{"0 0 1 * *", "0 0 * * MON", "0 0 ? * MON", "0 0 1-7 * ?", "@weekly", "@monthly", "0 2 * * *"} {
		r := read(schedule("s", cron, lastBackup(now.Add(-time.Hour))), nil, unknown, now)
		if r.DayMatch != "" || !r.NextRunCertain || r.State != StateOnSchedule {
			t.Errorf("%q: dayMatch=%q certain=%v state=%s", cron, r.DayMatch, r.NextRunCertain, r.State)
		}
	}
	// A stepped field is restricted: robfig/cron reads "*/2" as days 1, 3, …
	if r := read(schedule("s", "0 0 */2 * MON", lastBackup(now.Add(-time.Hour))), nil, unknown, now); r.DayMatch != DayMatchUnknown || r.NextRunCertain {
		t.Errorf("*/2 with a weekday: dayMatch=%q certain=%v", r.DayMatch, r.NextRunCertain)
	}
}

func TestZoneAndDayMatchUncertaintyCombine(t *testing.T) {
	// Neither the zone nor the version known: the day rule is the reason, and
	// the margin still includes the zone's.
	r := read(schedule("monthly", "0 0 1-7 * MON", lastBackup(day(2026, 11, 2))), nil, ServerConfig{Source: ServerUnread}, now)
	if r.State != StatePossiblyLate || r.Reason != ReasonDayMatchUnconfirmed || r.NextRunCertain || r.Zone.Source != ZoneUnknown {
		t.Fatalf("got %s reason=%q certain=%v zone=%s", r.State, r.Reason, r.NextRunCertain, r.Zone.Source)
	}
}
