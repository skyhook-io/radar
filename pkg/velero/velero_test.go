package velero

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var now = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

func at(clock string) time.Time {
	t, err := time.Parse("15:04", clock)
	if err != nil {
		panic(err)
	}
	return time.Date(2026, 11, 15, t.Hour(), t.Minute(), 0, 0, time.UTC)
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

type schedOpt func(map[string]any)

func schedule(name, cron string, opts ...schedOpt) *unstructured.Unstructured {
	obj := map[string]any{
		"apiVersion": "velero.io/v1", "kind": "Schedule",
		"metadata": map[string]any{"name": name, "namespace": "velero", "creationTimestamp": ts(now.Add(-30 * 24 * time.Hour))},
		"spec":     map[string]any{"schedule": cron, "template": map[string]any{}},
		"status":   map[string]any{"phase": "Enabled"},
	}
	for _, o := range opts {
		o(obj)
	}
	return &unstructured.Unstructured{Object: obj}
}

func lastBackup(t time.Time) schedOpt {
	return func(o map[string]any) { o["status"].(map[string]any)["lastBackup"] = ts(t) }
}
func lastSkipped(t time.Time) schedOpt {
	return func(o map[string]any) { o["status"].(map[string]any)["lastSkipped"] = ts(t) }
}
func phase(p string) schedOpt {
	return func(o map[string]any) { o["status"].(map[string]any)["phase"] = p }
}
func specField(k string, v any) schedOpt {
	return func(o map[string]any) { o["spec"].(map[string]any)[k] = v }
}
func created(t time.Time) schedOpt {
	return func(o map[string]any) { o["metadata"].(map[string]any)["creationTimestamp"] = ts(t) }
}

func backup(name, schedule, phase string, createdAt time.Time, extra ...func(map[string]any)) *unstructured.Unstructured {
	meta := map[string]any{"name": name, "namespace": "velero", "creationTimestamp": ts(createdAt)}
	if schedule != "" {
		meta["labels"] = map[string]any{ScheduleNameLabel: schedule}
	}
	status := map[string]any{}
	if phase != "" {
		status["phase"] = phase
	}
	obj := map[string]any{"apiVersion": "velero.io/v1", "kind": "Backup", "metadata": meta, "spec": map[string]any{}, "status": status}
	for _, e := range extra {
		e(obj)
	}
	return &unstructured.Unstructured{Object: obj}
}

func storedAt(loc string) func(map[string]any) {
	return func(o map[string]any) { o["spec"].(map[string]any)["storageLocation"] = loc }
}
func expires(t time.Time) func(map[string]any) {
	return func(o map[string]any) { o["status"].(map[string]any)["expiration"] = ts(t) }
}

var utcServer = ServerConfigFromDeployments([]*appsv1.Deployment{veleroDeployment(nil, nil)}, true)

func read(s *unstructured.Unstructured, backups []*unstructured.Unstructured, server ServerConfig, at time.Time) ScheduleReading {
	return ReadSchedule(ScheduleInput{Schedule: s, Backups: backups, BackupsRead: true, Server: server, DefaultLocation: "default", Now: at})
}

func TestReadScheduleTiming(t *testing.T) {
	hourly := func(opts ...schedOpt) *unstructured.Unstructured {
		return schedule("nightly", "0 * * * *", append([]schedOpt{lastBackup(at("11:00"))}, opts...)...)
	}
	cases := []struct {
		name      string
		s         *unstructured.Unstructured
		backups   []*unstructured.Unstructured
		unread    bool
		now       time.Time
		state     ScheduleState
		reason    string
		attention bool
		missed    int
	}{
		{name: "next run not yet reached", s: hourly(), now: at("11:30"), state: StateOnSchedule},
		{name: "exactly at the next run is not yet due", s: hourly(), now: at("12:00"), state: StateOnSchedule},
		{name: "due within grace", s: hourly(), now: at("12:03"), state: StateDue},
		{name: "late after grace", s: hourly(), now: at("12:10"), state: StateLate, attention: true, missed: 1},
		{name: "missed runs counted, not caught up", s: schedule("nightly", "0 * * * *", lastBackup(at("08:00"))), now: at("12:10"), state: StateLate, attention: true, missed: 4},
		{name: "lastSkipped moves the anchor", s: hourly(lastSkipped(at("12:01"))), now: at("12:10"), state: StateOnSchedule},
		{name: "held by a started run", s: hourly(), backups: []*unstructured.Unstructured{backup("nightly-20261115110000", "nightly", "InProgress", at("11:00"))}, now: at("12:10"), state: StateHeld},
		{name: "held by an unstarted run is attention", s: hourly(), backups: []*unstructured.Unstructured{backup("nightly-20261115115500", "nightly", "New", at("11:55"))}, now: at("12:10"), state: StateHeld, reason: ReasonUnstartedBlocker, attention: true},
		{name: "a finishing run does not hold", s: hourly(), backups: []*unstructured.Unstructured{backup("nightly-20261115110000", "nightly", "Finalizing", at("11:00"))}, now: at("12:10"), state: StateLate, attention: true, missed: 1},
		{name: "unread backups cannot rule out a hold", s: hourly(), unread: true, now: at("12:10"), state: StateLateOrHeld, reason: ReasonBackupsUnread, attention: true},
		{name: "another schedule's run does not hold", s: hourly(), backups: []*unstructured.Unstructured{backup("other-1", "other", "InProgress", at("11:00"))}, now: at("12:10"), state: StateLate, attention: true, missed: 1},
		{name: "never submitted anchors on creation", s: schedule("fresh", "0 * * * *", created(at("11:30"))), now: at("12:10"), state: StateLate, attention: true, missed: 1},
		{name: "cron with no future time", s: schedule("never", "0 0 31 2 *"), now: at("12:10"), state: StateAlwaysDue, reason: ReasonNoFutureTime, attention: true},
		{name: "paused wins over rejection", s: schedule("p", "0 * * * *", specField("paused", true), phase("FailedValidation")), now: at("12:10"), state: StatePaused},
		{name: "rejected is the Issue's, not attention", s: schedule("r", "bad cron", phase("FailedValidation")), now: at("12:10"), state: StateRejected},
		{name: "validation errors reject", s: schedule("r", "0 * * * *", func(o map[string]any) { o["status"].(map[string]any)["validationErrors"] = []any{"x"} }), now: at("12:10"), state: StateRejected},
		{name: "a name too long for a label never submits", s: schedule(strings.Repeat("a", 64), "0 * * * *"), now: at("12:10"), state: StateCannotRun, reason: ReasonNameTooLong, attention: true},
		{name: "unvalidated briefly is not attention", s: schedule("n", "0 * * * *", phase(""), created(at("11:58"))), now: at("12:00"), state: StateNotValidated, reason: ReasonValidationPending},
		{name: "unvalidated for long is attention", s: schedule("n", "0 * * * *", phase("New"), created(at("11:00"))), now: at("12:00"), state: StateNotValidated, reason: ReasonValidationPending, attention: true},
		{name: "enabled but unparseable here", s: schedule("u", "not a cron"), now: at("12:00"), state: StateUnknown, reason: ReasonUnparseable},
		{name: "pending skip is not late", s: hourly(specField("skipImmediately", true)), now: at("12:10"), state: StateSkipPending},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := ReadSchedule(ScheduleInput{Schedule: c.s, Backups: c.backups, BackupsRead: !c.unread, Server: utcServer, DefaultLocation: "default", Now: c.now})
			if r.State != c.state || r.Reason != c.reason || r.Attention != c.attention || r.MissedRuns != c.missed {
				t.Fatalf("got state=%s reason=%q attention=%v missed=%d, want %s %q %v %d", r.State, r.Reason, r.Attention, r.MissedRuns, c.state, c.reason, c.attention, c.missed)
			}
		})
	}
}

