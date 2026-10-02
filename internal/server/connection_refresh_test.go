package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/argocd"
	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/connections"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/opencost"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/pkg/prom"
	"k8s.io/client-go/rest"
)

func TestIntegrationOperationsRefreshExternalChangesWithoutSettings(t *testing.T) {
	s := setupLocalProfileTest(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/version":
			w.Write([]byte(`{"Version":"v3.0.0"}`))
		case "/api/v1/session/userinfo":
			if r.Header.Get("Authorization") != "Bearer rotated" {
				w.WriteHeader(401)
				return
			}
			w.Write([]byte(`{"loggedIn":true}`))
		case "/allocation", "/model/allocation":
			if r.Header.Get("X-API-KEY") != "rotated" {
				w.WriteHeader(401)
				return
			}
			w.Write([]byte(`{"code":200,"data":[{"cluster-a":{"properties":{"cluster":"cluster-a"}}}]}`))
		default:
			if r.Header.Get("Authorization") != "rotated" {
				w.WriteHeader(401)
				return
			}
			w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
		}
	}))
	defer backend.Close()
	target, err := k8s.CurrentProfileTarget()
	if err != nil {
		t.Fatal(err)
	}
	s.localRuntime.Apply(target, false)
	other := connections.NewResolver(s.localConnections.Store, target, nil)
	for _, kind := range config.IntegrationKinds {
		req := connections.Update{Target: target, Revision: other.Resolve(target, kind, false).View.Revision, Kind: kind, Action: "save", URL: &backend.URL}
		if kind == config.IntegrationMetrics {
			req.Headers = []prom.HeaderOperation{{Key: "Authorization", Action: "set", Value: "rotated"}}
		} else {
			req.Secret = &connections.SecretEdit{Action: "set", Value: "rotated"}
		}
		if kind == config.IntegrationCost {
			id := "cluster-a"
			req.ClusterID = &id
		}
		pending, err := other.Prepare(target, req)
		if err != nil {
			t.Fatal(err)
		}
		if err := other.Commit(context.Background(), pending); err != nil {
			t.Fatal(err)
		}
	}
	client, err := prometheuspkg.ClientForOperation()
	if err != nil {
		t.Fatal("metrics operation did not refresh")
	}
	if _, err := client.Query(context.Background(), "up"); err != nil {
		t.Fatal(err)
	}
	if err := argocd.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := opencost.Selected(context.Background()); err != nil {
		t.Fatal(err)
	}
	valid, err := os.ReadFile(s.localConnections.Store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.localConnections.Store.Path, []byte(`{"version":`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := prometheuspkg.ClientForOperation(); err == nil {
		t.Fatal("invalid file retained metrics client")
	}
	if err := argocd.Probe(context.Background()); err == nil {
		t.Fatal("invalid file retained Argo client")
	}
	if _, err := opencost.Selected(context.Background()); err == nil {
		t.Fatal("invalid file retained cost client")
	}
	if err := os.WriteFile(s.localConnections.Store.Path, valid, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := prometheuspkg.ClientForOperation(); err != nil {
		t.Fatal("repair did not reactivate metrics")
	}
	if err := argocd.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := opencost.Selected(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestLocalArgoTargetChangeStaysEditableAndReportsStatus(t *testing.T) {
	s := setupLocalProfileTest(t)
	t.Cleanup(func() { argocd.SetConfig("", "", false, true) })
	target, _ := k8s.CurrentProfileTarget()
	url := "https://argo.example"
	pending, err := s.localConnections.Prepare(target, connections.Update{Target: target, Revision: s.localConnections.Resolve(target, config.IntegrationArgoCD, false).View.Revision, Kind: config.IntegrationArgoCD, Action: "save", URL: &url, Secret: &connections.SecretEdit{Action: "set", Value: "saved-test-token"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.localConnections.Commit(context.Background(), pending); err != nil {
		t.Fatal(err)
	}
	old := k8s.SetTestConfig(&rest.Config{Host: "https://changed-cluster"})
	defer k8s.SetTestConfig(old)
	status := httptest.NewRecorder()
	s.handleArgoCDStatus(status, httptest.NewRequest(http.MethodGet, "/api/integrations/argocd/status", nil))
	var state struct {
		Configured bool   `json:"configured"`
		Connected  bool   `json:"connected"`
		Reason     string `json:"reason"`
	}
	if err := json.Unmarshal(status.Body.Bytes(), &state); err != nil || status.Code != http.StatusOK || !state.Configured || state.Connected || state.Reason == "" {
		t.Fatalf("expected actionable disconnected state: %d %s", status.Code, status.Body.String())
	}
	response := httptest.NewRecorder()
	s.handleGetConfig(response, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	var cfg struct {
		ArgoCDEnvManaged    bool                                           `json:"argoCdEnvManaged"`
		IntegrationProfiles map[config.Integration]connections.ProfileView `json:"integrationProfiles"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &cfg); err != nil {
		t.Fatal(err)
	}
	view := cfg.IntegrationProfiles[config.IntegrationArgoCD]
	if cfg.ArgoCDEnvManaged || view.State != "target_changed" || strings.Contains(response.Body.String(), "saved-test-token") {
		t.Fatal("target failure hid local recovery or exposed credentials")
	}
	request := connections.Update{Target: view.Target, Revision: view.Revision, Revisions: map[config.Integration]string{config.IntegrationArgoCD: view.Revision}, Kind: config.IntegrationArgoCD, Action: "reconfirm", Kinds: []config.Integration{config.IntegrationArgoCD}}
	body, _ := json.Marshal(request)
	result := httptest.NewRecorder()
	s.handleUpdateLocalConnection(result, httptest.NewRequest(http.MethodPut, "/api/integrations/connections", strings.NewReader(string(body))))
	if result.Code != http.StatusOK || s.localConnections.Resolve(view.Target, config.IntegrationArgoCD, false).Err != nil {
		t.Fatalf("recovery blocked: %d %s", result.Code, result.Body.String())
	}
}

func TestConcurrentRefreshDoesNotFailReaders(t *testing.T) {
	s := setupLocalProfileTest(t)
	target, _ := k8s.CurrentProfileTarget()
	s.localRuntime.Apply(target, false)
	var wg sync.WaitGroup
	for range 80 {
		wg.Go(func() {
			for range 5 {
				if err := connections.Refresh(config.IntegrationMetrics); err != nil {
					t.Errorf("parallel refresh: %v", err)
					return
				}
			}
		})
	}
	wg.Wait()
}

func TestArgoCandidateDoesNotBlockReadersAndCannotCommitAfterSwitch(t *testing.T) {
	s := setupLocalProfileTest(t)
	target, _ := k8s.CurrentProfileTarget()
	s.localRuntime.Apply(target, false)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(started) })
		<-release
		w.Write([]byte(`{"Version":"v3.0.0"}`))
	}))
	defer backend.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	view := s.localConnections.Resolve(target, config.IntegrationArgoCD, true).View
	data, _ := json.Marshal(connections.Update{Target: target, Revision: view.Revision, Kind: config.IntegrationArgoCD, Action: "save", URL: &backend.URL})
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		s.handleUpdateLocalConnection(response, httptest.NewRequest(http.MethodPut, "/api/integrations/connections", strings.NewReader(string(data))))
		done <- response
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("probe did not start")
	}
	for range 10 {
		if err := connections.Refresh(config.IntegrationMetrics); err != nil {
			t.Fatalf("candidate blocked metrics: %v", err)
		}
	}
	restore := k8s.SetTestProfileSource("/fixture/team", "different", "developer")
	defer restore()
	close(release)
	response := <-done
	if response.Code != http.StatusConflict {
		t.Fatalf("superseded probe: %d %s", response.Code, response.Body.String())
	}
	file, _, _ := s.localConnections.Store.Read()
	if len(file.Profiles) != 0 {
		t.Fatal("candidate saved after context changed")
	}
}

func TestManageConnectionsWithoutActiveCluster(t *testing.T) {
	s := setupLocalProfileTest(t)
	target, _ := k8s.CurrentProfileTarget()
	url := "https://metrics.example"
	view := s.localConnections.Resolve(target, config.IntegrationMetrics, false).View
	pending, err := s.localConnections.Prepare(target, connections.Update{Target: target, Revision: view.Revision, Kind: config.IntegrationMetrics, Action: "save", URL: &url})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.localConnections.Commit(context.Background(), pending); err != nil {
		t.Fatal(err)
	}
	previous := k8s.SetTestConfig(nil)
	defer k8s.SetTestConfig(previous)
	response := httptest.NewRecorder()
	s.handleLocalConnections(response, httptest.NewRequest(http.MethodGet, "/api/integrations/connections", nil))
	if response.Code != 200 {
		t.Fatalf("offline catalog: %s", response.Body.String())
	}
	var catalog localConnectionResponse
	if err := json.Unmarshal(response.Body.Bytes(), &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Connections) != 1 {
		t.Fatal("offline catalog lost saved connection")
	}
	use := catalog.Connections[0]
	data, _ := json.Marshal(connections.Update{Kind: config.IntegrationMetrics, Action: "forget", Binding: use.Binding, SourceRevision: use.Revision, ConfirmRemoval: true})
	response = httptest.NewRecorder()
	s.handleUpdateLocalConnection(response, httptest.NewRequest(http.MethodPut, "/api/integrations/connections", strings.NewReader(string(data))))
	if response.Code != 200 {
		t.Fatalf("offline forget: %d %s", response.Code, response.Body.String())
	}
}

func getLocalConnections(t *testing.T, s *Server) localConnectionResponse {
	t.Helper()
	response := httptest.NewRecorder()
	s.handleLocalConnections(response, httptest.NewRequest(http.MethodGet, "/api/integrations/connections", nil))
	var body localConnectionResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != http.StatusOK {
		t.Fatalf("read connections: %d %s", response.Code, response.Body.String())
	}
	return body
}

func TestRejectedArgoCandidateLeavesSavedConnectionUnchanged(t *testing.T) {
	s := setupLocalProfileTest(t)
	t.Cleanup(func() { argocd.SetConfig("", "", false, true) })
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/version":
			w.Write([]byte(`{"Version":"v3.0.0"}`))
		case "/api/v1/session/userinfo":
			if r.Header.Get("Authorization") != "Bearer saved-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Write([]byte(`{"loggedIn":true}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer backend.Close()
	target, _ := k8s.CurrentProfileTarget()
	pending, err := s.localConnections.Prepare(target, connections.Update{Target: target, Revision: s.localConnections.Resolve(target, config.IntegrationArgoCD, false).View.Revision, Kind: config.IntegrationArgoCD, Action: "save", URL: &backend.URL, Secret: &connections.SecretEdit{Action: "set", Value: "saved-token"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.localConnections.Commit(context.Background(), pending); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(s.localConnections.Store.Path)
	if err != nil {
		t.Fatal(err)
	}
	view := getLocalConnections(t, s).Profiles[config.IntegrationArgoCD]
	data, _ := json.Marshal(connections.Update{Target: view.Target, Revision: view.Revision, Kind: config.IntegrationArgoCD, Action: "save", URL: &backend.URL, Secret: &connections.SecretEdit{Action: "set", Value: "rejected-token"}})
	response := httptest.NewRecorder()
	s.handleUpdateLocalConnection(response, httptest.NewRequest(http.MethodPut, "/api/integrations/connections", strings.NewReader(string(data))))
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "previous connection is unchanged") || strings.Contains(response.Body.String(), "rejected-token") {
		t.Fatalf("rejected candidate: %d %s", response.Code, response.Body.String())
	}
	after, err := os.ReadFile(s.localConnections.Store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("rejected candidate changed clusters.json")
	}
	if got := getLocalConnections(t, s).Profiles[config.IntegrationArgoCD]; got.Revision != view.Revision || !got.SecretSet {
		t.Fatalf("saved connection changed after rejected candidate: %+v", got)
	}
	if err := argocd.Probe(context.Background()); err != nil {
		t.Fatalf("previous Argo CD connection stopped working: %v", err)
	}
}

func TestSavedConnectionRejectsOversizedRequest(t *testing.T) {
	s := setupLocalProfileTest(t)
	body := `{"kind":"metrics","action":"save","url":"http://` + strings.Repeat("a", 257*1024) + `"}`
	response := httptest.NewRecorder()
	s.handleUpdateLocalConnection(response, httptest.NewRequest(http.MethodPut, "/api/integrations/connections", strings.NewReader(body)))
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized request: %d %s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(s.localConnections.Store.Path); !os.IsNotExist(err) {
		t.Fatal("oversized request wrote clusters.json")
	}
}

func TestContextSwitchKeepsSavedCredentialsWithTheirContext(t *testing.T) {
	s := setupLocalProfileTest(t)
	t.Cleanup(func() { argocd.SetConfig("", "", false, true) })
	originalCost := opencost.ConfigSnapshot()
	t.Cleanup(func() { _ = opencost.Configure(originalCost) })
	var credentialed atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		argoToken := r.Header.Get("Authorization") == "Bearer argo-token-a"
		costKey := r.Header.Get("X-API-KEY") == "cost-key-a"
		if argoToken || costKey {
			credentialed.Add(1)
		}
		switch r.URL.Path {
		case "/api/version":
			w.Write([]byte(`{"Version":"v3.0.0"}`))
		case "/api/v1/session/userinfo":
			if !argoToken {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Write([]byte(`{"loggedIn":true}`))
		case "/allocation", "/model/allocation":
			if !costKey {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Write([]byte(`{"code":200,"data":[{"cluster-a":{"properties":{"cluster":"cluster-a"}}}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer backend.Close()
	targetA, err := k8s.CurrentProfileTarget()
	if err != nil {
		t.Fatal(err)
	}
	clusterID := "cluster-a"
	for _, req := range []connections.Update{
		{Kind: config.IntegrationArgoCD, Secret: &connections.SecretEdit{Action: "set", Value: "argo-token-a"}},
		{Kind: config.IntegrationCost, Secret: &connections.SecretEdit{Action: "set", Value: "cost-key-a"}, ClusterID: &clusterID},
	} {
		req.Target, req.Action, req.URL = targetA, "save", &backend.URL
		req.Revision = s.localConnections.Resolve(targetA, req.Kind, false).View.Revision
		pending, err := s.localConnections.Prepare(targetA, req)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.localConnections.Commit(context.Background(), pending); err != nil {
			t.Fatal(err)
		}
	}
	s.localRuntime.Apply(targetA, false)
	use := func() (argoErr, costErr error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		argoErr = argocd.Probe(ctx)
		_, costErr = opencost.Selected(ctx)
		return argoErr, costErr
	}
	if argoErr, costErr := use(); argoErr != nil || costErr != nil || !argocd.IsConfigured() || credentialed.Load() == 0 {
		t.Fatalf("context A did not use its saved connections: argo %v, cost %v", argoErr, costErr)
	}

	restore := k8s.SetTestProfileSource("/fixture/team", "other", "developer")
	defer restore()
	targetB, err := k8s.CurrentProfileTarget()
	if err != nil {
		t.Fatal(err)
	}
	s.localRuntime.ActivateSwitch(targetB)
	credentialed.Store(0)
	use()
	if argocd.IsConfigured() || opencost.ConfigSnapshot().APIKey != "" || credentialed.Load() != 0 {
		t.Fatalf("context B inherited context A's credentials: argo configured %v, requests carrying A's secrets %d", argocd.IsConfigured(), credentialed.Load())
	}
	b := getLocalConnections(t, s).Profiles
	if b[config.IntegrationArgoCD].SecretSet || b[config.IntegrationCost].SecretSet || b[config.IntegrationArgoCD].URL != "" {
		t.Fatalf("context B shows context A's saved connections: %+v", b)
	}

	restore()
	targetA, err = k8s.CurrentProfileTarget()
	if err != nil {
		t.Fatal(err)
	}
	s.localRuntime.ActivateSwitch(targetA)
	if argoErr, costErr := use(); argoErr != nil || costErr != nil || !argocd.IsConfigured() || credentialed.Load() == 0 {
		t.Fatalf("returning to context A did not restore its connections: argo %v, cost %v", argoErr, costErr)
	}
}
