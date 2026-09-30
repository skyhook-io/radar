package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
)

func TestCNPGSchedulePreview(t *testing.T) {
	now := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	t.Run("from now without a last check", func(t *testing.T) {
		p := cnpgSchedulePreview("0 30 2 * * *", nil, false, now)
		if !p.Valid || p.Basis != "now" || p.RunsImmediately {
			t.Fatalf("preview = %+v", p)
		}
		want := []string{"2026-10-01T02:30:00Z", "2026-10-02T02:30:00Z", "2026-10-03T02:30:00Z"}
		if strings.Join(p.NextRuns, ",") != strings.Join(want, ",") {
			t.Errorf("runs = %v, want %v", p.NextRuns, want)
		}
		if p.Description != "every day at 02:30:00 UTC" {
			t.Errorf("description = %q", p.Description)
		}
	})
	t.Run("counted from lastCheckTime, as the operator does", func(t *testing.T) {
		last := time.Date(2026, 9, 30, 1, 0, 0, 0, time.UTC)
		p := cnpgSchedulePreview("0 0 6 * * *", &last, false, now)
		if !p.RunsImmediately || p.Basis != "lastCheckTime" || p.NextRuns[0] != "2026-09-30T10:00:00Z" || p.NextRuns[1] != "2026-10-01T06:00:00Z" {
			t.Errorf("a due first run must be immediate: %+v", p)
		}
		if s := cnpgSchedulePreview("0 0 6 * * *", &last, true, now); s.RunsImmediately {
			t.Error("a suspended schedule runs nothing now")
		}
		if f := cnpgSchedulePreview("0 0 12 * * *", &last, false, now); f.RunsImmediately || f.NextRuns[0] != "2026-09-30T12:00:00Z" {
			t.Errorf("future first run = %+v", f)
		}
	})
	t.Run("stepped day-of-month with a day of week follows robfig/cron v1, as the operator does", func(t *testing.T) {
		p := cnpgSchedulePreview("0 0 0 */2 * 1", nil, false, now)
		if !p.Valid || p.NextRuns[0] != "2026-10-05T00:00:00Z" {
			t.Errorf("first run = %v, want 2026-10-05 (v1 semantics; v3 would say 2026-10-01)", p.NextRuns)
		}
	})
	for _, bad := range []string{"", "0 0 * * *  * *", "not cron", "0 0 0 31 2 *", "CRON_TZ=Europe/Berlin 0 0 0 * * *", "TZ=UTC 0 0 0 * * *", "0 0 0 * * 7", strings.Repeat("1", 300)} {
		if p := cnpgSchedulePreview(bad, nil, false, now); p.Valid || p.Error == "" || len(p.NextRuns) != 0 {
			t.Errorf("%q accepted: %+v", bad, p)
		}
	}
	if p := cnpgSchedulePreview("0 0 0 * *", nil, false, now); !p.Valid {
		t.Errorf("five fields (day of week optional) rejected: %s", p.Error)
	}
}

func TestDescribeCNPGSchedule(t *testing.T) {
	for spec, want := range map[string]string{
		"0 0 0 * * *":       "every day at 00:00:00 UTC",
		"@daily":            "every day at 00:00:00 UTC",
		"@every 1h30m":      "every 1h30m, counted from the operator's last check",
		"0 15 3 * * 1-5":    "every Monday through Friday at 03:15:00 UTC",
		"0 0 1 * * sun,wed": "every Sunday and Wednesday at 01:00:00 UTC",
		"0 0 4 1,15 * *":    "on day 1 and 15 of the month at 04:00:00 UTC",
		"0 0 4 1 jan,7 *":   "on day 1 of the month in January and July at 04:00:00 UTC",
		"0 */15 * * * *":    "every 15 minutes, second 0",
		"30 0 9-17 * * *":   "at 00:30 past the hour, during hours 9 through 17",
		"0 0 */6 * * 1":     "every Monday, at 00:00 past the hour, every 6 hours",
		"0 0 0 */2 * 1":     "every 2 days of the month from day 1, when it is a Monday at 00:00:00 UTC",
		"0 0 0 1,15 * 1":    "on day 1 and 15 of the month or every Monday at 00:00:00 UTC",
	} {
		if got := describeCNPGSchedule(spec); got != want {
			t.Errorf("describe(%q) = %q, want %q", spec, got, want)
		}
	}
}

