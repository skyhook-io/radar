package k8score

import (
	"context"
	"testing"
)

// Discovery can still be loading after a switch while the typed caches serve.
// Actions on built-in workloads must not wait for it.
func TestBuiltinWorkloadActionsWithoutDiscovery(t *testing.T) {
	for name, act := range map[string]func(*WorkloadManager) error{
		"restart": func(m *WorkloadManager) error { return m.RestartWorkload(context.Background(), "deployments", "prod", "web") },
		"scale":   func(m *WorkloadManager) error { return m.ScaleWorkload(context.Background(), "deployments", "prod", "web", 3) },
		"pause":   func(m *WorkloadManager) error { return m.SetDeploymentPaused(context.Background(), "prod", "web", true) },
	} {
		t.Run(name, func(t *testing.T) {
			client := newAppsClient(deploymentObject(podTemplateV1(), nil, nil))
			if err := act(NewWorkloadManager(client, nil)); err != nil {
				t.Fatalf("%s without discovery: %v", name, err)
			}
			if len(patchActions(client)) != 1 {
				t.Fatalf("%s issued %d patches, want 1", name, len(patchActions(client)))
			}
		})
	}
	if _, _, err := NewWorkloadManager(nil, nil).workloadGVR("rollouts"); err == nil {
		t.Fatal("a custom kind resolved without discovery")
	}
}
