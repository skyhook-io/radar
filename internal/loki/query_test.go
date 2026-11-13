package loki

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

var plain = StreamLabels{Namespace: "namespace", Pod: "pod", Container: "container"}

func TestPodSelector(t *testing.T) {
	if got, _ := PodSelector(plain, "team", "app-1", "web"); got != `{namespace="team", pod="app-1", container="web"}` {
		t.Errorf("selector = %s", got)
	}
	if got, _ := PodSelector(plain, "team", "app-1", `we"b\}`); got != `{namespace="team", pod="app-1", container="we\"b\\}"}` {
		t.Errorf("escaped selector = %s", got)
	}
	if got, _ := PodSelector(plain, "team", "app-1", ""); got != `{namespace="team", pod="app-1"}` {
		t.Errorf("no container = %s", got)
	}
	if _, err := PodSelector(StreamLabels{Pod: "pod"}, "team", "app-1", ""); err == nil {
		t.Error("accepted labels without a namespace label")
	}
	if _, err := PodSelector(StreamLabels{Namespace: "namespace", Pod: "pod"}, "team", "app-1", "web"); err == nil {
		t.Error("accepted a container without a container label")
	}
}

func TestJobSelector(t *testing.T) {
	withJob := plain
	withJob.Job = "job_name"
	if got, notice, _ := JobSelector(withJob, "team", "nightly.sync"); got != `{namespace="team", job_name="nightly.sync"}` || notice != "" {
		t.Errorf("job label: %s / %q", got, notice)
	}
	if got, notice, _ := JobSelector(plain, "team", "nightly.sync"); got != `{namespace="team", pod=~"^nightly\\.sync-([0-9]+-)?[a-z0-9]{5}$"}` || notice != "" {
		t.Errorf("pod regex: %s / %q", got, notice)
	}
	long := strings.Repeat("a", 60)
	got, notice, _ := JobSelector(plain, "team", long)
	if !strings.Contains(got, `"^`+strings.Repeat("a", 58)+`([0-9]+-)?`) || !strings.Contains(notice, "shortened") {
		t.Errorf("long name: %s / %q", got, notice)
	}
}

func TestParseWindow(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	q := func(s string) url.Values { v, _ := url.ParseQuery(s); return v }
	if w, err := ParseWindow(q(""), now); err != nil || w.End != now || w.Start != now.Add(-6*time.Hour) {
		t.Errorf("default = %+v %v", w, err)
	}
	if _, err := ParseWindow(q("since=2h"), now); err == nil {
		t.Error("accepted since=2h")
	}
	if w, err := ParseWindow(q("since=7d"), now); err != nil || w.Start != now.Add(-7*24*time.Hour) {
		t.Errorf("7d = %+v %v", w, err)
	}
	if _, err := ParseWindow(q("start=2026-11-01T00:00:00Z&end=2026-11-09T00:00:00Z"), now); err == nil {
		t.Error("accepted 8 days")
	}
	if _, err := ParseWindow(q("start=2026-11-09T00:00:00Z&end=2026-11-08T00:00:00Z"), now); err == nil {
		t.Error("accepted end before start")
	}
	if w, err := ParseWindow(q("start=2026-11-12T00:00:00Z&end=2026-11-12T01:00:00Z"), now); err != nil || w.End.Sub(w.Start) != time.Hour {
		t.Errorf("explicit = %+v %v", w, err)
	}
}
