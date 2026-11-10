package cnpg

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	integration "github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/pkg/cnpg"
)

func TestScheduleClockEvidence(t *testing.T) {
	operator := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "operator", Namespace: "system", Labels: map[string]string{cnpgOperatorNameLabel: cnpgOperatorNameValue}}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: cnpgOperatorContainer, Env: []corev1.EnvVar{{Name: "TZ", Value: "America/New_York"}}}}}}}}
	clock, zone := scheduleClockOf([]*appsv1.Deployment{operator}, true, "db")
	if !clock.Declared || clock.Zone != "America/New_York" {
		t.Fatalf("clock=%+v", clock)
	}
	p := schedulePreviewIn("0 30 2 * * *", nil, false, time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), zone)
	if !p.Valid || p.NextRuns[0] != "2026-10-01T06:30:00Z" || strings.Contains(p.Description, "UTC") {
		t.Fatalf("preview=%+v", p)
	}
	for _, scenario := range []string{"partial", "multiple", "valueFrom", "absent", "envFrom", "watch-unresolved", "duplicate-tz"} {
		t.Run(scenario, func(t *testing.T) {
			d := operator.DeepCopy()
			deployments := []*appsv1.Deployment{d}
			full := true
			switch scenario {
			case "partial":
				full = false
			case "multiple":
				deployments = append(deployments, operator.DeepCopy())
			case "valueFrom":
				d.Spec.Template.Spec.Containers[0].Env[0] = corev1.EnvVar{Name: "TZ", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{}}}
			case "absent":
				d.Spec.Template.Spec.Containers[0].Env = nil
			case "duplicate-tz":
				d.Spec.Template.Spec.Containers[0].Env = append(d.Spec.Template.Spec.Containers[0].Env, corev1.EnvVar{Name: "TZ", Value: "UTC"})
			case "envFrom":
				d.Spec.Template.Spec.Containers[0].EnvFrom = []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{}}}
			case "watch-unresolved":
				d.Spec.Template.Spec.Containers[0].Env = append(d.Spec.Template.Spec.Containers[0].Env, corev1.EnvVar{Name: cnpgWatchNamespaceEnv, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{}}})
			}
			clock, zone := scheduleClockOf(deployments, full, "db")
			if clock.Declared || zone != time.UTC || !strings.Contains(clock.Source, "assume") {
				t.Fatalf("clock=%+v zone=%s", clock, zone)
			}
		})
	}
}

func TestCNPGSchedulePreview(t *testing.T) {
	now := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	t.Run("from now without a last check", func(t *testing.T) {
		p := schedulePreview("0 30 2 * * *", nil, false, now)
		if !p.Valid || p.Basis != "now" || p.RunsImmediately {
			t.Fatalf("preview = %+v", p)
		}
		want := []string{"2026-10-01T02:30:00Z", "2026-10-02T02:30:00Z", "2026-10-03T02:30:00Z"}
		if strings.Join(p.NextRuns, ",") != strings.Join(want, ",") {
			t.Errorf("runs = %v, want %v", p.NextRuns, want)
		}
		if p.Description != "every day at 02:30 UTC" {
			t.Errorf("description = %q", p.Description)
		}
	})
	t.Run("counted from lastCheckTime, as the operator does", func(t *testing.T) {
		last := time.Date(2026, 9, 30, 1, 0, 0, 0, time.UTC)
		p := schedulePreview("0 0 6 * * *", &last, false, now)
		if !p.RunsImmediately || p.Basis != "lastCheckTime" || p.NextRuns[0] != "2026-09-30T10:00:00Z" || p.NextRuns[1] != "2026-10-01T06:00:00Z" {
			t.Errorf("a due first run must be immediate: %+v", p)
		}
		if s := schedulePreview("0 0 6 * * *", &last, true, now); s.RunsImmediately {
			t.Error("a suspended schedule runs nothing now")
		}
		if f := schedulePreview("0 0 12 * * *", &last, false, now); f.RunsImmediately || f.NextRuns[0] != "2026-09-30T12:00:00Z" {
			t.Errorf("future first run = %+v", f)
		}
	})
	t.Run("stepped day-of-month with a day of week follows robfig/cron v1, as the operator does", func(t *testing.T) {
		p := schedulePreview("0 0 0 */2 * 1", nil, false, now)
		if !p.Valid || p.NextRuns[0] != "2026-10-05T00:00:00Z" {
			t.Errorf("first run = %v, want 2026-10-05 (v1 semantics; v3 would say 2026-10-01)", p.NextRuns)
		}
	})
	for _, bad := range []string{"", "0 0 * * *  * *", "not cron", "0 0 0 31 2 *", "CRON_TZ=Europe/Berlin 0 0 0 * * *", "TZ=UTC 0 0 0 * * *", "0 0 0 * * 7", strings.Repeat("1", 300)} {
		if p := schedulePreview(bad, nil, false, now); p.Valid || p.Error == "" || len(p.NextRuns) != 0 {
			t.Errorf("%q accepted: %+v", bad, p)
		}
	}
	if p := schedulePreview("0 0 0 * *", nil, false, now); !p.Valid {
		t.Errorf("five fields (day of week optional) rejected: %s", p.Error)
	}
}

