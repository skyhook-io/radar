package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/connectionruntime"
	"github.com/skyhook-io/radar/internal/connections"
	"github.com/skyhook-io/radar/internal/k8s"
	internalopencost "github.com/skyhook-io/radar/internal/opencost"
	pkgopencost "github.com/skyhook-io/radar/pkg/opencost"
	"github.com/skyhook-io/radar/pkg/prom"
)

func setupKubecostIntegrationTest(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	return serverWithTestIntegrations(t, connectionruntime.Settings{})
}

func TestSaveSameKubecostConfigReplacesMemberInBackoff(t *testing.T) {
	for _, mode := range []string{"shared", "local profile"} {
		t.Run(mode, func(t *testing.T) {
			var ready atomic.Bool
			var calls atomic.Int32
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if !ready.Load() {
					w.WriteHeader(http.StatusBadGateway)
					return
				}
				_, _ = w.Write([]byte(`{"code":200,"data":[{"prod-a":{"properties":{"cluster":"prod-a"}}}]}`))
			}))
			defer backend.Close()
			clusterID := "prod-a"
			var s *Server
			var save func() *httptest.ResponseRecorder
			if mode == "shared" {
				s = setupKubecostIntegrationTest(t)
				editTestIntegrations(t, s, func(settings *connectionruntime.Settings) {
					settings.Cost = internalopencost.ManagerConfig{Source: internalopencost.SourceKubecost, URL: backend.URL, ClusterID: clusterID, ClusterIDContext: k8s.Current().ContextName()}
				})
				save = func() *httptest.ResponseRecorder {
					rec := httptest.NewRecorder()
					body := fmt.Sprintf(`{"source":"kubecost","url":%q,"clusterId":%q}`, backend.URL, clusterID)
					s.handleApplyCostSource(rec, httptest.NewRequest(http.MethodPut, "/api/integrations/cost", strings.NewReader(body)))
					return rec
				}
			} else {
				s = setupLocalProfileTest(t)
				target, err := k8s.CurrentProfileTarget()
				if err != nil {
					t.Fatal(err)
				}
				kind := config.IntegrationCost
				req := connections.Update{Target: target, Kind: kind, Action: "save", URL: &backend.URL, ClusterID: &clusterID, Revision: s.localConnections.Resolve(target, kind, false).View.Revision}
				pending, err := s.localConnections.Prepare(target, req)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.localConnections.Commit(t.Context(), pending); err != nil {
					t.Fatal(err)
				}
				s.integrationRuntime.RefreshProfiles()
				save = func() *httptest.ResponseRecorder {
					rec := httptest.NewRecorder()
					req.Revision = s.localConnections.Resolve(target, kind, false).View.Revision
					body, err := json.Marshal(req)
					if err != nil {
						t.Fatal(err)
					}
					s.handleUpdateLocalConnection(rec, httptest.NewRequest(http.MethodPut, "/api/integrations/connections", strings.NewReader(string(body))))
					return rec
				}
			}

			before := currentTestIntegrations()
			if _, err := before.Cost.Selected(t.Context()); err == nil || calls.Load() == 0 {
				t.Fatalf("expected a failed backend probe: %v, calls=%d", err, calls.Load())
			}
			ready.Store(true)
			previousCalls := calls.Load()
			if _, err := before.Cost.Selected(t.Context()); err == nil || calls.Load() != previousCalls {
				t.Fatalf("expected the live member to be in backoff: %v", err)
			}
			if rec := save(); rec.Code != http.StatusOK {
				t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
			}
			after := currentTestIntegrations()
			if after.Cost == before.Cost || after.Metrics != before.Metrics || after.ArgoCD != before.ArgoCD || after.Traffic != before.Traffic {
				t.Fatal("save should replace only the cost member")
			}
			conn, err := after.Cost.Selected(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			previousCalls = calls.Load()
			if _, err := conn.Client.GetAllocation(t.Context(), pkgopencost.KubecostAllocationOptions{}); err != nil || calls.Load() <= previousCalls {
				t.Fatalf("next read did not reach Kubecost: %v", err)
			}
		})
	}
}

