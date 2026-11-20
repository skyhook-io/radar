package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
)

var (
	jobStart = time.Date(2026, 11, 13, 2, 0, 0, 0, time.UTC)
	jobEnd   = jobStart.Add(10 * time.Minute)
)

// seedJob puts one finished Job (nightly-29905990 in team) in the cache.
func seedJob(t *testing.T) {
	t.Helper()
	seedJobWithSpec(t, batchv1.JobSpec{})
}

func seedJobWithSpec(t *testing.T, spec batchv1.JobSpec) {
	t.Helper()
	restoreFixtureCache(t)
	k8s.ResetResourceCache()
	start, end := metav1.NewTime(jobStart), metav1.NewTime(jobEnd)
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "nightly-29905990"},
		Spec:       spec,
		Status:     batchv1.JobStatus{StartTime: &start, CompletionTime: &end},
	}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team"}}
	if err := k8s.InitTestResourceCache(fake.NewClientset(job, ns)); err != nil {
		t.Fatal(err)
	}
}

const twoPods = `{"status":"success","data":{"resultType":"streams","result":[
 {"stream":{"namespace":"team","pod":"nightly-29905990-aaaaa","container":"job"},"values":[["1763002800000000002","b"],["1763002800000000001","a"]]},
 {"stream":{"namespace":"team","pod":"nightly-29905990-bbbbb","container":"job"},"values":[["1763002800000000003","c"]]}]}}`