func TestServerDownExplainsLateSchedulesAndOverdueValidation(t *testing.T) {
	d := veleroDeployment(nil, nil)
	zero := int32(0)
	d.Spec.Replicas = &zero
	d.Status.Replicas, d.Status.UpdatedReplicas, d.Status.AvailableReplicas = 0, 0, 0
	down := ServerConfigFromDeployments([]*appsv1.Deployment{d}, true)
	if !down.Down() {
		t.Fatalf("a Deployment scaled to zero is down: %+v", down)
	}
	r := read(schedule("nightly", "0 * * * *", lastBackup(at("08:00"))), nil, down, at("12:10"))
	if r.State != StateLate || r.Reason != ReasonServerDown || !r.Attention {
		t.Fatalf("got %s %s attention=%v", r.State, r.Reason, r.Attention)
	}
	onTime := read(schedule("nightly", "0 * * * *", lastBackup(at("12:00"))), nil, down, at("12:10"))
	if onTime.Reason == ReasonServerDown {
		t.Fatal("a schedule that is not late takes no server-down reason")
	}
	// Due expects a submission within minutes; a stopped server won't make one.
	due := read(schedule("nightly", "0 * * * *", lastBackup(at("11:00"))), nil, down, at("12:02"))
	if due.State != StateDue || due.Reason != ReasonServerDown || due.Attention {
		t.Fatalf("due: got %s %s attention=%v", due.State, due.Reason, due.Attention)
	}
	loc := ReadLocation(LocationInput{Location: location("l", func(o map[string]any) {
		o["status"].(map[string]any)["lastValidationTime"] = ts(at("11:00"))
	}), Server: down, Now: at("12:00")})
	if !loc.ValidationOverdue || loc.Reason != ReasonServerDown {
		t.Fatalf("location %+v", loc)
	}
	unready := veleroDeployment(nil, nil)
	unready.Status.AvailableReplicas = 0
	if up := ServerConfigFromDeployments([]*appsv1.Deployment{unready}, true); up.Down() {
		t.Fatal("a running pod that is not yet available still processes schedules; it is not down")
	}
	if (ServerConfig{Source: ServerUnread}).Down() {
		t.Fatal("an unread server is not known to be down")
	}
}

