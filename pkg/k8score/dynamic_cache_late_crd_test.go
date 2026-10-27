package k8score

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakediscovery "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	fakeclientset "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// lockedDiscovery lets a test change the served resources while Radar
// refreshes discovery in the background.
type lockedDiscovery struct {
	*fakediscovery.FakeDiscovery
	mu sync.Mutex
}

func newLockedDiscovery(resources ...*metav1.APIResourceList) *lockedDiscovery {
	fake := fakeclientset.NewSimpleClientset().Discovery().(*fakediscovery.FakeDiscovery)
	fake.Resources = resources
	return &lockedDiscovery{FakeDiscovery: fake}
}

func (d *lockedDiscovery) serve(list *metav1.APIResourceList) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Resources = append(d.Resources, list)
}

func (d *lockedDiscovery) ServerGroupsAndResources() ([]*metav1.APIGroup, []*metav1.APIResourceList, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.FakeDiscovery.ServerGroupsAndResources()
}

var crdResources = &metav1.APIResourceList{
	GroupVersion: "apiextensions.k8s.io/v1",
	APIResources: []metav1.APIResource{{Name: "customresourcedefinitions", Kind: "CustomResourceDefinition", Verbs: []string{"get", "list", "watch"}}},
}

var dbClusterResources = &metav1.APIResourceList{
	GroupVersion: "db.example.com/v1alpha1",
	APIResources: []metav1.APIResource{{Name: "dbclusters", Kind: "DBCluster", Namespaced: true, Verbs: []string{"get", "list", "watch"}}},
}

func crdObject(name, group, plural string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apiextensions.k8s.io/v1",
		"kind":       "CustomResourceDefinition",
		"metadata":   map[string]any{"name": name},
		"spec": map[string]any{
			"group": group,
			"names": map[string]any{"plural": plural},
		},
	}}
}

func waitUntilTrue(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A CRD installed after startup discovery gets the same decision as one that
// was there at startup: watched eagerly when it has few objects, so its
// changes reach the timeline before anyone lists the kind.
func TestCRDInstalledAfterDiscoveryIsWatched(t *testing.T) {
	old := lateCRDScanDelay
	lateCRDScanDelay = 50 * time.Millisecond
	t.Cleanup(func() { lateCRDScanDelay = old })

	widgets := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
	clusters := schema.GroupVersionResource{Group: "db.example.com", Version: "v1alpha1", Resource: "dbclusters"}

	fakeDiscovery := newLockedDiscovery(crdResources, &metav1.APIResourceList{
		GroupVersion: "example.com/v1",
		APIResources: []metav1.APIResource{{Name: "widgets", Kind: "Widget", Namespaced: true, Verbs: []string{"get", "list", "watch"}}},
	})
	discovery, err := NewResourceDiscovery(fakeDiscovery, WithDiscoveryCacheTTL(time.Hour))
	if err != nil {
		t.Fatalf("NewResourceDiscovery: %v", err)
	}
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			crdGVR:   "CustomResourceDefinitionList",
			widgets:  "WidgetList",
			clusters: "DBClusterList",
		},
		crdObject("widgets.example.com", "example.com", "widgets"),
	)
	var (
		changesMu sync.Mutex
		changes   []ResourceChange
	)
	cache, err := NewDynamicResourceCache(DynamicCacheConfig{
		DynamicClient: dynamicClient,
		Discovery:     discovery,
		OnChange: func(c ResourceChange, _, _ any) {
			changesMu.Lock()
			changes = append(changes, c)
			changesMu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("NewDynamicResourceCache: %v", err)
	}
	t.Cleanup(cache.Stop)

	cache.DiscoverAllCRDs()
	select {
	case <-cache.discoveryDone:
	case <-time.After(5 * time.Second):
		t.Fatal("DiscoverAllCRDs did not complete")
	}
	waitUntilTrue(t, "startup CRD watched", func() bool { return cache.hasCoveringInformer(widgets, "") })
	if cache.hasCoveringInformer(clusters, "") {
		t.Fatal("dbclusters watched before its CRD was installed")
	}

	// Install the CRD: the API server starts serving the kind, then the CRD
	// object appears.
	fakeDiscovery.serve(dbClusterResources)
	if _, err := dynamicClient.Resource(crdGVR).Create(context.Background(),
		crdObject("dbclusters.db.example.com", "db.example.com", "dbclusters"), metav1.CreateOptions{}); err != nil {
		t.Fatalf("create CRD: %v", err)
	}

	waitUntilTrue(t, "late-installed CRD watched", func() bool { return cache.hasCoveringInformer(clusters, "") })
	if !cache.WaitForSync(clusters, 5*time.Second) {
		t.Fatal("late-installed CRD informer did not sync")
	}

	// An edit to one of its objects reaches the timeline callback without
	// anyone having listed the kind.
	cr := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "db.example.com/v1alpha1", "kind": "DBCluster",
		"metadata": map[string]any{"name": "basic", "namespace": "default"},
		"spec":     map[string]any{"updateStrategy": "RollingUpdate"},
	}}
	created, err := dynamicClient.Resource(clusters).Namespace("default").Create(context.Background(), cr, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create CR: %v", err)
	}
	if err := unstructured.SetNestedField(created.Object, "SomeStrategyForUpdate", "spec", "updateStrategy"); err != nil {
		t.Fatal(err)
	}
	if _, err := dynamicClient.Resource(clusters).Namespace("default").Update(context.Background(), created, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update CR: %v", err)
	}
	waitUntilTrue(t, "CR update recorded", func() bool {
		changesMu.Lock()
		defer changesMu.Unlock()
		for _, c := range changes {
			if c.Kind == "DBCluster" && c.Name == "basic" && c.Operation == OpUpdate {
				return true
			}
		}
		return false
	})
}