func lokiJobLogs(t *testing.T, path string) (*http.Response, lokiJobLogsResponse, string) {
	t.Helper()
	resp, err := http.Get(testServer.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var got lokiJobLogsResponse
	_ = json.Unmarshal(raw, &got)
	return resp, got, string(raw)
}

func TestLokiJobLogsGroupsByPod(t *testing.T) {
	seedJob(t)
	rec := newLokiRecorder(t, twoPods)
	resp, got, raw := lokiJobLogs(t, "/api/loki/jobs/team/nightly-29905990/logs")
	if resp.StatusCode != 200 || got.Source != "loki" || len(got.Pods) != 2 || got.Pods[0].Name != "nightly-29905990-aaaaa" || len(got.Logs) != 3 {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
	if got.Logs[0].Content != "a" || got.Logs[1].Content != "b" || got.Logs[2].Content != "c" || got.Logs[0].Pod != "nightly-29905990-aaaaa" {
		t.Fatalf("logs = %+v", got.Logs)
	}
	if q := rec.queries[0].Get("query"); q != `{namespace="team", pod=~"^nightly-29905990-[a-z0-9]{5}$"}` {
		t.Fatalf("query = %s", q)
	}
}

func TestLokiJobLogsUsesJobLabel(t *testing.T) {
	seedJob(t)
	rec := newLokiRecorderWithLabels(t, `["namespace","pod","container","job_name"]`, twoPods)
	if resp, _, raw := lokiJobLogs(t, "/api/loki/jobs/team/nightly-29905990/logs"); resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
	if q := rec.queries[0].Get("query"); q != `{namespace="team", job_name="nightly-29905990"}` {
		t.Fatalf("query = %s", q)
	}
}

func TestLokiJobLogsWindowFromJob(t *testing.T) {
	seedJob(t)
	rec := newLokiRecorder(t, twoPods)
	if resp, got, raw := lokiJobLogs(t, "/api/loki/jobs/team/nightly-29905990/logs"); resp.StatusCode != 200 || !got.Window.Start.Equal(jobStart.Add(-time.Minute)) || !got.Window.End.Equal(jobEnd.Add(time.Minute)) {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
	q := rec.queries[0]
	if q.Get("start") != strconv.FormatInt(jobStart.Add(-time.Minute).UnixNano(), 10) || q.Get("end") != strconv.FormatInt(jobEnd.Add(time.Minute).UnixNano(), 10) {
		t.Fatalf("window = %s..%s", q.Get("start"), q.Get("end"))
	}
}

func TestLokiJobLogsDenied(t *testing.T) {
	seedJob(t)
	rec := newLokiRecorder(t, twoPods)
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"team"}}
	perms.SetCanI("get", "batch", "jobs", "team", true)
	perms.SetCanI("list", "", "pods", "team", true)
	env.srv.permCache.Set("alice", nil, perms)
	resp := env.authGet(t, "/api/loki/jobs/team/nightly-29905990/logs", "alice", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || rec.count() != 0 {
		t.Fatalf("without get pods/log: %d, %d queries", resp.StatusCode, rec.count())
	}
}

func TestLokiJobLogsJobGone(t *testing.T) {
	seedJob(t)
	rec := newLokiRecorder(t, twoPods)
	if resp, _, raw := lokiJobLogs(t, "/api/loki/jobs/team/long-gone/logs"); resp.StatusCode != http.StatusNotFound || rec.count() != 0 {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
}

func TestLokiJobLogsSharedListener(t *testing.T) {
	seedJob(t)
	rec := newLokiRecorder(t, twoPods)
	req, _ := http.NewRequest(http.MethodGet, testServer.URL+"/api/loki/jobs/team/nightly-29905990/logs", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || rec.count() != 0 {
		t.Fatalf("cross-site: %d, %d queries", resp.StatusCode, rec.count())
	}
	// Without auth, Radar's own identity bounds Job runs too.
	radarCanReadPodLogs = func(context.Context, string) bool { return false }
	if resp, _, raw := lokiJobLogs(t, "/api/loki/jobs/team/nightly-29905990/logs"); resp.StatusCode != http.StatusForbidden || rec.count() != 0 {
		t.Fatalf("no-auth without Radar's access: %d %s", resp.StatusCode, raw)
	}
}

func TestLokiJobLogsMarksInitAndSidecarsInTemplateOrder(t *testing.T) {
	always := corev1.ContainerRestartPolicyAlways
	seedJobWithSpec(t, batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
		InitContainers: []corev1.Container{{Name: "proxy", RestartPolicy: &always}, {Name: "migrate"}},
		Containers:     []corev1.Container{{Name: "job"}},
	}}})
	newLokiRecorder(t, `{"status":"success","data":{"resultType":"streams","result":[
 {"stream":{"namespace":"team","pod":"nightly-29905990-aaaaa","container":"job"},"values":[["1763002800000000003","c"]]},
 {"stream":{"namespace":"team","pod":"nightly-29905990-aaaaa","container":"extra"},"values":[["1763002800000000004","d"]]},
 {"stream":{"namespace":"team","pod":"nightly-29905990-aaaaa","container":"migrate"},"values":[["1763002800000000001","a"]]},
 {"stream":{"namespace":"team","pod":"nightly-29905990-aaaaa","container":"proxy"},"values":[["1763002800000000002","b"]]}]}}`)
	resp, got, raw := lokiJobLogs(t, "/api/loki/jobs/team/nightly-29905990/logs")
	if resp.StatusCode != 200 || len(got.Pods) != 1 {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
	p := got.Pods[0]
	if want := []string{"proxy", "migrate", "job", "extra"}; strings.Join(p.Containers, ",") != strings.Join(want, ",") {
		t.Fatalf("containers = %v, want %v", p.Containers, want)
	}
	roles := map[string]string{}
	for _, cs := range p.ContainerStatuses {
		roles[cs.Name] = map[bool]string{true: "init"}[cs.Init] + map[bool]string{true: "+sidecar"}[cs.Sidecar]
	}
	if roles["proxy"] != "init+sidecar" || roles["migrate"] != "init" || roles["job"] != "" || len(roles) != 3 {
		t.Fatalf("roles = %v (%s)", roles, raw)
	}
}

// The chosen container is read from Loki, not filtered after Loki's line
// limit; a multi-line entry becomes its lines at the entry's time.
func TestLokiJobLogsContainerAndMultiLine(t *testing.T) {
	seedJob(t)
	rec := newLokiRecorder(t, `{"status":"success","data":{"resultType":"streams","result":[
 {"stream":{"namespace":"team","pod":"nightly-29905990-aaaaa","container":"job"},"values":[["1763002800000000001","boom\n\tat x"]]}]}}`)
	resp, got, raw := lokiJobLogs(t, "/api/loki/jobs/team/nightly-29905990/logs?container=job")
	if resp.StatusCode != 200 || len(got.Logs) != 2 || got.Logs[0].Content != "boom" || got.Logs[1].Content != "\tat x" || got.Logs[0].Timestamp != got.Logs[1].Timestamp {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
	if q := rec.queries[0].Get("query"); q != `{namespace="team", pod=~"^nightly-29905990-[a-z0-9]{5}$", container="job"}` {
		t.Fatalf("query = %s", q)
	}
}

// A Job run read shares the per-user limit with pod History.
func TestLokiJobLogsShareTheReadLimit(t *testing.T) {
	seedJob(t)
	rec := newLokiRecorder(t, twoPods)
	hold := make(chan struct{})
	rec.mu.Lock()
	rec.hold = hold
	rec.mu.Unlock()
	first := make(chan int)
	go func() {
		resp, _ := lokiPodLogs(t, "")
		first <- resp.StatusCode
	}()
	for rec.count() == 0 {
		time.Sleep(time.Millisecond)
	}
	resp, _, raw := lokiJobLogs(t, "/api/loki/jobs/team/nightly-29905990/logs")
	close(hold)
	<-first
	if resp.StatusCode != http.StatusTooManyRequests || rec.count() != 1 {
		t.Fatalf("%d %s, %d queries", resp.StatusCode, raw, rec.count())
	}
}

func seedJobStatus(t *testing.T, status batchv1.JobStatus) {
	t.Helper()
	restoreFixtureCache(t)
	k8s.ResetResourceCache()
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "nightly-29905990"}, Status: status}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team"}}
	if err := k8s.InitTestResourceCache(fake.NewClientset(job, ns)); err != nil {
		t.Fatal(err)
	}
}

// A Job that never started has no window to read: 409, Loki untouched.
func TestLokiJobLogsNeverStarted(t *testing.T) {
	seedJobStatus(t, batchv1.JobStatus{})
	rec := newLokiRecorder(t, twoPods)
	if resp, _, raw := lokiJobLogs(t, "/api/loki/jobs/team/nightly-29905990/logs"); resp.StatusCode != http.StatusConflict || rec.count() != 0 {
		t.Fatalf("%d %s, %d queries", resp.StatusCode, raw, rec.count())
	}
}

// A failed Job has no completion time: the run ends at its last Failed condition.
func TestLokiJobLogsWindowEndsAtFailure(t *testing.T) {
	start := metav1.NewTime(jobStart)
	failed := metav1.NewTime(jobStart.Add(4 * time.Minute))
	seedJobStatus(t, batchv1.JobStatus{StartTime: &start, Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, LastTransitionTime: failed}}})
	newLokiRecorder(t, twoPods)
	resp, got, raw := lokiJobLogs(t, "/api/loki/jobs/team/nightly-29905990/logs")
	if resp.StatusCode != 200 || !got.Window.Start.Equal(jobStart.Add(-time.Minute)) || !got.Window.End.Equal(failed.Add(time.Minute)) {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
}

