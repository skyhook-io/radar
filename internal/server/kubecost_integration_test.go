package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/k8s"
	internalopencost "github.com/skyhook-io/radar/internal/opencost"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/pkg/prom"
)

func setupKubecostIntegrationTest(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	original := internalopencost.ConfigSnapshot()
	t.Cleanup(func() { _ = internalopencost.Configure(original) })
	if err := internalopencost.Configure(internalopencost.ManagerConfig{Source: internalopencost.SourceAuto}); err != nil {
		t.Fatal(err)
	}
	return &Server{}
}

func TestApplyKubecostConfigRejectsCredentialBearingURL(t *testing.T) {
	s := setupKubecostIntegrationTest(t)
	req := httptest.NewRequest(http.MethodPut, "/api/integrations/kubecost", strings.NewReader(`{"source":"kubecost","url":"https://user:secret@cost.example.com"}`))
	rec := httptest.NewRecorder()
	s.handleApplyKubecostConfig(rec, req)

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
	if err := internalopencost.Configure(previousManager); err != nil {
		t.Fatal(err)
	}

	promServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("query") == "up" {
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"job":"prometheus"},"value":[1700000000,"1"]}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	prometheuspkg.Initialize(nil, nil, "test")
	prometheuspkg.SetManualURL(promServer.URL)
	t.Cleanup(func() {
		promServer.Close()
		prometheuspkg.Reset()
		prometheuspkg.Initialize(nil, nil, "")
	})

	rec := httptest.NewRecorder()
	s.handleApplyKubecostConfig(rec, httptest.NewRequest(http.MethodPut, "/api/integrations/kubecost", strings.NewReader(`{"source":"auto"}`)))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "No compatible cost source") {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if saved := config.Load(); saved.CostSource != "prometheus" || saved.KubecostURL != "https://previous.example.com/model" {
		t.Fatalf("failed candidate changed persisted config: %#v", saved)
	}
	if restored := internalopencost.ConfigSnapshot(); restored.Source != previousManager.Source || restored.URL != previousManager.URL {
		t.Fatalf("manager config = %#v, want %#v", restored, previousManager)
	}
}

func TestKubecostEnvironmentConfigIsReadOnlyAndRedacted(t *testing.T) {
	s := setupKubecostIntegrationTest(t)
	t.Setenv("RADAR_COST_SOURCE", "kubecost")
	t.Setenv("RADAR_KUBECOST_URL", "https://cost.example.com/model")
	t.Setenv("RADAR_KUBECOST_CLUSTER_ID", "prod-a")
	t.Setenv("RADAR_KUBECOST_API_KEY", "env-secret")
	if err := internalopencost.ConfigureStartup(internalopencost.ManagerConfig{Source: internalopencost.SourceAuto}); err != nil {
		t.Fatal(err)
	}

	put := httptest.NewRecorder()
	s.handleApplyKubecostConfig(put, httptest.NewRequest(http.MethodPut, "/api/integrations/kubecost", strings.NewReader(`{"source":"auto"}`)))
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
	if err := internalopencost.Configure(internalopencost.ManagerConfig{
		Source: internalopencost.SourceKubecost, URL: "https://live.example.com/model", APIKey: "live-secret", ClusterID: "prod-a",
	}); err != nil {
		t.Fatal(err)
	}

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

func TestApplyKubecostConfigBindsExplicitClusterIDToContext(t *testing.T) {
	s := setupKubecostIntegrationTest(t)
	previousContext := k8s.SetTestContextName("cluster-a")
	t.Cleanup(func() { k8s.SetTestContextName(previousContext) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"code":200,"data":[{"prod-a":{"properties":{"cluster":"prod-a"}}}]}`))
	}))
	defer server.Close()

	req := httptest.NewRequest(http.MethodPut, "/api/integrations/kubecost", strings.NewReader(`{"source":"kubecost","url":"`+server.URL+`/model","apiKey":"secret","clusterId":"prod-a"}`))
	rec := httptest.NewRecorder()
	s.handleApplyKubecostConfig(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response struct {
		Address string `json:"address"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Address != server.URL+"/model" {
		t.Fatalf("address = %q, want resolved Kubecost URL", response.Address)
	}
	if saved := config.Load(); saved.KubecostClusterIDContext != "cluster-a" || saved.KubecostAPIKeyContext != "" {
		t.Fatalf("cluster ID context = %q, API key context = %q; want cluster-a and reusable explicit-URL key", saved.KubecostClusterIDContext, saved.KubecostAPIKeyContext)
	}
}

func TestKubecostConnectionGuidanceUsesTypedErrors(t *testing.T) {
	if got := kubecostConnectionGuidance(context.DeadlineExceeded); !strings.Contains(got, "timed out") {
		t.Fatalf("deadline guidance = %q", got)
	}
	upstream := &prom.HTTPError{StatusCode: http.StatusBadGateway, Body: []byte("authentication service unavailable")}
	if got := kubecostConnectionGuidance(upstream); strings.Contains(got, "rejected the API key") {
		t.Fatalf("upstream text was misclassified as authentication: %q", got)
	}
	if got := kubecostConnectionGuidance(internalopencost.ErrKubecostAuthentication); !strings.Contains(got, "rejected the API key") {
		t.Fatalf("typed authentication guidance = %q", got)
	}
	if got := kubecostConnectionGuidance(internalopencost.ErrKubecostContextMismatch); !strings.Contains(got, "not bound to the current kubeconfig context") {
		t.Fatalf("context mismatch guidance = %q", got)
	}
	if got := kubecostConnectionGuidance(internalopencost.ErrNoCostSource); !strings.Contains(got, "No compatible cost source") {
		t.Fatalf("no-source guidance = %q", got)
	}
}