func TestReadScheduleSchedulingDisabled(t *testing.T) {
	server := ServerConfigFromDeployments([]*appsv1.Deployment{veleroDeployment(nil, []string{"--disable-controllers=gc,schedule"})}, true)
	r := read(schedule("nightly", "0 * * * *", lastBackup(at("08:00"))), nil, server, at("12:10"))
	if r.State != StateSchedulingDisabled || r.Attention {
		t.Fatalf("got %s attention=%v", r.State, r.Attention)
	}
}

func TestReadScheduleZones(t *testing.T) {
	t.Run("cron names its zone", func(t *testing.T) {
		r := read(schedule("ny", "CRON_TZ=America/New_York 0 2 * * *", lastBackup(at("07:00"))), nil, ServerConfig{Source: ServerUnread}, at("12:00"))
		if r.Zone != (Zone{Name: "America/New_York", Source: ZoneFromCron}) || !r.NextRunCertain {
			t.Fatalf("zone %+v certain=%v", r.Zone, r.NextRunCertain)
		}
		if want := time.Date(2026, 11, 16, 7, 0, 0, 0, time.UTC); !r.NextRun.Equal(want) {
			t.Fatalf("next run %v, want %v (02:00 EST)", r.NextRun, want)
		}
	})
	t.Run("server TZ is used and certain", func(t *testing.T) {
		server := ServerConfigFromDeployments([]*appsv1.Deployment{veleroDeployment([]corev1.EnvVar{{Name: "TZ", Value: "Asia/Tokyo"}}, nil)}, true)
		r := read(schedule("jp", "0 3 * * *", lastBackup(at("00:00"))), nil, server, at("12:00"))
		if r.Zone.Source != ZoneFromServer || r.Zone.Name != "Asia/Tokyo" || !r.NextRunCertain {
			t.Fatalf("zone %+v", r.Zone)
		}
		if want := time.Date(2026, 11, 15, 18, 0, 0, 0, time.UTC); !r.NextRun.Equal(want) {
			t.Fatalf("next run %v, want %v (03:00 JST)", r.NextRun, want)
		}
	})
	t.Run("offset inferred from a backup name is not certain", func(t *testing.T) {
		runs := []*unstructured.Unstructured{backup("nightly-20261115130000", "nightly", "Completed", at("11:00").Add(2*time.Second))}
		r := read(schedule("nightly", "0 * * * *", lastBackup(at("11:00"))), runs, ServerConfig{Source: ServerUnread}, at("12:10"))
		if r.Zone != (Zone{Name: "UTC+02:00", Source: ZoneInferred}) || r.NextRunCertain {
			t.Fatalf("zone %+v certain=%v", r.Zone, r.NextRunCertain)
		}
		if r.State != StatePossiblyLate || r.Attention {
			t.Fatalf("an unconfirmed zone just past due should be possiblyLate, got %s", r.State)
		}
		later := read(schedule("nightly", "0 * * * *", lastBackup(at("11:00"))), runs, ServerConfig{Source: ServerUnread}, at("14:10"))
		if later.State != StateProbablyLate || later.Reason != ReasonZoneUnconfirmed || !later.Attention {
			t.Fatalf("an inferred zone two hours past due should be probablyLate, got %s", later.State)
		}
	})
	t.Run("a negative offset rounds to the nearest quarter hour", func(t *testing.T) {
		// Named 07:00 local, stamped 12:00:03Z: a server five hours behind UTC.
		runs := []*unstructured.Unstructured{backup("daily-20261115070000", "daily", "Completed", at("12:00").Add(3*time.Second))}
		r := read(schedule("daily", "0 7 * * *", lastBackup(at("12:00"))), runs, ServerConfig{Source: ServerUnread}, at("13:00"))
		if r.Zone != (Zone{Name: "UTC-05:00", Source: ZoneInferred}) {
			t.Fatalf("zone %+v, want UTC-05:00", r.Zone)
		}
		if want := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC); !r.NextRun.Equal(want) {
			t.Fatalf("next run %v, want %v (07:00 at UTC-05:00)", r.NextRun, want)
		}
	})
	t.Run("unknown zone far past due is probably late", func(t *testing.T) {
		r := read(schedule("nightly", "0 * * * *", lastBackup(at("11:00").Add(-48*time.Hour))), nil, ServerConfig{Source: ServerUnread}, at("12:00"))
		if r.Zone.Source != ZoneUnknown || r.State != StateProbablyLate || r.Reason != ReasonZoneUnconfirmed || !r.Attention {
			t.Fatalf("got zone=%+v state=%s reason=%s attention=%v", r.Zone, r.State, r.Reason, r.Attention)
		}
	})
	t.Run("envFrom leaves the zone unknown", func(t *testing.T) {
		d := veleroDeployment(nil, nil)
		d.Spec.Template.Spec.Containers[0].EnvFrom = []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{}}}
		cfg := ServerConfigFromDeployments([]*appsv1.Deployment{d}, true)
		if cfg.TimeZone != "" {
			t.Fatalf("zone %q should be unknown with envFrom", cfg.TimeZone)
		}
	})
}

