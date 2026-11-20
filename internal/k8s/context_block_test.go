package k8s

import (
	"errors"
	"testing"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestBlockClusterAccessDropsClientsAndRejectsReconnects(t *testing.T) {
	ResetTestState()
	t.Cleanup(func() {
		contextOperationsBlocked.Store(nil)
		ResetTestState()
	})
	config := &rest.Config{Host: "http://localhost:1"}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	clientMu.Lock()
	k8sConfig, k8sClient = config, clientset
	discoveryClient = discovery.NewDiscoveryClientForConfigOrDie(config)
	dynamicClient = dynamic.NewForConfigOrDie(config)
	clientMu.Unlock()

	blocked := errors.New("settings need a change")
	BlockClusterAccess(blocked)

	if GetConfig() != nil || GetClient() != nil || GetDynamicClient() != nil || GetDiscoveryClient() != nil {
		t.Fatal("a blocked Radar still holds a cluster client")
	}
	err = PerformContextSwitch("any-context")
	if !errors.Is(err, ErrContextSwitchPreflight) || !errors.Is(err, blocked) {
		t.Fatalf("PerformContextSwitch = %v, want a preflight rejection carrying the block", err)
	}
	// Retry takes its own path in-cluster (and with --fake-in-cluster); the
	// block must stop it there too.
	if err := RetryCurrentConnection(); !errors.Is(err, blocked) {
		t.Fatalf("RetryCurrentConnection = %v, want the block", err)
	}
}
