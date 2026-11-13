package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
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
	restoreFixtureCache(t)
	k8s.ResetResourceCache()
	start, end := metav1.NewTime(jobStart), metav1.NewTime(jobEnd)
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "nightly-29905990"},
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
	if q := rec.queries[0].Get("query"); q != `{namespace="team", pod=~"^nightly-29905990-([0-9]+-)?[a-z0-9]{5}$"}` {
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
