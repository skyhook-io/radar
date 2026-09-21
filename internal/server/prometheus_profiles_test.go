package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/connectionruntime"
	"github.com/skyhook-io/radar/internal/connections"
	"github.com/skyhook-io/radar/internal/k8s"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/pkg/prom"
	"k8s.io/client-go/rest"
)

func setupLocalProfileTest(t *testing.T) *Server {
	s := setupPrometheusIntegrationTest(t, nil)
	t.Cleanup(k8s.SetTestLocalMode())
	connection := k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	t.Cleanup(func() { k8s.SetConnectionStatus(connection) })
	t.Cleanup(k8s.SetTestProfileSource("/fixture/team", "dev", "developer"))
	old := k8s.SetTestConfig(&rest.Config{Host: "https://kubernetes"})
	t.Cleanup(func() { k8s.SetTestConfig(old) })
	target, err := k8s.CurrentProfileTarget()
	if err != nil {
		t.Fatal(err)
	}
	s.localConnections = connections.NewResolver(config.NewProfileStore(), target, nil)
	s.localRuntime = connectionruntime.New(s.localConnections, nil)
	connections.RegisterRefresh(s.localRuntime.Refresh)
	t.Cleanup(func() { connections.RegisterRefresh(nil) })
	t.Cleanup(prometheuspkg.Retire)
	return s
}