// An old run longer than 7 days reads the newest 7 days of its own span.
func TestLokiJobLogsLongRunCappedFromItsEnd(t *testing.T) {
	start := metav1.NewTime(time.Now().Add(-30 * 24 * time.Hour))
	end := metav1.NewTime(start.Add(10 * 24 * time.Hour))
	seedJobStatus(t, batchv1.JobStatus{StartTime: &start, CompletionTime: &end})
	newLokiRecorder(t, twoPods)
	resp, got, raw := lokiJobLogs(t, "/api/loki/jobs/team/nightly-29905990/logs")
	if resp.StatusCode != 200 || !got.Window.End.Equal(end.Add(time.Minute)) || got.Window.End.Sub(got.Window.Start) != 7*24*time.Hour {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
}

func TestLokiJobLogsHonourNamespaceScope(t *testing.T) {
	seedJob(t)
	rec := newLokiRecorder(t, twoPods)
	k8s.ForceNamespaceScope = true
	k8s.SetFallbackNamespace("other")
	t.Cleanup(func() {
		k8s.ForceNamespaceScope = false
		k8s.SetFallbackNamespace("")
	})
	if resp, _, raw := lokiJobLogs(t, "/api/loki/jobs/team/nightly-29905990/logs"); resp.StatusCode != http.StatusForbidden || rec.count() != 0 {
		t.Fatalf("outside --namespace-scope: %d %s, %d queries", resp.StatusCode, raw, rec.count())
	}
}

// A container value with quotes and backslashes stays one quoted value.
func TestLokiJobLogsHostileContainer(t *testing.T) {
	seedJob(t)
	rec := newLokiRecorder(t, twoPods)
	if resp, _, raw := lokiJobLogs(t, `/api/loki/jobs/team/nightly-29905990/logs?container=%22%7D%20or%20%7Bnamespace%3D~%22.%2B%5C`); resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
	want := `{namespace="team", pod=~"^nightly-29905990-[a-z0-9]{5}$", container="\"} or {namespace=~\".+\\"}`
	if q := rec.queries[0].Get("query"); q != want {
		t.Fatalf("query = %s\nwant    %s", q, want)
	}
}

// With a container chosen (or Loki's line limit filled by one container) the
// answer still lists every template container, so the viewer can switch.
func TestLokiJobLogsListsTemplateContainersWithoutLines(t *testing.T) {
	always := corev1.ContainerRestartPolicyAlways
	seedJobWithSpec(t, batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
		InitContainers: []corev1.Container{{Name: "setup"}, {Name: "proxy", RestartPolicy: &always}},
		Containers:     []corev1.Container{{Name: "work"}},
	}}})
	newLokiRecorder(t, `{"status":"success","data":{"resultType":"streams","result":[
 {"stream":{"namespace":"team","pod":"nightly-29905990-aaaaa","container":"work"},"values":[["1763002800000000001","a"]]}]}}`)
	resp, got, raw := lokiJobLogs(t, "/api/loki/jobs/team/nightly-29905990/logs?container=work")
	if resp.StatusCode != 200 || len(got.Pods) != 1 || strings.Join(got.Pods[0].Containers, ",") != "setup,proxy,work" || len(got.Pods[0].ContainerStatuses) != 3 {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
}