// A CRD event can arrive before discovery serves the new kind (the CRD isn't
// Established yet, or discovery failed). The scan is retried until it is.
func TestLateCRDScanRetriesUntilServed(t *testing.T) {
	old := lateCRDScanDelay
	lateCRDScanDelay = 20 * time.Millisecond
	t.Cleanup(func() { lateCRDScanDelay = old })

	clusters := schema.GroupVersionResource{Group: "db.example.com", Version: "v1alpha1", Resource: "dbclusters"}
	fakeDiscovery := newLockedDiscovery(crdResources)
	discovery, err := NewResourceDiscovery(fakeDiscovery, WithDiscoveryCacheTTL(time.Hour))
	if err != nil {
		t.Fatalf("NewResourceDiscovery: %v", err)
	}
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{crdGVR: "CustomResourceDefinitionList", clusters: "DBClusterList"},
		crdObject("dbclusters.db.example.com", "db.example.com", "dbclusters"),
	)
	cache, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dynamicClient, Discovery: discovery})
	if err != nil {
		t.Fatalf("NewDynamicResourceCache: %v", err)
	}
	t.Cleanup(cache.Stop)

	cache.DiscoverAllCRDs()
	select {
	case <-cache.discoveryDone:
	case <-time.After(5 * time.Second):
		t.Fatal("DiscoverAllCRDs did not complete")
	}
	// The CRD object exists but discovery doesn't serve its kind yet.
	waitUntilTrue(t, "CRD pending", func() bool {
		cache.lateCRDMu.Lock()
		defer cache.lateCRDMu.Unlock()
		_, pending := cache.pendingCRDs["db.example.com/dbclusters"]
		return pending
	})
	time.Sleep(100 * time.Millisecond) // at least one scan that finds nothing
	if cache.hasCoveringInformer(clusters, "") {
		t.Fatal("watched before discovery served the kind")
	}

	fakeDiscovery.serve(dbClusterResources)
	waitUntilTrue(t, "CRD watched after a retry", func() bool { return cache.hasCoveringInformer(clusters, "") })
}

