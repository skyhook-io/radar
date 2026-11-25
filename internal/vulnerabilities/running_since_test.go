package vulnerabilities

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var (
	day1 = time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	day2 = time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	day3 = time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
)

// startedOn is p with its app container started at at, reporting imageID.
func startedOn(p *corev1.Pod, imageID string, at time.Time) *corev1.Pod {
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "app", ImageID: imageID,
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(at)}},
	}}
	return p
}

// since is t as having run since at.
func since(t target, at time.Time) target {
	t.runningSince = at
	return t
}

// Running since comes from each running container's start, keyed by the
// digest the kubelet reports: replicas on one build give the earliest, a
// second build under the same tag keeps its own, and a container with no
// registry digest yet, or not running, gives none.
func TestLiveTargetsRunningSince(t *testing.T) {
	rs := owned("apps/v1", "ReplicaSet", "web-1")
	waiting := pod("web-1-e", corev1.PodPending, rs, "acme/web:1")
	waiting.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "app", ImageID: "docker-pullable://acme/web@sha256:aaa",
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
	}}
	cache := syncedCache(t,
		startedOn(pod("web-1-a", corev1.PodRunning, rs, "acme/web:1"), "docker-pullable://acme/web@sha256:AAA", day2),
		startedOn(pod("web-1-b", corev1.PodRunning, rs, "acme/web:1"), "docker-pullable://acme/web@sha256:aaa", day1),
		startedOn(pod("web-1-c", corev1.PodRunning, rs, "acme/web:1"), "docker-pullable://acme/web@sha256:bbb", day3),
		startedOn(pod("web-1-d", corev1.PodRunning, rs, "acme/web:1"), "", day1),
		startedOn(pod("local", corev1.PodRunning, nil, "acme/local:1"), "sha256:abc", day1),
		waiting,
	)
	live, ok, err := liveTargets(context.Background(), cache, []string{"shop"}, nil, nil, exposureIndex{})
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
	got := map[string]time.Time{}
	for _, tg := range live.targets {
		got[tg.key.name+"@"+tg.digest] = tg.runningSince
	}
	want := map[string]time.Time{
		"web-1@sha256:aaa": day1, // the earlier replica; the waiting one adds nothing
		"web-1@sha256:bbb": day3,
		"web-1@":           {}, // no imageID yet
		"local@":           {}, // a local image ID is no registry digest
	}
	if len(got) != len(want) {
		t.Fatalf("targets %v", got)
	}
	for k, v := range want {
		if at, ok := got[k]; !ok || !at.Equal(v) {
			t.Fatalf("%s: %v, want %v (all: %v)", k, at, v, got)
		}
	}
}

func runningSinceFixture() ([]*unstructured.Unstructured, []target) {
	items := []*unstructured.Unstructured{
		vulnReport(reportSpec{name: "a", ns: "shop", ownerKind: "ReplicaSet", ownerName: "checkout-1", container: "app", repo: "acme/checkout", tag: "1", digest: "sha256:a", scanned: "2026-11-20T00:00:00Z", vulns: []vuln{{"CVE-A", "CRITICAL", "zlib", "1", "2"}}}),
		vulnReport(reportSpec{name: "b", ns: "shop", ownerKind: "ReplicaSet", ownerName: "checkout-1", container: "app", repo: "acme/checkout", tag: "1", digest: "sha256:b", scanned: "2026-11-20T00:00:00Z", vulns: []vuln{{"CVE-B", "HIGH", "zlib", "1", "2"}}}),
		// worker's Pod hasn't started, so it matches its report by tag.
		vulnReport(reportSpec{name: "w", ns: "shop", ownerKind: "ReplicaSet", ownerName: "worker-1", container: "app", repo: "acme/worker", tag: "1", digest: "sha256:w", scanned: "2026-11-20T00:00:00Z", vulns: []vuln{{"CVE-W", "HIGH", "zlib", "1", "2"}}}),
	}
	targets := []target{
		since(running(liveTarget("shop", "ReplicaSet", "checkout-1", "app", "acme/checkout:1", "", checkout), "sha256:a"), day2),
		// Two builds of one tag under one template: each keeps its own start.
		since(running(liveTarget("shop", "ReplicaSet", "checkout-1", "app", "acme/checkout:1", "", checkout), "sha256:b"), day3),
		// The same build in another workload started earlier.
		since(running(liveTarget("shop", "ReplicaSet", "db-1", "app", "acme/checkout:1", "", db), "sha256:a"), day1),
		liveTarget("shop", "ReplicaSet", "worker-1", "app", "acme/worker:1", "", worker),
	}
	return items, targets
}