// Without a container label Loki can't split a pod: the answer keeps the
// one whole-pod entry instead of offering containers it can't read alone.
func TestLokiJobLogsNoContainerLabelKeepsWholePod(t *testing.T) {
	seedJobWithSpec(t, batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "work"}, {Name: "side"}}}}})
	newLokiRecorderWithLabels(t, `["namespace","pod"]`, `{"status":"success","data":{"resultType":"streams","result":[
 {"stream":{"namespace":"team","pod":"nightly-29905990-aaaaa"},"values":[["1763002800000000001","a"]]}]}}`)
	resp, got, raw := lokiJobLogs(t, "/api/loki/jobs/team/nightly-29905990/logs")
	if resp.StatusCode != 200 || len(got.Pods) != 1 || strings.Join(got.Pods[0].Containers, ",") != noContainerLabel {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
}

const noStreams = `{"status":"success","data":{"resultType":"streams","result":[]}}`

// A Loki fed by several shippers has a Job label on some streams only: a run
// whose streams lack it is read again by pod name, and the notice says so.
func TestLokiJobLogsFallsBackToNameMatching(t *testing.T) {
	seedJob(t)
	rec := newLokiRecorderWithLabels(t, `["namespace","pod","container","job_name"]`, "")
	rec.mu.Lock()
	rec.byQuery = func(query string) string {
		if strings.Contains(query, "job_name=") {
			return noStreams
		}
		return twoPods
	}
	rec.mu.Unlock()
	resp, got, raw := lokiJobLogs(t, "/api/loki/jobs/team/nightly-29905990/logs?container=job")
	if resp.StatusCode != 200 || len(got.Logs) != 3 || rec.count() != 2 {
		t.Fatalf("%d %s, %d queries", resp.StatusCode, raw, rec.count())
	}
	if q := rec.queries[0].Get("query"); q != `{namespace="team", job_name="nightly-29905990", container="job"}` {
		t.Fatalf("first query = %s", q)
	}
	second := rec.queries[1]
	if q := second.Get("query"); q != `{namespace="team", pod=~"^nightly-29905990-[a-z0-9]{5}$", container="job"}` {
		t.Fatalf("second query = %s", q)
	}
	if second.Get("start") != rec.queries[0].Get("start") || second.Get("end") != rec.queries[0].Get("end") || second.Get("limit") != rec.queries[0].Get("limit") {
		t.Fatalf("the name read changed the bounds: %v vs %v", second, rec.queries[0])
	}
	if !strings.Contains(got.Notice, "Selected by pod name: no lines carry Loki's job_name label") {
		t.Fatalf("notice = %q", got.Notice)
	}
}

