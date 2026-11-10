package k8s

import (
	"context"
	"testing"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Topology memos key on Generation(), so a cache built by InitResourceCache
// after a reset must report a different generation than the one it replaces.
// Every list probe against the unreachable server fails, so no informers
// start and the cache is built and promoted without a cluster.
func TestInitResourceCache_ReplacementHasNewGeneration(t *testing.T) {
	defer ResetTestState()

	client, err := kubernetes.NewForConfig(&rest.Config{Host: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("creating client: %v", err)
	}
	clientMu.Lock()
	k8sClient = client
	clientMu.Unlock()
	defer func() {
		clientMu.Lock()
		k8sClient = nil
		clientMu.Unlock()
	}()

	ResetResourceCache()
	if err := InitResourceCache(context.Background()); err != nil {
		t.Fatalf("first init: %v", err)
	}
	first := GetResourceCache()
	if first == nil {
		t.Fatal("no cache after first init")
	}

	ResetResourceCache()
	if err := InitResourceCache(context.Background()); err != nil {
		t.Fatalf("second init: %v", err)
	}
	second := GetResourceCache()
	if second == nil {
		t.Fatal("no cache after second init")
	}

	if first.Generation() == second.Generation() {
		t.Errorf("replacement cache kept generation %d", second.Generation())
	}
}