func TestReadScheduleRuns(t *testing.T) {
	runs := []*unstructured.Unstructured{
		backup("nightly-20261115110000", "nightly", "Failed", at("11:00")),
		backup("nightly-20261115100000", "nightly", "Completed", at("10:00")),
		backup("nightly-20261115090000", "nightly", "Completed", at("09:00")),
		backup("manual", "", "Completed", at("11:30")),
	}
	r := ReadSchedule(ScheduleInput{Schedule: schedule("nightly", "0 * * * *", lastBackup(at("11:00"))), Backups: runs, BackupsRead: true, Server: utcServer, Now: at("11:30"), WithRecent: true})
	if r.Runs.Read != RunsReadFull || r.Runs.Kept != 3 || r.Runs.Newest.Name != "nightly-20261115110000" || r.Runs.NewestCompleted.Name != "nightly-20261115100000" || len(r.Runs.Recent) != 3 {
		t.Fatalf("runs %+v", r.Runs)
	}
	unread := ReadSchedule(ScheduleInput{Schedule: schedule("nightly", "0 * * * *"), Server: utcServer, Now: at("11:30")})
	if unread.Runs.Read != RunsReadUnknown || unread.Runs.Newest != nil {
		t.Fatalf("unread runs must be unknown, got %+v", unread.Runs)
	}
	if r.Location != (ScheduleLocation{Name: "", ViaDefault: true}) {
		t.Fatalf("location %+v", r.Location)
	}
}

func veleroDeployment(env []corev1.EnvVar, args []string) *appsv1.Deployment {
	one := int32(1)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "velero", Namespace: "velero", Generation: 2},
		Spec: appsv1.DeploymentSpec{Replicas: &one, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "velero", Image: "velero/velero:v1.18.1", Command: []string{"/velero"}, Args: append([]string{"server"}, args...), Env: env,
		}}}}},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 2, Replicas: 1, UpdatedReplicas: 1, AvailableReplicas: 1},
	}
}

