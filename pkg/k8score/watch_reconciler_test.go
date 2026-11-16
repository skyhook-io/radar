package k8score

import (
	"context"
	"fmt"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func widgetDiscovery(t *testing.T) *ResourceDiscovery {
	t.Helper()
	d, err := NewResourceDiscovery(newLockedDiscovery(&metav1.APIResourceList{
		GroupVersion: "example.io/v1", APIResources: []metav1.APIResource{{Name: "widgets", Kind: "Widget", Namespaced: true, Verbs: []string{"get", "list", "watch"}}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestWatchReconcilerRequiresStart(t *testing.T) {
	for _, required := range []bool{false, true} {
		t.Run(fmt.Sprintf("required=%t", required), func(t *testing.T) {
			discovery, err := NewResourceDiscovery(newLockedDiscovery(crdResources, &metav1.APIResourceList{
				GroupVersion: "example.io/v1", APIResources: []metav1.APIResource{{Name: "widgets", Kind: "Widget", Namespaced: true, Verbs: []string{"get", "list", "watch"}}},
			}))
			if err != nil {
				t.Fatal(err)
			}
			client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
				crdGVR: "CustomResourceDefinitionList", deniedWidgets: "WidgetList",
			})
			cfg := DynamicCacheConfig{Discovery: discovery, DynamicClient: client}
			if required {
				cfg.RequiredWatchKinds = []schema.GroupKind{{Group: "example.io", Kind: "Widget"}}
			}
			d, err := NewDynamicResourceCache(cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(d.Stop)
			d.markCRDsSeen([]string{"db.example.com/dbclusters"})
			crd := crdObject("dbclusters.db.example.com", "db.example.com", "dbclusters")
			d.reconcileWatches()
			d.onCRDEvent(crd)
			d.crdEventHandler().OnAdd(crd, true)
			d.crdEventHandler().OnUpdate(crd, crd)
			d.mu.RLock()
			requests, informers := len(d.watchRequests), len(d.informers)
			d.mu.RUnlock()
			if requests != 0 || informers != 0 || len(client.Actions()) != 0 {
				t.Fatalf("unstarted reconciler activated watches: requests=%d informers=%d actions=%v", requests, informers, client.Actions())
			}
			if required {
				d.StartWatchReconciler()
				waitUntilTrue(t, "explicit start activates required kind", func() bool { return d.IsSynced(deniedWidgets) })
			}
		})
	}
}

func TestLateCRDMonitorDoesNotStartWatchReconciler(t *testing.T) {
	discovery := widgetDiscovery(t)
	discovery.AddAPIResource(APIResource{Group: crdGVR.Group, Version: crdGVR.Version, Name: crdGVR.Resource, Kind: "CustomResourceDefinition", Verbs: []string{"get", "list", "watch"}})
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		crdGVR: "CustomResourceDefinitionList", deniedWidgets: "WidgetList",
	})
	d, err := NewDynamicResourceCache(DynamicCacheConfig{Discovery: discovery, DynamicClient: client, RequiredWatchKinds: []schema.GroupKind{{Group: "example.io", Kind: "Widget"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	d.watchLateCRDs()
	waitUntilTrue(t, "late CRD monitor", func() bool { return d.IsSynced(crdGVR) })
	if d.watchReconcilerStarted.Load() || d.hasCoveringInformer(deniedWidgets, "") {
		t.Fatal("late CRD monitoring started the required-watch reconciler")
	}
	d.StartWatchReconciler()
	waitUntilTrue(t, "explicit start after late CRD monitoring", func() bool { return d.IsSynced(deniedWidgets) })
}

func TestWatchReconcilerRetriesDenialAfterGrant(t *testing.T) {
	var calls atomic.Int64
	var granted atomic.Bool
	d, err := NewDynamicResourceCache(DynamicCacheConfig{Discovery: widgetDiscovery(t), RequiredWatchKinds: []schema.GroupKind{{Group: "example.io", Kind: "Widget"}}, DynamicClient: countedWidgetLists(&calls, func(ns string) error {
		if granted.Load() {
			return nil
		}
		return forbidden(ns)
	})})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Stop()
	d.startWatchReconciler(10 * time.Millisecond)
	waitUntilTrue(t, "initial denial", func() bool { return d.Observation(deniedWidgets).State == DynamicObservationDenied })
	granted.Store(true)
	for range 100 {
		d.requestWatch(deniedWidgets)
	}
	time.Sleep(40 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("denial memory did not rate-limit requests: %d lists", got)
	}
	expireDenials(d)
	start := time.Now()
	waitUntilTrue(t, "watch after grant", func() bool { return d.IsSynced(deniedWidgets) })
	if time.Since(start) > 2*time.Second {
		t.Fatal("activation exceeded the 2s test bound")
	}
}

func TestWatchReconcilerDiscoveryRefreshAndReplacement(t *testing.T) {
	discovery, err := NewResourceDiscovery(newLockedDiscovery())
	if err != nil {
		t.Fatal(err)
	}
	newCache := func(namespace string) *DynamicResourceCache {
		d, err := NewDynamicResourceCache(DynamicCacheConfig{Discovery: discovery, RequiredWatchKinds: []schema.GroupKind{{Group: "example.io", Kind: "Widget"}}, DynamicClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{deniedWidgets: "WidgetList"}), NamespaceScoped: namespace != "", Namespace: namespace})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(d.Stop)
		d.StartWatchReconciler()
		return d
	}
	old := newCache("")
	old.Stop()
	waitUntilTrue(t, "old discovery subscription removed", func() bool {
		discovery.refreshListenersMu.Lock()
		defer discovery.refreshListenersMu.Unlock()
		return len(discovery.refreshListeners) == 0
	})
	current := newCache("team-b")
	discovery.AddAPIResource(APIResource{Group: "example.io", Version: "v1", Name: "widgets", Kind: "Widget", Namespaced: true, Verbs: []string{"get", "list", "watch"}})
	waitUntilTrue(t, "replacement cache watches newly discovered kind", func() bool { return current.IsSynced(deniedWidgets) })
	if old.GetInformerCount() != 0 {
		t.Fatal("stale cache started a watch")
	}
	o := current.Observation(deniedWidgets)
	if len(o.Namespaces) != 1 || o.Namespaces[0] != "team-b" {
		t.Fatalf("rescope ignored: %+v", o)
	}
}

func TestWatchReconcilerBypassesLargeKindDeferral(t *testing.T) {
	var objects []runtime.Object
	for i := range 150 {
		objects = append(objects, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "example.io/v1", "kind": "Widget", "metadata": map[string]any{"name": fmt.Sprintf("widget-%d", i), "namespace": "team"}}})
	}
	d, err := NewDynamicResourceCache(DynamicCacheConfig{Discovery: widgetDiscovery(t), RequiredWatchKinds: []schema.GroupKind{{Group: "example.io", Kind: "Widget"}}, DynamicClient: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{deniedWidgets: "WidgetList"}, objects...)})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Stop()
	stats := d.watchSmallCRDs([]schema.GroupVersionResource{deniedWidgets})
	if stats.deferred != 0 {
		t.Fatalf("required kind was deferred: %+v", stats)
	}
	waitUntilTrue(t, "required kind watch", func() bool { return d.IsSynced(deniedWidgets) })
}

func TestWatchReconcilerCRDAddAndUpdate(t *testing.T) {
	oldDelay := lateCRDScanDelay
	lateCRDScanDelay = 10 * time.Millisecond
	t.Cleanup(func() { lateCRDScanDelay = oldDelay })
	api := newLockedDiscovery(crdResources)
	discovery, err := NewResourceDiscovery(api)
	if err != nil {
		t.Fatal(err)
	}
	v2 := deniedWidgets
	v2.Version = "v2"
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{deniedWidgets: "WidgetList", v2: "WidgetList", crdGVR: "CustomResourceDefinitionList"})
	d, err := NewDynamicResourceCache(DynamicCacheConfig{Discovery: discovery, DynamicClient: client, RequiredWatchKinds: []schema.GroupKind{{Group: "example.io", Kind: "Widget"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Stop()
	d.StartWatchReconciler()
	waitUntilTrue(t, "early CRD informer", func() bool { return d.IsSynced(crdGVR) })
	api.serve(&metav1.APIResourceList{GroupVersion: "example.io/v1", APIResources: []metav1.APIResource{{Name: "widgets", Kind: "Widget", Namespaced: true, Verbs: []string{"get", "list", "watch"}}}})
	crd := crdObject("widgets.example.io", "example.io", "widgets")
	created, err := client.Resource(crdGVR).Create(context.Background(), crd, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	waitUntilTrue(t, "CRD add activates watch", func() bool { return d.IsSynced(deniedWidgets) })
	api.serve(&metav1.APIResourceList{GroupVersion: "example.io/v2", APIResources: []metav1.APIResource{{Name: "widgets", Kind: "Widget", Namespaced: true, Verbs: []string{"get", "list", "watch"}}}})
	created.SetResourceVersion("2")
	if err := unstructured.SetNestedSlice(created.Object, []any{map[string]any{"name": "v2", "served": true}}, "spec", "versions"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Resource(crdGVR).Update(context.Background(), created, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	waitUntilTrue(t, "CRD update activates new served version", func() bool { return d.IsSynced(v2) })
}

func TestRequestWatchIsNonBlockingAndSingleflight(t *testing.T) {
	var calls atomic.Int64
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	d, err := NewDynamicResourceCache(DynamicCacheConfig{Discovery: widgetDiscovery(t), DynamicClient: countedWidgetLists(&calls, func(string) error { <-release; return nil })})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Stop()
	done := make(chan struct{})
	go func() {
		for range 100 {
			d.requestWatch(deniedWidgets)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("requestWatch blocked on the probe")
	}
	waitUntilTrue(t, "one outstanding probe", func() bool { return calls.Load() > 0 })
	if got := calls.Load(); got != 1 {
		t.Fatalf("parallel requests made %d probes", got)
	}
	unblock()
	waitUntilTrue(t, "requested watch sync", func() bool { return d.IsSynced(deniedWidgets) })
}
