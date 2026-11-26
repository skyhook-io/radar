package connectionruntime

import (
	"testing"

	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/connections"
	"github.com/skyhook-io/radar/internal/k8s"
)

func TestRefreshOpenObserveWhileDisconnectedReactivatesMetricsOnReconnect(t *testing.T) {
	if k8s.GetConnectionStatus().State == k8s.StateConnected {
		t.Fatal("test expects no Kubernetes connection")
	}
	target := k8s.ProfileTarget{Context: "ctx", Binding: "b", Fingerprint: "f"}
	r := New(nil, nil)
	r.target = target
	r.active[config.IntegrationMetrics] = connections.Selection{}
	r.active[config.IntegrationArgoCD] = connections.Selection{}

	r.RefreshOpenObserve()

	if !r.changed(target, config.IntegrationMetrics, connections.Selection{}) {
		t.Fatal("metrics must activate again on the next Apply")
	}
	if r.changed(target, config.IntegrationArgoCD, connections.Selection{}) {
		t.Fatal("other integrations must stay as they were")
	}
}
