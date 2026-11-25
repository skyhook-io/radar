package k8s

import (
	"sync"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/k8score"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

func TestDynamicCacheStartFinishesAfterClose(t *testing.T) {
	withFreshTestConnection(t)
	old := Current()
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"})
	watching := make(chan struct{})
	started := sync.OnceFunc(func() { close(watching) })
	watcher := watch.NewProxyWatcher(make(chan watch.Event))
	client.PrependWatchReactor("widgets", func(clienttesting.Action) (bool, watch.Interface, error) {
		started()
		return true, watcher, nil
	})
	release := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	done := make(chan error, 1)
	go func() {
		done <- old.dynamicCacheState().init(func() (*DynamicResourceCache, error) {
			core, err := k8score.NewDynamicResourceCache(k8score.DynamicCacheConfig{DynamicClient: client})
			if err != nil {
				return nil, err
			}
			if err := core.EnsureWatching(gvr); err != nil {
				core.Stop()
				return nil, err
			}
			<-release
			return &DynamicResourceCache{core}, nil
		})
	}()
	select {
	case <-watching:
	case err := <-done:
		t.Fatalf("construction finished before opening its watch: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("construction did not open its watch")
	}

	// A cancelled switch drops the clients before tearing down their caches.
	displaced := dropClusterClients()
	resetAllSubsystems(displaced)
	if err := InitTestDynamicResourceCache(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ResetDynamicResourceCache)
	replacement := GetDynamicResourceCache()
	if replacement == nil {
		t.Fatal("replacement cache did not start")
	}
	unblock()
	if err := <-done; err == nil {
		t.Fatal("a closed connection accepted a late construction")
	}
	select {
	case <-watcher.StopChan():
	case <-time.After(3 * time.Second):
		t.Fatal("the discarded construction left its informer watching")
	}
	if old == Current() || old.DynamicResourceCache() != nil || GetDynamicResourceCache() != replacement || replacement.IsStopped() {
		t.Fatal("a late construction published after the switch")
	}
}

func TestRebuildOnSameClientsReplacesDynamicCache(t *testing.T) {
	withFreshTestConnection(t)
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	if err := InitTestDynamicResourceCache(client, nil); err != nil {
		t.Fatal(err)
	}
	old := Current()
	first := old.DynamicResourceCache()
	ResetAllSubsystems()
	if first == nil || !first.IsStopped() || GetDynamicResourceCache() != nil {
		t.Fatal("rebuild did not retire the old dynamic cache")
	}
	if err := old.initDynamicResourceCache(nil); err == nil {
		t.Fatal("restarting the closed construction reported success")
	}
	if err := InitDynamicResourceCache(nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ResetDynamicResourceCache)
	if second := GetDynamicResourceCache(); Current().Dynamic() != client || second == nil || second == first || second.IsStopped() {
		t.Fatal("rebuild did not serve a new dynamic cache on the same clients")
	}
}

func TestReadmissionKeepsTheConnectionsDynamicCache(t *testing.T) {
	withFreshTestConnection(t)
	t.Cleanup(SnapshotTestConnection())
	clearTestConnectionIdentity()
	if err := InitTestDynamicResourceCache(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ResetDynamicResourceCache)
	SetConnectionStatus(ConnectionStatus{State: StateConnected})
	first, built := ServingConnectionID(), GetDynamicResourceCache()
	RetireServingConnection()
	SetConnectionStatus(ConnectionStatus{State: StateConnected})
	if second := ServingConnectionID(); second == "" || second == first {
		t.Fatalf("re-admission IDs %q then %q, want a new one", first, second)
	}
	if got := GetDynamicResourceCache(); got == nil || got != built || got.IsStopped() {
		t.Fatal("re-admission lost the running dynamic cache")
	}
}

func TestCRDCompletionSubscriptionsAcrossBuilds(t *testing.T) {
	withFreshTestConnection(t)
	restoreTestConnection(nil)
	crdCompleteMu.Lock()
	previous := crdCompleteSubscribers
	crdCompleteMu.Unlock()
	t.Cleanup(func() {
		crdCompleteMu.Lock()
		crdCompleteSubscribers = previous
		crdCompleteMu.Unlock()
	})

	subscribe := func() <-chan struct{} {
		called := make(chan struct{}, 2)
		OnCRDDiscoveryComplete(func() { called <- struct{}{} })
		return called
	}
	beforeInstall := subscribe()
	if err := installTestDynamicClient(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), nil); err != nil {
		t.Fatal(err)
	}
	Current().dynamicCacheState()
	beforeBuild := subscribe()
	if err := InitDynamicResourceCache(nil); err != nil {
		t.Fatal(err)
	}
	afterBuild := subscribe()

	discover := func() {
		t.Helper()
		GetDynamicResourceCache().DiscoverAllCRDs()
		for _, called := range []<-chan struct{}{beforeInstall, beforeBuild, afterBuild} {
			select {
			case <-called:
			case <-time.After(3 * time.Second):
				t.Fatal("missing CRD completion callback")
			}
		}
	}
	discover()
	ResetAllSubsystems()
	Current().publishDiscovery(buildFakeDiscoveryCore(t))
	if err := InitDynamicResourceCache(nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ResetDynamicResourceCache)
	discover()
}