func TestCNPGActionSetSchedule(t *testing.T) {
	req := func(reviewed, next string) CNPGActionRequest {
		facts, _ := json.Marshal(map[string]any{"schedule": reviewed})
		params, _ := json.Marshal(map[string]any{"schedule": next})
		return CNPGActionRequest{ReviewedContext: "kind-test", UID: "sched-uid", Facts: facts, Params: params}
	}
	t.Run("merge-patches spec.schedule bound to the resourceVersion", func(t *testing.T) {
		env := newCNPGActionEnv(t, []runtime.Object{cnpgActionCluster(nil), cnpgActionSchedule(nil)})
		res, err := runCNPGScheduleAction(context.Background(), env.clients(), "db", "nightly", "setSchedule", req("0 0 0 * * *", "0 30 2 * * *"))
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
		_, err := runCNPGScheduleAction(context.Background(), env.clients(), "db", "nightly", "setSchedule", req("0 0 1 * * *", "0 30 2 * * *"))
		if ae, ok := cnpgActionStatus(t, err); !ok || ae.Code != cnpgCodeChanged || len(env.patches) != 0 {
			t.Fatalf("err = %v, want 409 changed and no write", err)
		}
	})
	t.Run("rejects an invalid schedule before any read or write", func(t *testing.T) {
		env := newCNPGActionEnv(t, []runtime.Object{cnpgActionCluster(nil), cnpgActionSchedule(nil)})
		_, err := runCNPGScheduleAction(context.Background(), env.clients(), "db", "nightly", "setSchedule", req("0 0 0 * * *", "0 0 25 * * *"))
		if ae, ok := cnpgActionStatus(t, err); !ok || ae.Status != 400 || ae.Code != cnpgCodeInvalidSchedule || len(env.patches) != 0 {
			t.Fatalf("err = %v, want 400 invalid_schedule", err)
		}
	})
	t.Run("requires the reviewed schedule", func(t *testing.T) {
		env := newCNPGActionEnv(t, []runtime.Object{cnpgActionCluster(nil), cnpgActionSchedule(nil)})
		r := req("", "0 30 2 * * *")
		r.Facts = json.RawMessage(`{}`)
		if ae, ok := cnpgActionStatus(t, func() error {
			_, err := runCNPGScheduleAction(context.Background(), env.clients(), "db", "nightly", "setSchedule", r)
			return err
		}()); !ok || ae.Status != 400 {
			t.Fatalf("missing facts.schedule: %v", ae)
		}
	})
	t.Run("an unchanged schedule is blocked", func(t *testing.T) {
		env := newCNPGActionEnv(t, []runtime.Object{cnpgActionCluster(nil), cnpgActionSchedule(nil)})
		_, err := runCNPGScheduleAction(context.Background(), env.clients(), "db", "nightly", "setSchedule", req("0 0 0 * * *", "0 0 0 * * *"))
		if ae, ok := cnpgActionStatus(t, err); !ok || ae.Code != cnpgCodeBlocked {
			t.Fatalf("err = %v, want blocked", err)
		}
	})
}

func TestCNPGScheduleCapabilitiesCarryScheduleAndPreview(t *testing.T) {
	sched := cnpgActionSchedule(func(o map[string]any) {
		o["status"].(map[string]any)["lastCheckTime"] = "2026-09-28T00:00:00Z"
	})
	env := newCNPGActionEnv(t, []runtime.Object{cnpgActionCluster(nil), sched})
	resp, err := (&Server{}).cnpgScheduleCapabilities(httptest.NewRequest(http.MethodGet, "/", nil), env.clients(), "kind-test", "db", "nightly")
	if err != nil {
		t.Fatal(err)
	}
	f := resp.Facts
	if f.Schedule != "0 0 0 * * *" || !f.Preview.Valid || f.Preview.Basis != "lastCheckTime" || f.Preview.Description != "every day at 00:00:00 UTC" || len(f.Preview.NextRuns) != 3 {
		t.Errorf("facts = %+v", f)
	}
	if !f.Preview.RunsImmediately {
		t.Error("a run due since lastCheckTime must be reported as immediate")
	}
	if !resp.Actions.SetSchedule.Allowed {
		t.Errorf("setSchedule = %+v", resp.Actions.SetSchedule)
	}
}