func TestDescribeCNPGSchedule(t *testing.T) {
	for spec, want := range map[string]string{
		"0 0 0 * * *":       "every day at 00:00 operator clock",
		"0 0 2 * * *":       "every day at 02:00 operator clock",
		"@daily":            "every day at 00:00 operator clock",
		"@hourly":           "every hour, on the hour",
		"0 0 * * * *":       "every hour, on the hour",
		"0 15 * * * *":      "every hour at :15",
		"@every 1h30m":      "every 1h30m, counted from the operator's last check",
		"0 30 2 * * 1-5":    "every Monday through Friday at 02:30 operator clock",
		"0 15 3 * * 1-5":    "every Monday through Friday at 03:15 operator clock",
		"0 0 1 * * sun,wed": "every Sunday and Wednesday at 01:00 operator clock",
		"0 0 4 1,15 * *":    "on day 1 and 15 of the month at 04:00 operator clock",
		"0 0 4 1 jan,7 *":   "on day 1 of the month in January and July at 04:00 operator clock",
		"15 30 4 * * *":     "every day at 04:30:15 operator clock",
		"0 */15 * * * *":    "every 15 minutes",
		"30 0 9-17 * * *":   "every hour at :00:30, during hours 9 through 17",
		"0 0 */6 * * 1":     "every Monday, every 6 hours, on the hour",
		"0 0 0 */2 * 1":     "every 2 days of the month from day 1, when it is a Monday at 00:00 operator clock",
		"0 0 0 1,15 * 1":    "on day 1 and 15 of the month or every Monday at 00:00 operator clock",
	} {
		if got := describeCNPGSchedule(spec); got != want {
			t.Errorf("describe(%q) = %q, want %q", spec, got, want)
		}
	}
}

func TestCNPGActionSetSchedule(t *testing.T) {
	req := func(reviewed, next string) integration.ActionRequest {
		facts, _ := json.Marshal(map[string]any{"schedule": reviewed})
		params, _ := json.Marshal(map[string]any{"schedule": next})
		return integration.ActionRequest{ReviewedContext: "kind-test", UID: "sched-uid", Facts: facts, Params: params}
	}
	t.Run("merge-patches spec.schedule bound to the resourceVersion", func(t *testing.T) {
		env := newCNPGActionEnv(t, []runtime.Object{cnpgActionCluster(nil), cnpgActionSchedule(nil)})
		res, err := RunCNPGScheduleAction(context.Background(), env.clients(), "db", "nightly", "setSchedule", req("0 0 0 * * *", "0 30 2 * * *"))
		if err != nil {
			t.Fatal(err)
		}
		if res.Action != "setSchedule" || len(env.patches) != 1 {
			t.Fatalf("res = %+v patches = %d", res, len(env.patches))
		}
		body := cnpgActionPatchBody(t, env.patches[0])
		if body["spec"].(map[string]any)["schedule"] != "0 30 2 * * *" || body["metadata"].(map[string]any)["resourceVersion"] != "7" || len(body["spec"].(map[string]any)) != 1 {
			t.Errorf("patch = %v", body)
		}
	})
	t.Run("refuses a schedule changed since review", func(t *testing.T) {
		env := newCNPGActionEnv(t, []runtime.Object{cnpgActionCluster(nil), cnpgActionSchedule(nil)})
		_, err := RunCNPGScheduleAction(context.Background(), env.clients(), "db", "nightly", "setSchedule", req("0 0 1 * * *", "0 30 2 * * *"))
		if ae, ok := cnpgActionStatus(t, err); !ok || ae.Code != integration.ActionCodeChanged || len(env.patches) != 0 {
			t.Fatalf("err = %v, want 409 changed and no write", err)
		}
	})
	t.Run("rejects an invalid schedule before any read or write", func(t *testing.T) {
		env := newCNPGActionEnv(t, []runtime.Object{cnpgActionCluster(nil), cnpgActionSchedule(nil)})
		_, err := RunCNPGScheduleAction(context.Background(), env.clients(), "db", "nightly", "setSchedule", req("0 0 0 * * *", "0 0 25 * * *"))
		if ae, ok := cnpgActionStatus(t, err); !ok || ae.Status != 400 || ae.Code != cnpgCodeInvalidSchedule || len(env.patches) != 0 {
			t.Fatalf("err = %v, want 400 invalid_schedule", err)
		}
	})
	t.Run("requires the reviewed schedule", func(t *testing.T) {
		env := newCNPGActionEnv(t, []runtime.Object{cnpgActionCluster(nil), cnpgActionSchedule(nil)})
		r := req("", "0 30 2 * * *")
		r.Facts = json.RawMessage(`{}`)
		if ae, ok := cnpgActionStatus(t, func() error {
			_, err := RunCNPGScheduleAction(context.Background(), env.clients(), "db", "nightly", "setSchedule", r)
			return err
		}()); !ok || ae.Status != 400 {
			t.Fatalf("missing facts.schedule: %v", ae)
		}
	})
	t.Run("an unchanged schedule is blocked", func(t *testing.T) {
		env := newCNPGActionEnv(t, []runtime.Object{cnpgActionCluster(nil), cnpgActionSchedule(nil)})
		_, err := RunCNPGScheduleAction(context.Background(), env.clients(), "db", "nightly", "setSchedule", req("0 0 0 * * *", "0 0 0 * * *"))
		if ae, ok := cnpgActionStatus(t, err); !ok || ae.Code != integration.ActionCodeBlocked {
			t.Fatalf("err = %v, want blocked", err)
		}
	})
}

