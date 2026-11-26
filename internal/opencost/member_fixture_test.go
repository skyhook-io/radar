package opencost

import (
	"context"
	"errors"
	"testing"

	"net/http"

	"github.com/skyhook-io/radar/internal/k8s"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
)

func testMetrics(t *testing.T, url string) *prometheuspkg.Client {
	t.Helper()
	client := prometheuspkg.NewClient(nil, nil, "test", nil, false, k8s.GetResourceCache())
	client.Configure(url, nil)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func testMetricsSource(client *prometheuspkg.Client) func() (*prometheuspkg.Client, error) {
	return func() (*prometheuspkg.Client, error) { return client, nil }
}

func testRouteScope(manager *Manager, currency func() string) RouteScope {
	return RouteScope{Capture: func(*http.Request) (*Manager, func() string) { return manager, currency }}
}

func probeConfiguredKubecost(ctx context.Context, cfg ManagerConfig) (Connection, error) {
	if cfg.URL == "" {
		return Connection{}, errors.New("Kubecost requires an Aggregator URL")
	}
	manager := NewManager(k8s.Current(), false, nil, nil)
	defer manager.Close()
	return manager.connectKubecost(ctx, cfg)
}
