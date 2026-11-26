package server

import (
	"context"
	"testing"

	"github.com/skyhook-io/radar/internal/argocd"
	"github.com/skyhook-io/radar/internal/connectionruntime"
	"github.com/skyhook-io/radar/internal/k8s"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/pkg/prom"
)

func currentTestIntegrations() connectionruntime.Set {
	return connectionruntime.SetFor(k8s.Current())
}

func admitTestIntegrations(t *testing.T, runtime *connectionruntime.Runtime) {
	t.Helper()
	k8s.RetireServingConnection()
	if !k8s.AdmitTestIntegrations(runtime.Build) {
		t.Fatal("test admission refused")
	}
}

func serverWithTestIntegrations(t *testing.T, startup connectionruntime.Settings) *Server {
	t.Helper()
	t.Cleanup(k8s.SnapshotTestConnection())
	runtime := connectionruntime.New(nil, startup)
	admitTestIntegrations(t, runtime)
	t.Cleanup(k8s.RetireServingConnection)
	return &Server{integrationRuntime: runtime}
}

func editTestIntegrations(t *testing.T, s *Server, update func(*connectionruntime.Settings)) {
	t.Helper()
	if err := s.integrationRuntime.PrepareShared(k8s.Current(), s.integrationRuntime.Snapshot(), update).Commit(func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func configureTestMetrics(t *testing.T, s *Server, url string, headers map[string]string) {
	t.Helper()
	editTestIntegrations(t, s, func(settings *connectionruntime.Settings) {
		settings.Metrics = prom.Connection{URL: url, Headers: headers}
	})
}

func testMetricsHeaders() map[string]string {
	_, headers := currentTestIntegrations().Metrics.CurrentConfig()
	return headers
}

func installTestMetrics(t *testing.T, url string) *prometheuspkg.Client {
	t.Helper()
	client := prometheuspkg.NewClient(nil, nil, "test", nil, false, k8s.GetResourceCache())
	client.Configure(url, nil)
	t.Cleanup(k8s.SetTestIntegrations(&connectionruntime.Set{Metrics: client}))
	return client
}

func installTestArgo(t *testing.T) *argocd.Manager {
	t.Helper()
	conn := k8s.Current()
	manager := argocd.NewConnectionManager(conn, k8s.IsInCluster(), conn.ContextBinding())
	t.Cleanup(k8s.SetTestIntegrations(&connectionruntime.Set{ArgoCD: manager}))
	return manager
}

func captureTestMetrics() (*prometheuspkg.Client, error) {
	return connectionruntime.CaptureMetrics(context.Background())
}