func TestCNPGScheduleCapabilitiesCarryScheduleAndPreview(t *testing.T) {
	sched := cnpgActionSchedule(func(o map[string]any) {
		o["status"].(map[string]any)["lastCheckTime"] = "2026-09-28T00:00:00Z"
	})
	env := newCNPGActionEnv(t, []runtime.Object{cnpgActionCluster(nil), sched})
	resp, err := newTestReader(nil).ScheduleCapabilities(context.Background(), env.clients(), "kind-test", "db", "nightly")
	if err != nil {
		t.Fatal(err)
	}
	f := resp.Facts
	if f.Schedule != "0 0 0 * * *" || !f.Preview.Valid || f.Preview.Basis != "lastCheckTime" || f.Preview.Description != "every day at 00:00 UTC" || len(f.Preview.NextRuns) != 3 {
		t.Errorf("facts = %+v", f)
	}
	if !f.Preview.RunsImmediately {
		t.Error("a run due since lastCheckTime must be reported as immediate")
	}
	if !resp.Actions.SetSchedule.Allowed {
		t.Errorf("setSchedule = %+v", resp.Actions.SetSchedule)
	}
}

func TestCNPGScheduleReadingsWordOnlyValidSchedules(t *testing.T) {
	sb := func(name, spec string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"metadata": map[string]any{"namespace": "db", "name": name},
			"spec":     map[string]any{"schedule": spec},
		}}
	}
	got := scheduleReadings([]*unstructured.Unstructured{sb("daily", "0 0 2 * * *"), sb("bad", "not a cron"), sb("empty", "")})
	if got["db/daily"] != "every day at 02:00 operator clock" {
		t.Errorf("daily = %q", got["db/daily"])
	}
	if _, ok := got["db/bad"]; ok {
		t.Errorf("an unparseable schedule was worded: %q", got["db/bad"])
	}
	if _, ok := got["db/empty"]; ok {
		t.Error("an empty schedule was worded")
	}
}

// Each reading is checked against when the operator's parser actually runs the
// schedule, so a description can never promise runs the parser would not make.
func TestDescribeCNPGScheduleMatchesTheParser(t *testing.T) {
	start := time.Date(2026, 10, 10, 18, 30, 0, 0, time.UTC) // a Saturday
	cases := []struct {
		spec string
		want string
		runs []string // the parser's next runs from start
	}{
		{"0 0 */5 * * *", "every day at 00:00, 05:00, 10:00, 15:00 and 20:00 operator clock", []string{"2026-10-10T20:00:00Z", "2026-10-11T00:00:00Z"}},
		{"0 0 */6 * * *", "every 6 hours, on the hour", []string{"2026-10-11T00:00:00Z", "2026-10-11T06:00:00Z"}},
		// Five fields are seconds through month; the day of week is optional.
		{"0 30 2 * *", "every day at 02:30 operator clock", []string{"2026-10-11T02:30:00Z", "2026-10-12T02:30:00Z"}},
		{"0 0 */6,13 * * *", "cron 0 0 */6,13 * * *", nil},
		{"0 0 0 1,*/2 * 1", "cron 0 0 0 1,*/2 * 1", nil},
		{"0 0 0 */2 * 1", "every 2 days of the month from day 1, when it is a Monday at 00:00 operator clock", []string{"2026-10-19T00:00:00Z"}},
		{"@every 500ms", "every 1s, counted from the operator's last check", []string{"2026-10-10T18:30:01Z"}},
		{"@every 1.5s", "every 1s, counted from the operator's last check", []string{"2026-10-10T18:30:01Z"}},
		{"@every 1h30m", "every 1h30m, counted from the operator's last check", []string{"2026-10-10T20:00:00Z"}},
	}
	for _, c := range cases {
		if got := describeCNPGSchedule(c.spec); got != c.want {
			t.Errorf("describe(%q) = %q, want %q", c.spec, got, c.want)
		}
		sched, err := cnpg.ParseSchedule(c.spec)
		if err != nil {
			t.Fatalf("%q: %v", c.spec, err)
		}
		at := start
		for _, want := range c.runs {
			at = sched.Next(at)
			if got := at.UTC().Format(time.RFC3339); got != want {
				t.Errorf("%q: next run %s, want %s (the reading must match)", c.spec, got, want)
			}
		}
	}
	// */17 minutes restarts each hour: 0, 17, 34, 51 — never every 17 minutes across the hour.
	got := describeCNPGSchedule("0 */17 * * * *")
	if !strings.Contains(got, "0, 17, 34, 51") || strings.Contains(got, "every 17") {
		t.Errorf("*/17 minutes = %q", got)
	}
}