func TestServerConfigFromDeployments(t *testing.T) {
	cfg := ServerConfigFromDeployments([]*appsv1.Deployment{veleroDeployment(nil, []string{"--default-backup-storage-location", "primary", "--store-validation-frequency=0s", "--restore-only"})}, true)
	if cfg.Source != ServerFromDeployment || cfg.TimeZone != "UTC" || cfg.DefaultLocation != "primary" || cfg.ValidationFrequencySeconds == nil || *cfg.ValidationFrequencySeconds != 0 || !cfg.SchedulingDisabled || !cfg.FlagsKnown() {
		t.Fatalf("cfg %+v", cfg)
	}
	defaults := ServerConfigFromDeployments([]*appsv1.Deployment{veleroDeployment(nil, nil)}, true)
	if defaults.DefaultLocation != "default" || *defaults.ValidationFrequencySeconds != 60 || defaults.SchedulingDisabled {
		t.Fatalf("defaults %+v", defaults)
	}
	expanded := ServerConfigFromDeployments([]*appsv1.Deployment{veleroDeployment(nil, []string{"--default-backup-storage-location=$(LOC)"})}, true)
	if expanded.FlagsKnown() || expanded.DefaultLocation != "" {
		t.Fatalf("a $(VAR) arg must leave flags unknown, got %+v", expanded)
	}
	valueFrom := ServerConfigFromDeployments([]*appsv1.Deployment{veleroDeployment([]corev1.EnvVar{{Name: "TZ", ValueFrom: &corev1.EnvVarSource{}}}, nil)}, true)
	if valueFrom.TimeZone != "" {
		t.Fatalf("a TZ from valueFrom must be unknown, got %q", valueFrom.TimeZone)
	}
	rolling := veleroDeployment(nil, nil)
	rolling.Status.UpdatedReplicas = 0
	if got := ServerConfigFromDeployments([]*appsv1.Deployment{rolling}, true); got.Source != ServerRolling || got.TimeZone != "" {
		t.Fatalf("rolling %+v", got)
	}
	other := veleroDeployment(nil, nil)
	other.Spec.Template.Spec.Containers[0] = corev1.Container{Name: "app", Image: "nginx", Args: []string{"server"}}
	if got := ServerConfigFromDeployments([]*appsv1.Deployment{other}, true); got.Source != ServerNotFound {
		t.Fatalf("a non-Velero server is not Velero: %+v", got)
	}
	if got := ServerConfigFromDeployments(nil, false); got.Source != ServerUnread {
		t.Fatalf("unread %+v", got)
	}
}

func location(name string, opts ...func(map[string]any)) *unstructured.Unstructured {
	obj := map[string]any{
		"apiVersion": "velero.io/v1", "kind": "BackupStorageLocation",
		"metadata": map[string]any{"name": name, "namespace": "velero"},
		"spec":     map[string]any{"provider": "aws", "objectStorage": map[string]any{"bucket": "b"}},
		"status":   map[string]any{"phase": "Available", "lastValidationTime": ts(at("11:59"))},
	}
	for _, o := range opts {
		o(obj)
	}
	return &unstructured.Unstructured{Object: obj}
}

func TestReadLocation(t *testing.T) {
	isDefault := func(o map[string]any) { o["spec"].(map[string]any)["default"] = true }
	locs := []*unstructured.Unstructured{location("primary", isDefault), location("dr")}
	name, src := DefaultLocation(locs, utcServer)
	if name != "primary" || src != DefaultFromSpec {
		t.Fatalf("default %s %s", name, src)
	}
	if name, src := DefaultLocation([]*unstructured.Unstructured{location("dr")}, utcServer); name != "default" || src != DefaultFromServer {
		t.Fatalf("server default %s %s", name, src)
	}
	emptyFlag := ServerConfigFromDeployments([]*appsv1.Deployment{veleroDeployment(nil, []string{"--default-backup-storage-location="})}, true)
	if name, src := DefaultLocation([]*unstructured.Unstructured{location("default")}, emptyFlag); name != "" || src != DefaultNone {
		t.Fatalf("an explicitly empty flag means no default, got %q %s", name, src)
	}
	if name, src := DefaultLocation(nil, ServerConfig{Source: ServerUnread}); name != "" || src != DefaultUnknown {
		t.Fatalf("unknown default %s %s", name, src)
	}

	backups := []*unstructured.Unstructured{
		backup("a", "", "Completed", at("10:00"), storedAt("primary"), expires(at("23:00"))),
		backup("b", "", "Completed", at("09:00"), storedAt("primary"), expires(at("11:00"))),
		backup("c", "", "Failed", at("08:00"), storedAt("primary")),
		backup("d", "", "New", at("11:59")),
		backup("e", "", "Completed", at("10:00"), storedAt("dr")),
	}
	schedules := []*unstructured.Unstructured{
		schedule("explicit", "0 * * * *", func(o map[string]any) {
			o["spec"].(map[string]any)["template"] = map[string]any{"storageLocation": "primary"}
		}),
		schedule("implicit", "0 * * * *"),
		schedule("elsewhere", "0 * * * *", func(o map[string]any) {
			o["spec"].(map[string]any)["template"] = map[string]any{"storageLocation": "dr"}
		}),
	}
	r := ReadLocation(LocationInput{Location: locs[0], Backups: backups, BackupsRead: true, Schedules: schedules, SchedulesRead: true, Server: utcServer, DefaultLocation: "primary", DefaultSource: DefaultFromSpec, Now: at("12:00")})
	if !r.Default || r.Backups == nil || *r.Backups != (LocationBackups{Targeting: 3, CompletedNotExpired: 1, Expired: 1}) {
		t.Fatalf("reading %+v backups %+v", r, r.Backups)
	}
	if len(r.Schedules) != 2 || r.Schedules[0] != (ScheduleRef{Name: "explicit"}) || r.Schedules[1] != (ScheduleRef{Name: "implicit", ViaDefault: true}) {
		t.Fatalf("schedules %+v", r.Schedules)
	}
	if UnassignedBackups(backups) != 1 {
		t.Fatalf("unassigned %d", UnassignedBackups(backups))
	}
	unread := ReadLocation(LocationInput{Location: locs[0], Server: utcServer, Now: at("12:00")})
	if unread.Backups != nil || unread.Schedules != nil {
		t.Fatalf("unread inventories must stay nil, got %+v", unread)
	}
}