func TestApplyCostSourceRejectsCredentialBearingURL(t *testing.T) {
	s := setupKubecostIntegrationTest(t)
	req := httptest.NewRequest(http.MethodPut, "/api/integrations/cost", strings.NewReader(`{"source":"kubecost","url":"https://user:secret@cost.example.com"}`))
	rec := httptest.NewRecorder()
	s.handleApplyCostSource(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
	if saved := config.Load(); saved.KubecostURL != "" || saved.KubecostAPIKey != "" {
		t.Fatalf("rejected credentials were persisted: %#v", saved)
	}
}

func TestApplyAutomaticSourceRejectsNoSourceAndRestoresPreviousConfig(t *testing.T) {
	s := setupKubecostIntegrationTest(t)
	if _, err := config.Update(func(c *config.Config) {
		c.CostSource = "prometheus"
		c.KubecostURL = "https://previous.example.com/model"
	}); err != nil {
		t.Fatal(err)
	}
	previousManager := internalopencost.ManagerConfig{
		Source: internalopencost.SourcePrometheus,
		URL:    "https://previous.example.com/model",
	}
	editTestIntegrations(t, s, func(settings *connectionruntime.Settings) { settings.Cost = previousManager })

	promServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("query") == "up" {
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"job":"prometheus"},"value":[1700000000,"1"]}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	configureTestMetrics(t, s, promServer.URL, nil)
	t.Cleanup(promServer.Close)

	rec := httptest.NewRecorder()
	s.handleApplyCostSource(rec, httptest.NewRequest(http.MethodPut, "/api/integrations/cost", strings.NewReader(`{"source":"auto"}`)))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "No compatible cost source") {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if saved := config.Load(); saved.CostSource != "prometheus" || saved.KubecostURL != "https://previous.example.com/model" {
		t.Fatalf("failed candidate changed persisted config: %#v", saved)
	}
	if restored := currentTestIntegrations().Cost.ConfigSnapshot(); restored.Source != previousManager.Source || restored.URL != previousManager.URL {
		t.Fatalf("manager config = %#v, want %#v", restored, previousManager)
	}
}

