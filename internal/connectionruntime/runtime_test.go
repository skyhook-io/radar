package connectionruntime

import (
	"path/filepath"
	"testing"

	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/connections"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/prometheus"
	"k8s.io/client-go/rest"
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

func TestSubstrateSelectionUsesExistingRefreshAndSwitch(t *testing.T) {
	t.Cleanup(k8s.SetTestLocalMode())
	t.Cleanup(k8s.SetTestProfileSource("/fixture/runtime", "dev", "developer"))
	previous := k8s.SetTestConfig(&rest.Config{Host: "https://cluster-a"})
	t.Cleanup(func() { k8s.SetTestConfig(previous) })
	status := k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	t.Cleanup(func() { k8s.SetConnectionStatus(status) })
	t.Cleanup(prometheus.Retire)
	target, err := k8s.CurrentProfileTarget()
	if err != nil {
		t.Fatal(err)
	}
	resolver := connections.NewResolver(&config.ProfileStore{Path: filepath.Join(t.TempDir(), "clusters.json")}, target, nil)
	r := New(resolver, nil)
	var targets []k8s.ProfileTarget
	r.SetSubstrateSelection(func(target k8s.ProfileTarget) { targets = append(targets, target) })
	connections.RegisterRefresh(r.Refresh)
	t.Cleanup(func() { connections.RegisterRefresh(nil) })
	if err := connections.Refresh(config.IntegrationMetrics); err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || !targets[0].Same(target) {
		t.Fatalf("first refresh: %+v", targets)
	}
	// With the other integrations unchanged, a sibling file must still be selected.
	if err := connections.Refresh(config.IntegrationArgoCD); err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 {
		t.Fatalf("unchanged refresh skipped sibling: %+v", targets)
	}
	switched := target
	switched.ClientGeneration++
	r.ActivateSwitch(switched)
	if len(targets) != 3 || !targets[2].Same(switched) {
		t.Fatalf("switch: %+v", targets)
	}
}