func TestEveryScheduleIsLateRegardlessOfZone(t *testing.T) {
	s := schedule("hourly", "@every 1h", lastBackup(at("08:00")))
	r := read(s, nil, ServerConfig{Source: ServerUnread}, at("12:00"))
	if r.State != StateLate || !r.Attention || !r.NextRunCertain || r.MissedRuns != 3 {
		t.Fatalf("got %s attention=%v certain=%v missed=%d", r.State, r.Attention, r.NextRunCertain, r.MissedRuns)
	}
	unread := ReadSchedule(ScheduleInput{Schedule: s, Server: ServerConfig{Source: ServerUnread}, Now: at("12:00")})
	if unread.State != StateLateOrHeld {
		t.Fatalf("with backups unread, got %s", unread.State)
	}
}

func TestRunsRecreatedByBackupSyncKeepTheirRunTime(t *testing.T) {
	started := func(at time.Time) func(map[string]any) {
		return func(o map[string]any) { o["status"].(map[string]any)["startTimestamp"] = ts(at) }
	}
	runs := []*unstructured.Unstructured{
		// The genuinely newer run, created when it ran.
		backup("nightly-20261115090000", "nightly", "Completed", at("09:00"), started(at("09:00"))),
		// Two days old, but its CR was recreated by backup sync a minute ago.
		backup("nightly-20261113090000", "nightly", "Completed", at("11:59"), started(at("09:00").Add(-48*time.Hour))),
	}
	r := ReadSchedule(ScheduleInput{Schedule: schedule("nightly", "0 9 * * *", lastBackup(at("09:00"))), Backups: runs, BackupsRead: true, Server: ServerConfig{Source: ServerUnread}, Now: at("12:00")})
	if r.Runs.NewestCompleted == nil || r.Runs.NewestCompleted.Name != "nightly-20261115090000" {
		t.Fatalf("newest completed %+v, want the run that happened last, not the CR created last", r.Runs.NewestCompleted)
	}
	if r.Zone.Source != ZoneInferred || r.Zone.Name != "UTC+00:00" {
		t.Fatalf("zone %+v: a recreated CR's creation time must not feed the inferred offset", r.Zone)
	}
}

func TestUnknownZoneWithUnreadBackupsKeepsTheHiddenRunCaveat(t *testing.T) {
	r := ReadSchedule(ScheduleInput{
		Schedule: schedule("nightly", "0 * * * *", lastBackup(at("11:00").Add(-48*time.Hour))),
		Server:   ServerConfig{Source: ServerUnread}, Now: at("12:00"),
	})
	if r.State != StateLateOrHeld || r.Reason != ReasonBackupsUnread || !r.Attention || r.NextRunCertain {
		t.Fatalf("got %s %s attention=%v certain=%v", r.State, r.Reason, r.Attention, r.NextRunCertain)
	}
}

func TestLocationNeverValidated(t *testing.T) {
	unvalidated := func(created time.Time, freq string) *unstructured.Unstructured {
		return location("l", func(o map[string]any) {
			o["metadata"].(map[string]any)["creationTimestamp"] = ts(created)
			delete(o["status"].(map[string]any), "lastValidationTime")
			if freq != "" {
				o["spec"].(map[string]any)["validationFrequency"] = freq
			}
		})
	}
	for _, c := range []struct {
		name    string
		loc     *unstructured.Unstructured
		flagged bool
	}{
		{"old and never validated", unvalidated(at("10:00"), ""), true},
		{"old, recurring validation disabled, still validated once", unvalidated(at("10:00"), "0s"), true},
		{"just created", unvalidated(at("11:55"), ""), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := ReadLocation(LocationInput{Location: c.loc, Server: utcServer, Now: at("12:00")})
			if r.NeverValidated != c.flagged || r.Attention != c.flagged {
				t.Fatalf("neverValidated=%v attention=%v", r.NeverValidated, r.Attention)
			}
		})
	}
}

