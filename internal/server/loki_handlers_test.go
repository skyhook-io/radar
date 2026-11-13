package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/connections"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/loki"
	"github.com/skyhook-io/radar/pkg/prom"
)

func fakeLokiServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/loki/api/v1/labels" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":["namespace","pod","container"]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func putLoki(t *testing.T, s *Server, url string, headers map[string]string) (*httptest.ResponseRecorder, localConnectionResponse) {
	t.Helper()
	target, err := k8s.CurrentProfileTarget()
	if err != nil {
		t.Fatal(err)
	}
	body := connections.Update{Target: target, Revision: s.localConnections.Resolve(target, config.IntegrationLoki, false).View.Revision, Kind: config.IntegrationLoki, Action: "save", URL: &url}
	for k, v := range headers {
		body.Headers = append(body.Headers, prom.HeaderOperation{Key: k, Action: "set", Value: v})
	}
	data, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	s.handleUpdateLocalConnection(rec, httptest.NewRequest(http.MethodPut, "/api/integrations/connections", strings.NewReader(string(data))))
	var out localConnectionResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func TestLokiSaveThenProbe(t *testing.T) {
	t.Setenv("RADAR_CLOUD_MODE", "false")
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	s := setupLocalProfileTest(t)
	t.Cleanup(func() { loki.Retire(nil) })
	srv := fakeLokiServer(t)

	rec, got := putLoki(t, s, srv.URL, nil)
	if rec.Code != http.StatusOK || !got.Connected || !got.Checked || got.Error != "" {
		t.Fatalf("live Loki: %d %s", rec.Code, rec.Body.String())
	}

	rec, got = putLoki(t, s, "http://127.0.0.1:1", nil)
	if rec.Code != http.StatusOK || got.Connected || !got.Checked || got.Error != "Saved, but Loki could not be reached. Check its URL, headers and network access." {
		t.Fatalf("dead Loki: %d %s", rec.Code, rec.Body.String())
	}
	target, _ := k8s.CurrentProfileTarget()
	if s.localConnections.Resolve(target, config.IntegrationLoki, false).View.URL != "http://127.0.0.1:1" {
		t.Fatal("an unreachable Loki was not saved")
	}
}

func TestLokiStatusRedacts(t *testing.T) {
	t.Cleanup(func() { loki.Retire(nil) })
	srv := fakeLokiServer(t)
	loki.Configure("http://user:pw@"+strings.TrimPrefix(srv.URL, "http://"), map[string]string{"Authorization": "Bearer zzz"})
	rec := httptest.NewRecorder()
	(&Server{}).handleLokiStatus(rec, httptest.NewRequest(http.MethodGet, "/api/loki/status", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"configured":true`) {
		t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
	}
	for _, leak := range []string{"pw", "zzz"} {
		if strings.Contains(rec.Body.String(), leak) {
			t.Fatalf("status leaks %q: %s", leak, rec.Body.String())
		}
	}
}

// A shared local listener without auth: a saved Loki's headers go only to the
// server they were saved for, and a cross-site page can't read the status.
func TestLokiSettingsOnSharedLocalListener(t *testing.T) {
	t.Setenv("RADAR_CLOUD_MODE", "false")
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	s := setupLocalProfileTest(t)
	s.listenAddress = "0.0.0.0:9280"
	t.Cleanup(func() { loki.Retire(nil) })
	srv := fakeLokiServer(t)
	if rec, _ := putLoki(t, s, srv.URL, map[string]string{"X-Scope-OrgID": "tenant-a"}); rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	before, _, _ := s.localConnections.Store.Read()
	if rec, _ := putLoki(t, s, "http://elsewhere.example:3100", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("moving saved headers to another server: %d %s", rec.Code, rec.Body.String())
	}
	if after, _, _ := s.localConnections.Store.Read(); !jsonEqual(before, after) {
		t.Fatal("the refused change altered the file")
	}
	req, _ := http.NewRequest(http.MethodGet, testServer.URL+"/api/loki/status", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site status read = %d", resp.StatusCode)
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// fakeLokiWithLogs answers labels and query_range, recording every query.
type lokiRecorder struct {
	srv     *httptest.Server
	queries []url.Values
	mu      sync.Mutex
	status  int
	body    string
}

func newLokiRecorder(t *testing.T, streams string) *lokiRecorder {
	t.Helper()
	return newLokiRecorderWithLabels(t, `["namespace","pod","container"]`, streams)
}

func newLokiRecorderWithLabels(t *testing.T, labelNames, streams string) *lokiRecorder {
	t.Helper()
	rec := &lokiRecorder{status: 200, body: streams}
	rec.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/loki/api/v1/labels":
			_, _ = w.Write([]byte(`{"status":"success","data":` + labelNames + `}`))
		case "/loki/api/v1/query_range":
			rec.mu.Lock()
			rec.queries = append(rec.queries, r.URL.Query())
			status, body := rec.status, rec.body
			rec.mu.Unlock()
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(rec.srv.Close)
	t.Cleanup(func() { loki.Retire(nil) })
	allowRadarPodLogs(t)
	loki.Configure(rec.srv.URL, map[string]string{"X-Scope-OrgID": "tenant-secret"})
	if err := loki.EnsureConnected(t.Context()); err != nil {
		t.Fatal(err)
	}
	return rec
}

func (r *lokiRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.queries)
}

const twoContainers = `{"status":"success","data":{"resultType":"streams","result":[
 {"stream":{"namespace":"team","pod":"app-1","container":"web"},"values":[["1763035201000000000","fourth"],["1763035200500000000","third-b"],["1763035200500000000","third-a"],["1763035200000000002","second"],["1763035200000000001","first"]]},
 {"stream":{"namespace":"team","pod":"app-1","container":"sidecar"},"values":[["1763035200000000003","side"]]}]}}`

func lokiPodLogs(t *testing.T, query string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(testServer.URL + "/api/loki/pods/team/app-1/logs" + query)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, string(raw)
}

func TestLokiPodLogsHappyPath(t *testing.T) {
	rec := newLokiRecorder(t, twoContainers)
	resp, raw := lokiPodLogs(t, "?since=1h")
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
	var got lokiPodLogsResponse
	_ = json.Unmarshal([]byte(raw), &got)
	if got.Source != "loki" || !slices.Equal(got.Containers, []string{"sidecar", "web"}) || got.Truncated {
		t.Fatalf("response = %+v", got)
	}
	web := strings.Split(strings.TrimSpace(got.Logs["web"]), "\n")
	want := []string{"first", "second", "third-a", "third-b", "fourth"}
	if len(web) != len(want) {
		t.Fatalf("web lines = %q", web)
	}
	for i, line := range web {
		// Fixed-width times keep text order equal to time order, even on a whole second.
		if ts, text, _ := strings.Cut(line, " "); len(ts) != len("2025-11-13T12:00:00.000000000Z") || text != want[i] {
			t.Fatalf("line %d = %q, want text %q with a fixed-width time", i, line, want[i])
		}
	}
	q := rec.queries[0]
	if q.Get("query") != `{namespace="team", pod="app-1"}` || q.Get("direction") != "backward" || q.Get("limit") != "5000" {
		t.Fatalf("query = %v", q)
	}
	if strings.Contains(raw, "tenant-secret") {
		t.Fatal("response leaks a header value")
	}
}

func TestLokiPodLogsContainerAndTruncated(t *testing.T) {
	rec := newLokiRecorder(t, twoContainers)
	resp, raw := lokiPodLogs(t, "?container=web&limit=3")
	var got lokiPodLogsResponse
	_ = json.Unmarshal([]byte(raw), &got)
	if resp.StatusCode != 200 || !got.Truncated || got.Limit != 3 {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
	if q := rec.queries[0]; q.Get("query") != `{namespace="team", pod="app-1", container="web"}` || q.Get("limit") != "3" {
		t.Fatalf("query = %v", q)
	}
}

func TestLokiPodLogsDeniedBeforeQuery(t *testing.T) {
	rec := newLokiRecorder(t, twoContainers)
	env := newAuthTestServer(t)
	env.srv.permCache.Set("alice", nil, &auth.UserPermissions{AllowedNamespaces: []string{"team"}})
	resp := env.authGet(t, "/api/loki/pods/team/app-1/logs", "alice", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || rec.count() != 0 {
		t.Fatalf("status %d, %d Loki queries", resp.StatusCode, rec.count())
	}
}

func TestLokiPodLogsWindowRejected(t *testing.T) {
	rec := newLokiRecorder(t, twoContainers)
	if resp, raw := lokiPodLogs(t, "?since=30d"); resp.StatusCode != http.StatusBadRequest || rec.count() != 0 {
		t.Fatalf("%d %s, %d queries", resp.StatusCode, raw, rec.count())
	}
}

func TestLokiPodLogsNotConnected(t *testing.T) {
	allowRadarPodLogs(t)
	loki.Retire(nil)
	if resp, raw := lokiPodLogs(t, ""); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
}

func TestLokiPodLogsUpstreamError(t *testing.T) {
	rec := newLokiRecorder(t, "")
	rec.status, rec.body = 400, "max entries limit per query exceeded (tenant-secret)"
	resp, raw := lokiPodLogs(t, "")
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(raw, "max entries limit per query exceeded") || strings.Contains(raw, "tenant-secret") {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
}

// The pod need not exist: History reads pods Kubernetes no longer holds.
func TestLokiPodLogsGonePod(t *testing.T) {
	newLokiRecorder(t, twoContainers)
	resp, err := http.Get(testServer.URL + "/api/loki/pods/team/long-gone/logs")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestLokiPodLogsSharedListener(t *testing.T) {
	rec := newLokiRecorder(t, twoContainers)
	req, _ := http.NewRequest(http.MethodGet, testServer.URL+"/api/loki/pods/team/app-1/logs?since=1h&query=%7Bx%3D%22y%22%7D", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || rec.count() != 0 {
		t.Fatalf("cross-site: %d, %d queries", resp.StatusCode, rec.count())
	}
	if resp, raw := lokiPodLogs(t, "?since=1h&query=%7Bx%3D%22y%22%7D&direction=forward"); resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
	if rec.count() != 1 || rec.queries[0].Get("query") != `{namespace="team", pod="app-1"}` || rec.queries[0].Get("direction") != "backward" {
		t.Fatalf("a caller's parameter reached Loki: %v", rec.queries)
	}
}

// Without authentication History may read no more than Radar's own identity:
// Loki doesn't ask Kubernetes, so Radar must.
func TestLokiPodLogsNoAuthNeedsRadarsOwnAccess(t *testing.T) {
	rec := newLokiRecorder(t, twoContainers)
	radarCanReadPodLogs = func(_ context.Context, ns string) bool { return ns != "kube-system" }
	resp, err := http.Get(testServer.URL + "/api/loki/pods/kube-system/etcd-0/logs")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || rec.count() != 0 {
		t.Fatalf("status %d, %d queries", resp.StatusCode, rec.count())
	}
}

func TestLokiPodLogsHonourNamespaceScope(t *testing.T) {
	rec := newLokiRecorder(t, twoContainers)
	k8s.ForceNamespaceScope = true
	k8s.SetFallbackNamespace("other")
	t.Cleanup(func() {
		k8s.ForceNamespaceScope = false
		k8s.SetFallbackNamespace("")
	})
	if resp, raw := lokiPodLogs(t, ""); resp.StatusCode != http.StatusForbidden || rec.count() != 0 {
		t.Fatalf("outside --namespace-scope: %d %s, %d queries", resp.StatusCode, raw, rec.count())
	}
}

// An answer over the size cap is Loki answering, not Loki gone: the
// connection stays.
func TestLokiPodLogsTooLargeKeepsTheConnection(t *testing.T) {
	rec := newLokiRecorder(t, `{"status":"success","data":{"resultType":"streams","result":[{"stream":{"pod":"app-1"},"values":[["1","`+strings.Repeat("x", 33<<20)+`"]]}]}}`)
	resp, raw := lokiPodLogs(t, "")
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(raw, "shorter window") {
		t.Fatalf("%d %.200s", resp.StatusCode, raw)
	}
	if !loki.CurrentStatus().Connected {
		t.Fatal("a large answer dropped the connection")
	}
	_ = rec
}

// allowRadarPodLogs stands in for Radar's own access review, which the fake
// clientset would refuse.
func allowRadarPodLogs(t *testing.T) {
	t.Helper()
	saved := radarCanReadPodLogs
	radarCanReadPodLogs = func(context.Context, string) bool { return true }
	t.Cleanup(func() { radarCanReadPodLogs = saved })
}
