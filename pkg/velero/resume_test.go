package velero

import (
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func serverWithArgs(args ...string) ServerConfig {
	return ServerConfigFromDeployments([]*appsv1.Deployment{veleroDeployment(nil, args)}, true)
}

func TestServerSkipImmediatelyFlag(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want *bool
	}{
		{name: "absent is Velero's default, false", want: new(false)},
		{name: "bare flag", args: []string{"--schedule-skip-immediately"}, want: new(true)},
		{name: "explicit false", args: []string{"--schedule-skip-immediately=false"}, want: new(false)},
		{name: "explicit true", args: []string{"--schedule-skip-immediately=true"}, want: new(true)},
		// A boolean flag takes no separate value: "false" is a stray argument.
		{name: "a separate token is not its value", args: []string{"--schedule-skip-immediately", "false"}, want: new(true)},
		{name: "unparseable value is unknown", args: []string{"--schedule-skip-immediately=maybe"}},
		{name: "built from a variable is unknown", args: []string{"--schedule-skip-immediately=$(SKIP)"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := serverWithArgs(c.args...).ScheduleSkipImmediately
			if (got == nil) != (c.want == nil) || (got != nil && *got != *c.want) {
				t.Fatalf("ScheduleSkipImmediately = %v, want %v", deref(got), deref(c.want))
			}
		})
	}
	if got := ServerConfigFromDeployments(nil, false).ScheduleSkipImmediately; got != nil {
		t.Fatalf("an unread server must leave the flag unknown, got %v", *got)
	}
}

func deref(b *bool) any {
	if b == nil {
		return nil
	}
	return *b
}