func TestLocationValidation(t *testing.T) {
	validated := func(when time.Time) func(map[string]any) {
		return func(o map[string]any) { o["status"].(map[string]any)["lastValidationTime"] = ts(when) }
	}
	freq := func(v string) func(map[string]any) {
		return func(o map[string]any) { o["spec"].(map[string]any)["validationFrequency"] = v }
	}
	cases := []struct {
		name    string
		loc     *unstructured.Unstructured
		server  ServerConfig
		overdue bool
		source  string
		nilFreq bool
	}{
		{name: "server default, recent", loc: location("l"), server: utcServer, source: "server"},
		{name: "server default, overdue", loc: location("l", validated(at("11:50"))), server: utcServer, overdue: true, source: "server"},
		{name: "spec frequency wins", loc: location("l", validated(at("10:00")), freq("1h0m0s")), server: utcServer, source: "spec"},
		{name: "spec zero disables", loc: location("l", validated(at("00:00")), freq("0s")), server: utcServer, source: "spec"},
		{name: "negative falls back to the server", loc: location("l", validated(at("11:50")), freq("-1m")), server: utcServer, overdue: true, source: "server"},
		{name: "unknown frequency gives no verdict", loc: location("l", validated(at("00:00"))), server: ServerConfig{Source: ServerUnread}, nilFreq: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := ReadLocation(LocationInput{Location: c.loc, Server: c.server, Now: at("12:00")})
			if r.ValidationOverdue != c.overdue || r.Attention != c.overdue || (r.Frequency == nil) != c.nilFreq {
				t.Fatalf("overdue=%v attention=%v freq=%+v", r.ValidationOverdue, r.Attention, r.Frequency)
			}
			if r.Frequency != nil && r.Frequency.Source != c.source {
				t.Fatalf("source %s, want %s", r.Frequency.Source, c.source)
			}
		})
	}
}

// During a migration a stopped server Deployment can sit beside its running
// replacement. Whichever lists first, Velero is running.
func TestServerConfigPrefersTheRunningServer(t *testing.T) {
	stopped := veleroDeployment(nil, nil)
	stopped.Name = "velero-old"
	zero := int32(0)
	stopped.Spec.Replicas = &zero
	stopped.Status.Replicas, stopped.Status.UpdatedReplicas = 0, 0
	running := veleroDeployment(nil, nil)
	for _, order := range [][]*appsv1.Deployment{{stopped, running}, {running, stopped}} {
		cfg := ServerConfigFromDeployments(order, true)
		if cfg.Down() || cfg.Deployment != "velero" {
			t.Fatalf("order %s,%s: %+v, want the running server", order[0].Name, order[1].Name, cfg)
		}
	}
}

func TestZoneInferenceIgnoresNewerOnDemandRun(t *testing.T) {
	controller := backup("nightly-20261115130000", "nightly", "Completed", at("11:00").Add(2*time.Second))
	manual := backup("nightly-20261115113500", "nightly", "Completed", at("11:35"))
	s := schedule("nightly", "0 13 * * *", lastBackup(at("11:00")))
	r := read(s, []*unstructured.Unstructured{manual, controller}, ServerConfig{Source: ServerUnread}, at("12:00"))
	if r.Zone.Name != "UTC+02:00" || r.Zone.Source != ZoneInferred {
		t.Fatalf("manual run changed zone: %+v", r.Zone)
	}
	without := read(s, []*unstructured.Unstructured{manual}, ServerConfig{Source: ServerUnread}, at("12:00"))
	if without.Zone.Source != ZoneUnknown {
		t.Fatalf("manual run established zone: %+v", without.Zone)
	}
}

