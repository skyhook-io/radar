package mcp

import (
	"testing"

	"github.com/skyhook-io/radar/internal/connectionruntime"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/opencost"
	"github.com/skyhook-io/radar/internal/prometheus"
)

func installTestMetrics(t *testing.T, url string) *prometheus.Client {
	t.Helper()
	client := prometheus.NewClient(nil, nil, "test-ctx", nil, false, k8s.GetResourceCache())
	client.Configure(url, nil)
	t.Cleanup(k8s.SetTestIntegrations(&connectionruntime.Set{Metrics: client}))
	return client
}

func installTestCostMetrics(t *testing.T, url string) {
	t.Helper()
	conn := k8s.Current()
	client := prometheus.NewClient(nil, nil, "test-ctx", nil, false, conn.ResourceCache())
	client.Configure(url, nil)
	cost := opencost.NewManager(conn, false, client, nil)
	if err := cost.Configure(opencost.ManagerConfig{Source: opencost.SourcePrometheus}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.SetTestIntegrations(&connectionruntime.Set{Metrics: client, Cost: cost}))
}