func TestDescribeResume(t *testing.T) {
	paused := func(opts ...schedOpt) *unstructured.Unstructured {
		return schedule("nightly", "0 * * * *", append([]schedOpt{lastBackup(at("11:00")), specField("paused", true)}, opts...)...)
	}
	skipOn := serverWithArgs("--schedule-skip-immediately")
	flagUnread := utcServer
	flagUnread.ScheduleSkipImmediately = nil
	inProgress := []*unstructured.Unstructured{backup("nightly-20261115110000", "nightly", "InProgress", at("11:00"))}

	cases := []struct {
		name    string
		s       *unstructured.Unstructured
		backups []*unstructured.Unstructured
		unread  bool
		server  ServerConfig
		now     time.Time
		skip    *bool
		outcome ResumeOutcome
		reason  string
		due     bool
	}{
		{name: "a due run is submitted at once", s: paused(), now: at("12:10"), outcome: ResumeRunsNow, due: true},
		{name: "a stored skip is cleared by resume, as the CLI does", s: paused(specField("skipImmediately", true)), now: at("12:10"), outcome: ResumeRunsNow, due: true},
		// The skip is consumed before the hold check, so it wins.
		{name: "skip beats a hold", s: paused(), backups: inProgress, now: at("12:10"), skip: new(true), outcome: ResumeSkips, due: true},
		{name: "held by a run still in progress", s: paused(), backups: inProgress, now: at("12:10"), skip: new(false), outcome: ResumeHeld, due: true},
		{name: "false is a choice to run, whatever the server says", s: paused(), server: skipOn, now: at("12:10"), skip: new(false), outcome: ResumeRunsNow, due: true},
		{name: "unset takes the server's flag, which Velero 1.18 applies once", s: paused(), server: skipOn, now: at("12:10"), outcome: ResumeMayRunNow, reason: ResumeReasonSkipOnce, due: true},
		{name: "unset with the flag unread may run", s: paused(), server: flagUnread, now: at("12:10"), outcome: ResumeMayRunNow, reason: ResumeReasonSkipUnknown, due: true},
		{name: "an explicit choice needs no flag", s: paused(), server: flagUnread, now: at("12:10"), skip: new(false), outcome: ResumeRunsNow, due: true},
		{name: "scheduling disabled runs nothing", s: paused(), server: serverWithArgs("--disable-controllers=schedule"), now: at("12:10"), outcome: ResumeNothing},
		{name: "nothing due waits, skip or not", s: paused(), now: at("11:30"), skip: new(true), outcome: ResumeWaits},
		{name: "unread backups may hide a hold", s: paused(), unread: true, now: at("12:10"), outcome: ResumeMayRunNow, reason: string(ReasonBackupsUnread), due: true},
		// A paused schedule is never validated; Velero validates it on resume.
		{name: "paused from creation is validated on resume", s: paused(phase("")), now: at("12:10"), outcome: ResumeRunsNow, due: true},
		{name: "an old rejection of a fixed cron no longer counts", s: paused(phase("FailedValidation")), now: at("12:10"), outcome: ResumeRunsNow, due: true},
		{name: "an invalid cron is rejected again", s: schedule("bad", "not a cron", specField("paused", true), phase("FailedValidation")), now: at("12:10"), outcome: ResumeWontRun, reason: ResumeReasonRejected},
		{name: "a name too long never runs", s: schedule(strings.Repeat("a", 64), "0 * * * *", specField("paused", true)), now: at("12:10"), outcome: ResumeWontRun, reason: string(ReasonNameTooLong)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			server := c.server
			if server.Source == "" {
				server = utcServer
			}
			got := DescribeResume(ScheduleInput{Schedule: c.s, Backups: c.backups, BackupsRead: !c.unread, Server: server, DefaultLocation: "default", Now: c.now}, c.skip)
			if got.Outcome != c.outcome || got.Reason != c.reason || got.Due != c.due || got.Summary == "" {
				t.Fatalf("got %+v, want outcome=%s reason=%q due=%v", got, c.outcome, c.reason, c.due)
			}
		})
	}

	t.Run("an unconfirmed zone is not a certain wait", func(t *testing.T) {
		// Due at 09:00 in UTC, but 09:00 in a zone east of UTC has passed.
		daily := schedule("daily", "0 9 * * *", specField("paused", true), created(at("00:00")))
		unread := ServerConfigFromDeployments(nil, false)
		in := ScheduleInput{Schedule: daily, BackupsRead: true, Server: unread, Now: at("08:00")}
		got := DescribeResume(in, nil)
		if got.Outcome != ResumeMayRunNow || !got.Due || strings.Contains(got.Summary, "No run is due") || !strings.Contains(got.Summary, "may be due") {
			t.Fatalf("default %+v, want an uncertain may-run", got)
		}
		if chosen := DescribeResume(in, new(false)); chosen.Outcome != ResumeMayRunNow || chosen.Reason != ResumeReasonTiming || !chosen.Due {
			t.Fatalf("an explicit run %+v, want the timing reason", chosen)
		}
		if skipping := DescribeResume(in, new(true)); skipping.Outcome != ResumeSkips || !skipping.Due {
			t.Fatalf("skipping %+v", skipping)
		}
	})

	t.Run("a schedule writing to a read-only location won't run once resumed", func(t *testing.T) {
		readOnly := location("default", func(o map[string]any) { o["spec"].(map[string]any)["accessMode"] = "ReadOnly" })
		in := ScheduleInput{Schedule: schedule("nightly", "0 * * * *", specField("paused", true)), BackupsRead: true, Server: utcServer, DefaultLocation: "default", Locations: []*unstructured.Unstructured{readOnly}, Now: at("12:10")}
		got := DescribeResume(in, nil)
		if got.Outcome != ResumeWontRun || got.Reason != string(ReasonLocationReadOnly) || !strings.Contains(got.Summary, "read-only") {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("an @every schedule waits whatever the zone", func(t *testing.T) {
		every := schedule("every", "@every 1h", specField("paused", true), lastBackup(at("11:00")))
		got := DescribeResume(ScheduleInput{Schedule: every, BackupsRead: true, Server: ServerConfigFromDeployments(nil, false), Now: at("11:30")}, nil)
		if got.Outcome != ResumeWaits || got.Due || !got.NextRunCertain || got.NextRun == nil || !got.NextRun.Equal(at("12:00")) {
			t.Fatalf("got %+v, want a certain wait until 12:00", got)
		}
	})

	t.Run("a cron with no future time runs repeatedly, and skipping does not help", func(t *testing.T) {
		never := schedule("never", "0 0 31 2 *", specField("paused", true))
		for _, skip := range []*bool{nil, new(false), new(true)} {
			got := DescribeResume(ScheduleInput{Schedule: never, BackupsRead: true, Server: utcServer, Now: at("12:10")}, skip)
			if got.Outcome != ResumeRunsRepeatedly || got.Reason != string(ReasonNoFutureTime) || got.Due || !strings.Contains(got.Summary, "repeatedly") {
				t.Fatalf("skip=%v: got %+v", deref(skip), got)
			}
		}
	})

	t.Run("the schedule is not changed", func(t *testing.T) {
		s := paused(phase(""))
		DescribeResume(ScheduleInput{Schedule: s, BackupsRead: true, Server: utcServer, Now: at("12:10")}, nil)
		if p, _, _ := unstructured.NestedBool(s.Object, "spec", "paused"); !p {
			t.Fatal("DescribeResume modified its input")
		}
	})

	t.Run("waiting names the next run", func(t *testing.T) {
		got := DescribeResume(ScheduleInput{Schedule: paused(), BackupsRead: true, Server: utcServer, Now: at("11:30")}, nil)
		if got.NextRun == nil || !got.NextRun.Equal(at("12:00")) || !got.NextRunCertain {
			t.Fatalf("got %+v, want the next run at 12:00", got)
		}
	})

	t.Run("a held run is named", func(t *testing.T) {
		got := DescribeResume(ScheduleInput{Schedule: paused(), Backups: inProgress, BackupsRead: true, Server: utcServer, Now: at("12:10")}, nil)
		if got.Blocker == nil || got.Blocker.Name != "nightly-20261115110000" || !strings.Contains(got.Summary, "nightly-20261115110000") {
			t.Fatalf("got %+v", got)
		}
	})
}