func TestServerReadyReplicasAndBackupControllerFlags(t *testing.T) {
	for _, tc := range []struct {
		args                 []string
		disabled, scheduling bool
	}{
		{[]string{"--restore-only"}, true, true},
		{[]string{"--restore-only=false"}, false, false},
		{[]string{"--disable-controllers=backup,gc"}, true, false},
		{[]string{"--disable-controllers", "schedule"}, false, true},
		{nil, false, false},
	} {
		d := veleroDeployment(nil, tc.args)
		d.Status.Replicas, d.Status.ReadyReplicas = 1, 0
		cfg := ServerConfigFromDeployments([]*appsv1.Deployment{d}, true)
		if cfg.BackupsDisabled != tc.disabled || cfg.SchedulingDisabled != tc.scheduling || !cfg.FlagsKnown() {
			t.Fatalf("%v: %+v", tc.args, cfg)
		}
		if cfg.Down() || cfg.ReadyReplicas == nil || *cfg.ReadyReplicas != 0 {
			t.Fatalf("unready is not down: %+v", cfg)
		}
		encoded, err := json.Marshal(cfg)
		var decoded ServerConfig
		if err := json.Unmarshal(encoded, &decoded); err != nil || decoded.BackupsDisabled != cfg.BackupsDisabled {
			t.Fatalf("controller fact did not round-trip: %s (%v)", encoded, err)
		}
		if err != nil || !strings.Contains(string(encoded), `"backupsDisabled":`) || !strings.Contains(string(encoded), `"readyReplicas":0`) {
			t.Fatalf("wire %s: %v", encoded, err)
		}
	}
	d := veleroDeployment(nil, []string{"--restore-only", "--default-backup-storage-location=$(LOC)"})
	cfg := ServerConfigFromDeployments([]*appsv1.Deployment{d}, true)
	encoded, _ := json.Marshal(cfg)
	if cfg.FlagsKnown() || strings.Contains(string(encoded), "backupsDisabled") {
		t.Fatalf("unknown flags serialized a fact: %s", encoded)
	}
}

func TestReadScheduleTTLFromTemplate(t *testing.T) {
	s := schedule("nightly", "0 9 * * *", specField("template", map[string]any{"ttl": "720h0m0s"}))
	// A run backup sync recreated: created now, expiring in 30 days less a day.
	runs := []*unstructured.Unstructured{backup("nightly-1", "nightly", "Completed", at("11:00"), expires(at("11:00").Add(24*time.Hour)))}
	if r := read(s, runs, utcServer, at("12:00")); r.TTL != "720h0m0s" {
		t.Fatalf("ttl %q, want the template's", r.TTL)
	}
	if r := read(schedule("nightly", "0 9 * * *"), nil, utcServer, at("12:00")); r.TTL != "" {
		t.Fatalf("ttl %q, want none when the server default applies", r.TTL)
	}
	// Velero's backup controller substitutes its default for a zero duration.
	for _, zero := range []string{"0s", "0h0m0s", "0"} {
		s := schedule("nightly", "0 9 * * *", specField("template", map[string]any{"ttl": zero}))
		if r := read(s, nil, utcServer, at("12:00")); r.TTL != "" {
			t.Fatalf("ttl %q read as %q, want the server default", zero, r.TTL)
		}
	}
}

func TestDeclaredTTL(t *testing.T) {
	for in, want := range map[string]string{"": "", "0s": "", "0h0m0s": "", "0": "", "720h0m0s": "720h0m0s", "1h": "1h", "not-a-duration": "not-a-duration"} {
		if got := DeclaredTTL(in); got != want {
			t.Errorf("DeclaredTTL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestScheduleReadingAlwaysSendsTTL(t *testing.T) {
	b, err := json.Marshal(read(schedule("nightly", "0 9 * * *"), nil, utcServer, at("12:00")))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"ttl":""`) {
		t.Fatalf("reading %s, want an explicit empty ttl for the server default", b)
	}
}

func TestReadScheduleNamespaceSelectionFromTemplate(t *testing.T) {
	s := schedule("nightly", "0 9 * * *", specField("template", map[string]any{"includedNamespaces": []any{"shop", "db-*"}, "excludedNamespaces": []any{"db-scratch"}}))
	r := read(s, nil, utcServer, at("12:00"))
	if !reflect.DeepEqual(r.IncludedNamespaces, []string{"shop", "db-*"}) || !reflect.DeepEqual(r.ExcludedNamespaces, []string{"db-scratch"}) {
		t.Fatalf("selection %v except %v, want the template's", r.IncludedNamespaces, r.ExcludedNamespaces)
	}
	if r := read(schedule("all", "0 9 * * *"), nil, utcServer, at("12:00")); r.IncludedNamespaces == nil || r.ExcludedNamespaces == nil || len(r.IncludedNamespaces) != 0 {
		t.Fatalf("an empty selection is sent as empty lists, got %#v %#v", r.IncludedNamespaces, r.ExcludedNamespaces)
	}
}