// A run the Job label finds is read once, and the notice names the label.
func TestLokiJobLogsJobLabelNamedInNotice(t *testing.T) {
	seedJob(t)
	rec := newLokiRecorderWithLabels(t, `["namespace","pod","container","job_name"]`, twoPods)
	if resp, got, raw := lokiJobLogs(t, "/api/loki/jobs/team/nightly-29905990/logs"); resp.StatusCode != 200 || rec.count() != 1 || got.Notice != "Selected by Loki's job_name label." {
		t.Fatalf("%d %s, %d queries", resp.StatusCode, raw, rec.count())
	}
}

// The Job label is looked up in the run's own window, so a shipper that
// started adding it after Radar connected is used.
func TestLokiJobLogsReadsLabelsInTheRunWindow(t *testing.T) {
	seedJob(t)
	rec := newLokiRecorder(t, twoPods)
	rec.mu.Lock()
	rec.labelNames = `["namespace","pod","container","job_name"]`
	rec.mu.Unlock()
	resp, got, raw := lokiJobLogs(t, "/api/loki/jobs/team/nightly-29905990/logs")
	if resp.StatusCode != 200 || len(rec.labelCalls) != 1 {
		t.Fatalf("%d %s, %d label reads", resp.StatusCode, raw, len(rec.labelCalls))
	}
	lc := rec.labelCalls[0]
	if lc.Get("start") != strconv.FormatInt(jobStart.Add(-time.Minute).UnixNano(), 10) || lc.Get("end") != strconv.FormatInt(jobEnd.Add(time.Minute).UnixNano(), 10) {
		t.Fatalf("label window = %s..%s", lc.Get("start"), lc.Get("end"))
	}
	if q := rec.queries[0].Get("query"); q != `{namespace="team", job_name="nightly-29905990"}` || !strings.Contains(got.Notice, "job_name label") {
		t.Fatalf("query = %s, notice %q", q, got.Notice)
	}
}

// Container support comes from the run's own streams, not the connection's
// recent label discovery: a run shipped before Loki labelled containers is
// read whole, with no container choices and no container matcher.
func TestLokiJobLogsContainerSupportFromTheRun(t *testing.T) {
	seedJobWithSpec(t, batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "work"}, {Name: "side"}}}}})
	rec := newLokiRecorder(t, `{"status":"success","data":{"resultType":"streams","result":[
 {"stream":{"namespace":"team","pod":"nightly-29905990-aaaaa"},"values":[["1763002800000000001","a"]]}]}}`)
	rec.mu.Lock()
	rec.labelNames = `["namespace","pod"]`
	rec.mu.Unlock()
	resp, got, raw := lokiJobLogs(t, "/api/loki/jobs/team/nightly-29905990/logs?container=work")
	if resp.StatusCode != 200 || len(got.Pods) != 1 || strings.Join(got.Pods[0].Containers, ",") != noContainerLabel {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
	if q := rec.queries[0].Get("query"); q != `{namespace="team", pod=~"^nightly-29905990-[a-z0-9]{5}$"}` {
		t.Fatalf("query = %s", q)
	}
}

// With mixed pipelines a pod whose streams carry no container label is read
// whole: it offers no template containers beside its whole-pod entry.
func TestLokiJobLogsWholePodStreamsOfferNoContainers(t *testing.T) {
	seedJobWithSpec(t, batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "work"}, {Name: "side"}}}}})
	newLokiRecorder(t, `{"status":"success","data":{"resultType":"streams","result":[
 {"stream":{"namespace":"team","pod":"nightly-29905990-aaaaa"},"values":[["1763002800000000001","a"]]},
 {"stream":{"namespace":"team","pod":"nightly-29905990-bbbbb","container":"work"},"values":[["1763002800000000002","b"]]}]}}`)
	resp, got, raw := lokiJobLogs(t, "/api/loki/jobs/team/nightly-29905990/logs")
	if resp.StatusCode != 200 || len(got.Pods) != 2 {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
	if a, b := strings.Join(got.Pods[0].Containers, ","), strings.Join(got.Pods[1].Containers, ","); a != noContainerLabel || b != "work,side" {
		t.Fatalf("containers = %q / %q", a, b)
	}
}
