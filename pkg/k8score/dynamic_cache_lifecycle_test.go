package k8score

import (
	"context"
	"fmt"
	"runtime/pprof"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"
)

func lifecycleTestCache(t *testing.T, gvrs ...schema.GroupVersionResource) *DynamicResourceCache {
	t.Helper()
	lists := map[schema.GroupVersionResource]string{}
	for _, gvr := range gvrs {
		lists[gvr] = "WidgetList"
	}
	dyn := fakeDynamicForListAccess(t, lists, func(schema.GroupVersionResource, string) bool { return true })
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn})
	if err != nil {
		t.Fatalf("NewDynamicResourceCache failed: %v", err)
	}
	t.Cleanup(d.Stop)
	return d
}

func informerFor(t *testing.T, d *DynamicResourceCache, gvr schema.GroupVersionResource, ns string) cache.SharedIndexInformer {
	t.Helper()
	d.mu.RLock()
	defer d.mu.RUnlock()
	e := d.informers[informerKey{gvr: gvr, ns: ns}]
	if e == nil {
		t.Fatalf("no informer for %v in %q", gvr, ns)
	}
	return e.informer
}

func TestStopStopsEveryDynamicInformer(t *testing.T) {
	widgets := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
	gadgets := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "gadgets"}
	d := lifecycleTestCache(t, widgets, gadgets)

	if err := d.startWatching(widgets, ""); err != nil {
		t.Fatalf("startWatching(widgets): %v", err)
	}
	if err := d.startWatching(gadgets, "team-a"); err != nil {
		t.Fatalf("startWatching(gadgets, team-a): %v", err)
	}
	informers := []cache.SharedIndexInformer{
		informerFor(t, d, widgets, ""),
		informerFor(t, d, gadgets, "team-a"),
	}
	for _, inf := range informers {
		waitUntilTrue(t, "informer sync", inf.HasSynced)
		if inf.IsStopped() {
			t.Fatal("informer stopped before the cache was")
		}
	}

	d.Stop()

	for _, inf := range informers {
		waitUntilTrue(t, "informer to stop with the cache", inf.IsStopped)
	}
	if err := d.startWatching(gadgets, "team-b"); err == nil {
		t.Fatal("a stopped cache must refuse new informers")
	}
}

func TestSupersededNamespaceInformerStopsAloneAndCacheKeepsRunning(t *testing.T) {
	widgets := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
	d := lifecycleTestCache(t, widgets)

	if err := d.startWatching(widgets, "team-a"); err != nil {
		t.Fatalf("startWatching(team-a): %v", err)
	}
	nsInformer := informerFor(t, d, widgets, "team-a")
	waitUntilTrue(t, "namespace informer sync", nsInformer.HasSynced)

	if err := d.startWatching(widgets, ""); err != nil {
		t.Fatalf("startWatching(cluster-wide): %v", err)
	}
	clusterInformer := informerFor(t, d, widgets, "")

	waitUntilTrue(t, "superseded informer to stop", nsInformer.IsStopped)
	waitUntilTrue(t, "cluster-wide informer sync", clusterInformer.HasSynced)
	if clusterInformer.IsStopped() {
		t.Fatal("superseding must not stop the replacement informer")
	}
	if err := d.stopCtx.Err(); err != nil {
		t.Fatalf("cancelling one informer cancelled the cache: %v", err)
	}
}

// A goroutine that only relays shutdown into an informer's context costs one
// per informer, hundreds on clusters with many CRDs. That covers both Radar's
// own relay and the ones context.WithCancel starts when an informer runs from
// a bare channel instead of a cancelable context.
func TestSyncedDynamicInformersLeaveNoShutdownRelayGoroutines(t *testing.T) {
	gvrs := []schema.GroupVersionResource{
		{Group: "example.com", Version: "v1", Resource: "widgets"},
		{Group: "example.com", Version: "v1", Resource: "gadgets"},
		{Group: "example.com", Version: "v1", Resource: "gizmos"},
	}
	d := lifecycleTestCache(t, gvrs...)
	// Goroutines inherit labels from the goroutine that starts them, so the
	// label scopes the scan to goroutines this test's informers started.
	label := pprof.Labels("lifecycle-test", t.Name())
	pprof.SetGoroutineLabels(pprof.WithLabels(context.Background(), label))
	t.Cleanup(func() { pprof.SetGoroutineLabels(context.Background()) })
	for _, gvr := range gvrs {
		if err := d.startWatching(gvr, ""); err != nil {
			t.Fatalf("startWatching(%v): %v", gvr, err)
		}
	}
	for _, gvr := range gvrs {
		waitUntilTrue(t, "informer sync", informerFor(t, d, gvr, "").HasSynced)
	}

	labelLine := fmt.Sprintf("# labels: {%q:%q}", "lifecycle-test", t.Name())
	ours := func() []string {
		var out strings.Builder
		if err := pprof.Lookup("goroutine").WriteTo(&out, 1); err != nil {
			t.Fatalf("goroutine profile: %v", err)
		}
		var groups []string
		for _, g := range strings.Split(out.String(), "\n\n") {
			if strings.Contains(g, labelLine) {
				groups = append(groups, g)
			}
		}
		return groups
	}
	if len(ours()) == 0 {
		t.Fatal("no goroutines carry the test label; the scan would pass vacuously")
	}
	// The per-informer sync waiter exits once its informer reports synced.
	waitUntilTrue(t, "no shutdown relay goroutines", func() bool {
		for _, g := range ours() {
			if strings.Contains(g, "(*DynamicResourceCache).startWatchingScope.func") ||
				strings.Contains(g, "context.(*cancelCtx).propagateCancel") {
				return false
			}
		}
		return true
	})
}
