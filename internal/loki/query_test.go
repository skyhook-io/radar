package loki

import (
	"fmt"
	"net/url"
	"regexp"
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
	// Without a container label the whole pod is read; the answer says so by its key.
	if got, err := PodSelector(StreamLabels{Namespace: "namespace", Pod: "pod"}, "team", "app-1", "web"); err != nil || got != `{namespace="team", pod="app-1"}` {
		t.Errorf("no container label: %s %v", got, err)
	}
}

// podNames are what Kubernetes names a Job's pods: generateName "<job>-",
// which the API server cuts to 58 characters; an Indexed Job's controller
// keeps "-<index>-" whole and cuts the Job name instead.
func podName(job string, indexed bool, index int) string {
	if !indexed {
		p := job + "-"
		if len(p) > 58 {
			p = p[:58]
		}
		return p + "x7k2p"
	}
	suffix := fmt.Sprintf("-%d-", index)
	p := job + suffix
	if len(p) > 58 {
		p = p[:58-len(suffix)] + suffix
	}
	return p + "x7k2p"
}

func selectorRegex(t *testing.T, sel string) *regexp.Regexp {
	t.Helper()
	raw := strings.TrimSuffix(strings.SplitN(sel, `pod=~"`, 2)[1], `"}`)
	return regexp.MustCompile(strings.ReplaceAll(raw, `\\`, `\`))
}

func TestJobSelector(t *testing.T) {
	withJob := plain
	withJob.Job = "job_name"
	if got, notice, _ := JobSelector(withJob, "team", "nightly.sync", false); got != `{namespace="team", job_name="nightly.sync"}` || notice != "" {
		t.Errorf("job label: %s / %q", got, notice)
	}
	if got, notice, _ := JobSelector(plain, "team", "nightly.sync", false); got != `{namespace="team", pod=~"^nightly\\.sync-[a-z0-9]{5}$"}` || notice != "" {
		t.Errorf("pod regex: %s / %q", got, notice)
	}
	for _, c := range []struct {
		job     string
		indexed bool
	}{
		{"nightly.sync", false}, {"backup", false}, {strings.Repeat("b", 56), false}, {strings.Repeat("a", 60), false},
		{"train", true}, {strings.Repeat("c", 54), true}, {strings.Repeat("d", 56), true}, {strings.Repeat("e", 70), true},
	} {
		sel, notice, err := JobSelector(plain, "team", c.job, c.indexed)
		if err != nil {
			t.Fatal(err)
		}
		re := selectorRegex(t, sel)
		indexes := []int{0}
		if c.indexed {
			indexes = []int{0, 7, 12, 345}
		}
		for _, i := range indexes {
			if pod := podName(c.job, c.indexed, i); !re.MatchString(pod) {
				t.Errorf("%s (indexed %v): %s does not match %s", c.job, c.indexed, pod, re)
			}
		}
		if cut := len(podName(c.job, c.indexed, 345)) < len(c.job)+len("-345-x7k2p") && c.indexed || !c.indexed && len(c.job) > 57; cut != (notice != "") {
			t.Errorf("%s: notice %q, cut %v", c.job, notice, cut)
		}
	}
	// A CronJob's Jobs are "<cronjob>-<schedule>": a Job named after the
	// CronJob must not read their pods.
	sel, _, _ := JobSelector(plain, "team", "backup", false)
	if re := selectorRegex(t, sel); re.MatchString("backup-29123456-x7k2p") {
		t.Error("Job backup matched a pod of Job backup-29123456")
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
