package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