func updateProfile(t *testing.T, s *Server, view connections.ProfileView, action, url string, headers *map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	if action == "apply" {
		action = "save"
	}
	if action == "finish_migration" {
		action = "dismiss_legacy"
	}
	body := connections.Update{Target: view.Target, Revision: view.Revision, Kind: config.IntegrationMetrics, Action: action, URL: &url, ConfirmRemoval: true}
	if headers != nil {
		for _, key := range view.HeaderKeys {
			if _, exists := (*headers)[key]; !exists {
				body.Headers = append(body.Headers, prom.HeaderOperation{Key: key, Action: "clear"})
			}
		}
		for key, value := range *headers {
			body.Headers = append(body.Headers, prom.HeaderOperation{Key: key, Action: "set", Value: value})
		}
	}
	if view.Legacy != nil {
		body.LegacyRevision = view.Legacy.Revision
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	s.handleUpdateLocalConnection(response, httptest.NewRequest(http.MethodPut, "/api/integrations/connections", strings.NewReader(string(data))))
	return response
}

func (s *Server) localPrometheusView() connections.ProfileView {
	target, _ := k8s.CurrentProfileTarget()
	return s.localRuntime.Apply(target, true)[config.IntegrationMetrics].View
}

func TestLocalProfileSaveConflictAndCredentialProtection(t *testing.T) {
	s := setupLocalProfileTest(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "test-secret" {
			t.Error("missing profile credential")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer backend.Close()
	view := s.localPrometheusView()
	headers := map[string]string{"Authorization": "test-secret"}
	res := updateProfile(t, s, view, "apply", backend.URL, &headers)
	if res.Code != 200 || strings.Contains(res.Body.String(), "test-secret") {
		t.Fatalf("save: %d %s", res.Code, res.Body.String())
	}
	if res := updateProfile(t, s, view, "apply", backend.URL, nil); res.Code != 409 {
		t.Fatalf("stale revision: %d %s", res.Code, res.Body.String())
	}
	view = s.localPrometheusView()
	if res := updateProfile(t, s, view, "apply", "https://different.example", nil); res.Code != 400 {
		t.Fatalf("cross-origin credentials: %d %s", res.Code, res.Body.String())
	}
	view.Target.OperationGeneration++
	if res := updateProfile(t, s, view, "apply", backend.URL, nil); res.Code != 409 {
		t.Fatalf("stale cluster generation: %d %s", res.Code, res.Body.String())
	}
	if c := config.Load(); c.PrometheusURL != "" || len(c.PrometheusHeaders) > 0 {
		t.Fatal("profile leaked into global config")
	}
}

func TestLocalProfileLegacyAdoptionAndCompletion(t *testing.T) {
	s := setupLocalProfileTest(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer backend.Close()
	_, err := config.Update(func(c *config.Config) {
		c.PrometheusURL = backend.URL
		c.PrometheusHeaders = map[string]string{"Authorization": "legacy-secret"}
	})
	if err != nil {
		t.Fatal(err)
	}
	view := s.localPrometheusView()
	if view.Legacy == nil || len(prometheuspkg.CurrentHeaders()) != 0 {
		t.Fatal("legacy credentials activated without adoption")
	}
	res := updateProfile(t, s, view, "adopt", "unfinished-url", nil)
	if res.Code != 200 || strings.Contains(res.Body.String(), "legacy-secret") {
		t.Fatalf("adopt: %d %s", res.Code, res.Body.String())
	}
	if s.localPrometheusView().Legacy != nil {
		t.Fatal("adoption offered twice")
	}
	restore := k8s.SetTestProfileSource("/fixture/team", "second", "developer")
	defer restore()
	view = s.localPrometheusView()
	if view.Legacy != nil || len(prometheuspkg.CurrentHeaders()) != 0 {
		t.Fatal("second context repeated import offer or inherited credentials")
	}
	if res := updateProfile(t, s, view, "finish_migration", "unfinished-url", nil); res.Code != 200 {
		t.Fatalf("finish: %d %s", res.Code, res.Body.String())
	}
	if s.localPrometheusView().Legacy != nil {
		t.Fatal("global completion did not stop offers")
	}
	if config.Load().PrometheusHeaders["Authorization"] != "legacy-secret" {
		t.Fatal("legacy recovery copy changed")
	}
}

func TestLocalProfileChangedTargetCanReplaceWithoutAdoption(t *testing.T) {
	s := setupLocalProfileTest(t)
	view := s.localPrometheusView()
	if res := updateProfile(t, s, view, "apply", "http://127.0.0.1:1", nil); res.Code != 200 {
		t.Fatalf("initial save: %d", res.Code)
	}
	old := k8s.SetTestConfig(&rest.Config{Host: "https://different-cluster"})
	defer k8s.SetTestConfig(old)
	view = s.localPrometheusView()
	if view.State != "target_changed" {
		t.Fatalf("expected suspension: %+v", view)
	}
	empty := map[string]string{}
	if res := updateProfile(t, s, view, "replace", "", &empty); res.Code != 200 {
		t.Fatalf("replace: %d %s", res.Code, res.Body.String())
	}
	if s.localPrometheusView().State != "saved" {
		t.Fatal("replacement did not record new target")
	}
}

func TestLocalProfileReadWhileDisconnectedDoesNotActivate(t *testing.T) {
	s := setupLocalProfileTest(t)
	prometheuspkg.Retire()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateDisconnected})
	view := s.localPrometheusView()
	if view.State != "auto" || prometheuspkg.GetClient() != nil {
		t.Fatalf("offline read activated metrics: %+v", view)
	}
}

func TestLocalProfileEnvironmentOriginGuidance(t *testing.T) {
	s := setupLocalProfileTest(t)
	t.Setenv("PROFILE_TEST_TOKEN", "synthetic-secret")
	target, err := k8s.CurrentProfileTarget()
	if err != nil {
		t.Fatal(err)
	}
	_, rev, _ := s.localConnections.Store.Read()
	_, err = s.localConnections.Store.Update(context.Background(), rev, func(file *config.ClusterProfiles) error {
		file.Connections["env"] = config.SavedConnection{Type: config.IntegrationMetrics, Prometheus: &prom.Connection{URL: "https://original.example", HeadersFromEnv: map[string]string{"Authorization": "PROFILE_TEST_TOKEN"}}}
		file.Profiles[target.Binding] = config.ClusterProfile{Context: target.Context, Integrations: map[config.Integration]config.IntegrationAssignment{config.IntegrationMetrics: {Mode: "connection", Target: target.Fingerprint, ConnectionID: "env"}}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	res := updateProfile(t, s, s.localPrometheusView(), "apply", "https://different.example", nil)
	if res.Code != 400 || !strings.Contains(res.Body.String(), "environment references in clusters.json") || strings.Contains(res.Body.String(), "synthetic-secret") {
		t.Fatalf("guidance: %d %s", res.Code, res.Body.String())
	}
}

func TestLocalProfileSupersededProbeDoesNotReportConnected(t *testing.T) {
	s := setupLocalProfileTest(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(entered) })
		<-release
		w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer first.Close()
	defer close(release)
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer second.Close()
	initial := s.localPrometheusView()
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() { result <- updateProfile(t, s, initial, "apply", first.URL, nil) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first probe did not start")
	}
	res := updateProfile(t, s, s.localPrometheusView(), "apply", second.URL, nil)
	if res.Code != 200 {
		t.Fatalf("second save: %d %s", res.Code, res.Body.String())
	}
	select {
	case res := <-result:
		var body struct {
			Connected bool
			Error     string
		}
		if err := json.Unmarshal(res.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Connected || !strings.Contains(body.Error, "Settings changed during the connection check") {
			t.Fatalf("stale probe: %s", res.Body.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("superseded probe did not finish")
	}
}