func TestKubecostEnvironmentConfigIsReadOnlyAndRedacted(t *testing.T) {
	s := setupKubecostIntegrationTest(t)
	t.Setenv("RADAR_COST_SOURCE", "kubecost")
	t.Setenv("RADAR_KUBECOST_URL", "https://cost.example.com/model")
	t.Setenv("RADAR_KUBECOST_CLUSTER_ID", "prod-a")
	t.Setenv("RADAR_KUBECOST_API_KEY", "env-secret")
	editTestIntegrations(t, s, func(settings *connectionruntime.Settings) {
		settings.Cost, settings.CostEnv, settings.CostError = internalopencost.EnvironmentConfiguration()
	})

	put := httptest.NewRecorder()
	s.handleApplyCostSource(put, httptest.NewRequest(http.MethodPut, "/api/integrations/cost", strings.NewReader(`{"source":"auto"}`)))
	if put.Code != http.StatusConflict {
		t.Fatalf("PUT status = %d, want 409; body = %s", put.Code, put.Body.String())
	}

	get := httptest.NewRecorder()
	s.handleGetConfig(get, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if get.Code != http.StatusOK || strings.Contains(get.Body.String(), "env-secret") {
		t.Fatalf("GET status=%d leaked=%v body=%s", get.Code, strings.Contains(get.Body.String(), "env-secret"), get.Body.String())
	}
	var body struct {
		File               config.Config `json:"file"`
		KubecostAPIKeySet  bool          `json:"kubecostApiKeySet"`
		KubecostEnvManaged bool          `json:"kubecostEnvManaged"`
	}
	if err := json.Unmarshal(get.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.KubecostEnvManaged || !body.KubecostAPIKeySet || body.File.CostSource != "kubecost" || body.File.KubecostURL != "https://cost.example.com/model" || body.File.KubecostClusterID != "prod-a" {
		t.Fatalf("unexpected config response: %#v", body)
	}
}

func TestGetConfigUsesLiveKubecostConfigWithoutMutatingEffectiveConfig(t *testing.T) {
	s := setupKubecostIntegrationTest(t)
	s.effectiveConfig = &config.Config{CostSource: "auto", KubecostURL: "https://stale.example.com/model", KubecostAPIKey: "stale"}
	editTestIntegrations(t, s, func(settings *connectionruntime.Settings) {
		settings.Cost = internalopencost.ManagerConfig{
			Source: internalopencost.SourceKubecost, URL: "https://live.example.com/model", APIKey: "live-secret", ClusterID: "prod-a",
		}
	})

	get := httptest.NewRecorder()
	s.handleGetConfig(get, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if get.Code != http.StatusOK || strings.Contains(get.Body.String(), "live-secret") || strings.Contains(get.Body.String(), "stale") {
		t.Fatalf("GET status=%d body=%s", get.Code, get.Body.String())
	}
	var body struct {
		Effective config.Config `json:"effective"`
	}
	if err := json.Unmarshal(get.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Effective.CostSource != "kubecost" || body.Effective.KubecostURL != "https://live.example.com/model" || body.Effective.KubecostClusterID != "prod-a" || body.Effective.KubecostAPIKey != "" {
		t.Fatalf("unexpected effective config: %#v", body.Effective)
	}
	if s.effectiveConfig.KubecostURL != "https://stale.example.com/model" || s.effectiveConfig.KubecostAPIKey != "stale" {
		t.Fatalf("effective config was mutated: %#v", s.effectiveConfig)
	}
}

func TestApplyCostSourceBindsExplicitClusterIDToContext(t *testing.T) {
	s := setupKubecostIntegrationTest(t)
	previousContext := k8s.SetTestContextName("cluster-a")
	t.Cleanup(func() { k8s.SetTestContextName(previousContext) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":200,"data":[{"prod-a":{"properties":{"cluster":"prod-a"}}}]}`))
	}))
	defer server.Close()

	req := httptest.NewRequest(http.MethodPut, "/api/integrations/cost", strings.NewReader(`{"source":"kubecost","url":"`+server.URL+`/model","apiKey":"secret","clusterId":"prod-a"}`))
	rec := httptest.NewRecorder()
	s.handleApplyCostSource(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Address string              `json:"address"`
		Service *kubecostServiceRef `json:"service"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Address != server.URL+"/model" {
		t.Fatalf("address = %q, want resolved Kubecost URL", response.Address)
	}
	if response.Service != nil {
		t.Fatalf("explicit URL returned discovered Service reference: %#v", response.Service)
	}
	if saved := config.Load(); saved.KubecostClusterIDContext != "cluster-a" || saved.KubecostAPIKeyContext != "" {
		t.Fatalf("cluster ID context = %q, API key context = %q; want cluster-a and reusable explicit-URL key", saved.KubecostClusterIDContext, saved.KubecostAPIKeyContext)
	}
}

func TestKubecostConnectionGuidanceUsesTypedErrors(t *testing.T) {
	if got := kubecostConnectionGuidance(context.DeadlineExceeded, false); !strings.Contains(got, "timed out") {
		t.Fatalf("deadline guidance = %q", got)
	}
	upstream := &prom.HTTPError{StatusCode: http.StatusBadGateway, Body: []byte("authentication service unavailable")}
	if got := kubecostConnectionGuidance(upstream, false); strings.Contains(got, "rejected the API key") {
		t.Fatalf("upstream text was misclassified as authentication: %q", got)
	}
	if got := kubecostConnectionGuidance(internalopencost.ErrKubecostAuthentication, true); !strings.Contains(got, "rejected the API key") {
		t.Fatalf("typed authentication guidance = %q", got)
	}
	if got := kubecostConnectionGuidance(internalopencost.ErrKubecostAuthentication, false); !strings.Contains(got, "requires authentication") {
		t.Fatalf("authentication guidance without a key = %q", got)
	}
	fallbackUnavailable := fmt.Errorf("primary endpoint failed: %v; fallback failed: %w", internalopencost.ErrKubecostAuthentication, internalopencost.ErrKubecostUnavailable)
	if got := kubecostConnectionGuidance(fallbackUnavailable, false); !strings.Contains(got, "unreachable") || strings.Contains(got, "requires authentication") {
		t.Fatalf("fallback availability guidance = %q", got)
	}
	fallbackNoData := fmt.Errorf("primary endpoint failed: %v; fallback failed: %w", internalopencost.ErrKubecostAuthentication, internalopencost.ErrKubecostNoData)
	if got := kubecostConnectionGuidance(fallbackNoData, false); !strings.Contains(got, "no allocation data") {
		t.Fatalf("fallback no-data guidance = %q", got)
	}
	if got := kubecostConnectionGuidance(internalopencost.ErrKubecostContextMismatch, false); !strings.Contains(got, "not bound to the current kubeconfig context") {
		t.Fatalf("context mismatch guidance = %q", got)
	}
	if got := kubecostConnectionGuidance(internalopencost.ErrNoCostSource, false); !strings.Contains(got, "No compatible cost source") {
		t.Fatalf("no-source guidance = %q", got)
	}
}