// An image row runs since the earliest of its workloads; a workload's
// containers keep their own build's start and the workload the earliest of
// them; a workload with no running Pod has none.
func TestRunningSinceRollsUp(t *testing.T) {
	items, targets := runningSinceFixture()
	images := mustBuild(t, items, targets, ViewImage, Filter{}).Images
	byID := map[string]Image{}
	for _, img := range images {
		byID[img.ID] = img
	}
	for id, want := range map[string]*time.Time{"sha256:a": &day1, "sha256:b": &day3, "sha256:w": nil} {
		if got := byID[id].RunningSince; !sameTime(got, want) {
			t.Fatalf("image %s: %v, want %v", id, got, want)
		}
	}

	byName := map[string]Workload{}
	for _, w := range mustBuild(t, items, targets, ViewWorkload, Filter{}).Workloads {
		byName[w.Name] = w
	}
	co := byName["checkout"]
	if len(co.Containers) != 2 || !sameTime(co.RunningSince, &day2) {
		t.Fatalf("checkout: %+v", co)
	}
	for _, c := range co.Containers {
		want := map[string]*time.Time{"sha256:a": &day2, "sha256:b": &day3}[c.ImageID]
		if !sameTime(c.RunningSince, want) {
			t.Fatalf("container on %s: %v, want %v", c.ImageID, c.RunningSince, want)
		}
	}
	if !sameTime(byName["db"].RunningSince, &day1) {
		t.Fatalf("db: %+v", byName["db"])
	}
	if w, ok := byName["worker"]; !ok || w.RunningSince != nil || w.Containers[0].RunningSince != nil {
		t.Fatalf("worker has no running Pod: %+v", w)
	}
}

// The image and workload CSVs carry each row's running_since; the findings
// CSV each workload container's own.
func TestExportRunningSince(t *testing.T) {
	items, targets := runningSinceFixture()
	cell := func(at time.Time) string { return at.Format(time.RFC3339) }

	for _, r := range exportRows(t, mustExport(t, items, targets, ViewImage, Filter{}), Filter{}) {
		want := map[string]string{"sha256:a": cell(day1), "sha256:b": cell(day3), "sha256:w": ""}[r["digest"]]
		if r["running_since"] != want {
			t.Fatalf("image %s: %q, want %q", r["digest"], r["running_since"], want)
		}
	}
	for _, r := range exportRows(t, mustExport(t, items, targets, ViewWorkload, Filter{}), Filter{}) {
		want := map[string]string{"checkout": cell(day2), "db": cell(day1), "worker": ""}[r["workload"]]
		if r["running_since"] != want {
			t.Fatalf("workload %s: %q, want %q", r["workload"], r["running_since"], want)
		}
	}
	rows := exportRows(t, mustExport(t, items, targets, ViewFindings, Filter{}), Filter{})
	if len(rows) != 4 {
		t.Fatalf("rows %v", rows)
	}
	for _, r := range rows {
		want := map[string]string{"CVE-A/checkout": cell(day2), "CVE-A/db": cell(day1), "CVE-B/checkout": cell(day3), "CVE-W/worker": ""}[r["cve"]+"/"+r["workload"]]
		if r["running_since"] != want {
			t.Fatalf("finding %s in %s: %q, want %q", r["cve"], r["workload"], r["running_since"], want)
		}
	}
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

// A CVE runs since the earliest of the images that carry it, in the table
// and in the CVE CSV; one whose images have no running Pod has none.
func TestCVERunningSince(t *testing.T) {
	items, targets := runningSinceFixture()
	// CVE-B is in both builds: a has run since day1, b since day3.
	items[0] = vulnReport(reportSpec{name: "a", ns: "shop", ownerKind: "ReplicaSet", ownerName: "checkout-1", container: "app", repo: "acme/checkout", tag: "1", digest: "sha256:a", scanned: "2026-11-20T00:00:00Z", vulns: []vuln{{"CVE-A", "CRITICAL", "zlib", "1", "2"}, {"CVE-B", "HIGH", "zlib", "1", "2"}}})
	want := map[string]*time.Time{"CVE-A": &day1, "CVE-B": &day1, "CVE-W": nil}
	for _, c := range mustBuild(t, items, targets, ViewCVE, Filter{}).CVEs {
		if w, ok := want[c.ID]; !ok || !sameTime(c.RunningSince, w) {
			t.Fatalf("%s: %v, want %v", c.ID, c.RunningSince, w)
		}
	}
	cell := func(at *time.Time) string {
		if at == nil {
			return ""
		}
		return at.Format(time.RFC3339)
	}
	rows := exportRows(t, mustExport(t, items, targets, ViewCVE, Filter{}), Filter{})
	if len(rows) != 3 {
		t.Fatalf("rows %v", rows)
	}
	for _, r := range rows {
		if r["running_since"] != cell(want[r["cve"]]) {
			t.Fatalf("CVE %s: %q, want %q", r["cve"], r["running_since"], cell(want[r["cve"]]))
		}
	}
}
