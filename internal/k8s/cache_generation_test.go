package k8s

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/k8score"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Topology memos key on Generation(), so a cache built by InitResourceCache
// after a reset must report a different generation than the one it replaces.
// With no dynamic client the probe enables no informers, so both builds
// reach promotion without an API server.
func TestInitResourceCache_ReplacementHasNewGeneration(t *testing.T) {
	defer ResetTestState()

	client, err := kubernetes.NewForConfig(&rest.Config{Host: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("creating client: %v", err)
	}
	clientMu.Lock()
	installedConn.Store(installedConnectionLocked(nil, &sharedKubernetesClients{clientset: client}))
	clientMu.Unlock()
	defer func() {
		ResetResourceCache()
		clientMu.Lock()
		installedConn.Store(nil)
		clientMu.Unlock()
	}()

	if err := InitResourceCache(context.Background()); err != nil {
		t.Fatalf("first init: %v", err)
	}
	first := GetResourceCache()
	if first == nil {
		t.Fatal("no cache after first init")
	}

	ResetAllSubsystems()
	if err := InitResourceCache(context.Background()); err != nil {
		t.Fatalf("second init: %v", err)
	}
	second := GetResourceCache()
	if second == nil {
		t.Fatal("no cache after second init")
	}

	if Current().Client() != client || first == second {
		t.Fatal("rescope must keep the clients and serve a new construction")
	}
	select {
	case <-first.Done():
	default:
		t.Fatal("previous construction is still running")
	}
	if first.Generation() == second.Generation() {
		t.Errorf("replacement cache kept generation %d", second.Generation())
	}
}

func TestInitResourceCacheProbeFinishesAfterClose(t *testing.T) {
	withFreshTestConnection(t)
	started, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	signal := sync.OnceFunc(func() { close(started) })
	client := kubernetes.NewForConfigOrDie(&rest.Config{Host: "http://127.0.0.1:1"})
	dyn := fakeDyn(t, func(schema.GroupVersionResource, string) bool {
		signal()
		<-release
		return false
	})
	restoreTestConnection(&Connection{clients: &sharedKubernetesClients{clientset: client, dynamic: dyn}})
	old := Current()
	done := make(chan error, 1)
	go func() { done <- InitResourceCache(t.Context()) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("construction never reached the permission probe")
	}
	ResetAllSubsystems()
	unblock()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("closed construction reported success")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("closed construction did not finish")
	}
	promoted, syncing := SnapshotCaches()
	if Current() == old || promoted != nil || syncing != nil || InitialSyncComplete() || old.ResourceCache() != nil {
		t.Fatal("late construction published after the connection was replaced")
	}
}

func TestResetStopsInFlightInformerSync(t *testing.T) {
	withFreshTestConnection(t)
	t.Setenv("RADAR_DEBUG_SYNC_DELAY", "pods=1h")
	client := kubernetes.NewForConfigOrDie(&rest.Config{Host: "http://127.0.0.1:1"})
	SetTestClient(client)
	t.Cleanup(SetTestPermissionResult(&PermissionCheckResult{
		Perms:  &ResourcePermissions{Pods: true},
		Scopes: map[string]k8score.ResourceScope{"pods": {Enabled: true}},
	}))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- InitResourceCache(ctx) }()
	var syncing *ResourceCache
	for deadline := time.Now().Add(3 * time.Second); syncing == nil; {
		syncing = GetSyncingResourceCache()
		if time.Now().After(deadline) {
			t.Fatal("informer did not enter its initial sync")
		}
		if syncing == nil {
			time.Sleep(time.Millisecond)
		}
	}
	ResetAllSubsystems()
	select {
	case <-syncing.Done():
	default:
		t.Fatal("closing the connection left the syncing informer running")
	}
	select {
	case err := <-done:
		if err == nil || GetResourceCache() != nil || GetSyncingResourceCache() != nil {
			t.Fatalf("closed sync returned %v, promoted %p, syncing %p", err, GetResourceCache(), GetSyncingResourceCache())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("closing the connection did not release the constructor")
	}
}