func TestLateCRDCountFailureIsRetried(t *testing.T) {
	old := lateCRDScanDelay
	lateCRDScanDelay = 20 * time.Millisecond
	t.Cleanup(func() { lateCRDScanDelay = old })

	clusters := schema.GroupVersionResource{Group: "db.example.com", Version: "v1alpha1", Resource: "dbclusters"}
	discovery, err := NewResourceDiscovery(newLockedDiscovery(crdResources, dbClusterResources), WithDiscoveryCacheTTL(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{clusters: "DBClusterList"})
	var attempts atomic.Int32
	dynamicClient.PrependReactor("list", "dbclusters", func(k8stesting.Action) (bool, runtime.Object, error) {
		if attempts.Add(1) == 1 {
			return true, nil, apierrors.NewNotFound(clusters.GroupResource(), "")
		}
		return false, nil, nil
	})
	cache, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dynamicClient, Discovery: discovery})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cache.Stop)

	cache.onCRDEvent(crdObject("dbclusters.db.example.com", "db.example.com", "dbclusters"))
	waitUntilTrue(t, "CRD watched after transient count failure", func() bool {
		return cache.hasCoveringInformer(clusters, "")
	})
	if !cache.WaitForSync(clusters, 5*time.Second) {
		t.Fatal("late CRD informer did not sync")
	}
	waitUntilTrue(t, "retry scan complete", func() bool {
		cache.lateCRDMu.Lock()
		defer cache.lateCRDMu.Unlock()
		_, seen := cache.seenCRDs["db.example.com/dbclusters"]
		return seen && len(cache.pendingCRDs) == 0 && !cache.lateCRDScanning
	})
}

func TestLateCRDScanOnlyHandlesNewCRDs(t *testing.T) {
	cache := &DynamicResourceCache{seenCRDs: map[string]struct{}{}, pendingCRDs: map[string]struct{}{}}

	if fresh := cache.markCRDsSeen([]string{"a.io/as"}); len(fresh) != 1 {
		t.Fatalf("first sight of a: got %v", fresh)
	}
	cache.pendingCRDs["b.io/bs"] = struct{}{}
	fresh := cache.markCRDsSeen([]string{"a.io/as", "b.io/bs"})
	if _, ok := fresh["b.io/bs"]; !ok || len(fresh) != 1 {
		t.Fatalf("expected only b to be new, got %v", fresh)
	}
	if len(cache.pendingCRDs) != 0 {
		t.Fatalf("seen CRD still pending: %v", cache.pendingCRDs)
	}

	// After the CRD is deleted, reinstalling it is handled again.
	cache.forgetCRD(crdObject("as.a.io", "a.io", "as"))
	if fresh := cache.markCRDsSeen([]string{"a.io/as"}); len(fresh) != 1 {
		t.Fatalf("reinstalled a should be new again, got %v", fresh)
	}
}

// Updates to CRDs that already had a watch decision must not refresh
// discovery: some controllers rewrite CRDs regularly.
func TestKnownCRDEventsDoNotScheduleScan(t *testing.T) {
	old := lateCRDScanDelay
	lateCRDScanDelay = time.Hour
	t.Cleanup(func() { lateCRDScanDelay = old })

	cache := &DynamicResourceCache{seenCRDs: map[string]struct{}{"a.io/as": {}}, pendingCRDs: map[string]struct{}{}}
	cache.onCRDEvent(crdObject("as.a.io", "a.io", "as"))
	if cache.lateCRDTimer != nil {
		t.Fatal("event for a known CRD scheduled a scan")
	}
	cache.lateCRDRetries = lateCRDMaxRetries
	cache.onCRDEvent(crdObject("bs.b.io", "b.io", "bs"))
	if cache.lateCRDTimer == nil {
		t.Fatal("event for a new CRD did not schedule a scan")
	}
	if cache.lateCRDRetries != 0 {
		t.Fatal("a newly pending CRD did not get the full retry budget")
	}
	cache.lateCRDTimer.Stop()
}
