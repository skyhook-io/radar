package k8score

import (
	"errors"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

// IsSynced answers "is SOME informer for this GVR synced", which is true as soon
// as any one namespace has been watched. A caller reading a different namespace
// that passes that gate starts a fresh informer on its first List and reads an
// empty indexer with no error — an absence the cache never established. For a
// screen that says "no cluster uses this catalog", that is the whole bug.
func TestIsNamespaceSynced_DoesNotBorrowAnotherNamespacesInformer(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
	dyn := fakeDynamicForListAccess(t, map[schema.GroupVersionResource]string{
		gvr: "WidgetList",
	}, func(schema.GroupVersionResource, string) bool { return true })
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn})
	if err != nil {
		t.Fatalf("NewDynamicResourceCache failed: %v", err)
	}
	defer d.Stop()

	if err := d.startWatching(gvr, "ns-a"); err != nil {
		t.Fatalf("startWatching(ns-a): %v", err)
	}
	if !d.WaitForSync(gvr, 2*time.Second) {
		t.Fatal("ns-a informer never synced")
	}

	// The state that makes the difference visible: one namespace watched.
	if !d.IsSynced(gvr) {
		t.Fatal("IsSynced should be true once any informer synced; test premise broken")
	}
	if !d.IsNamespaceSynced(gvr, "ns-a") {
		t.Error("the namespace that IS watched must report synced")
	}
	if d.IsNamespaceSynced(gvr, "ns-b") {
		t.Error("ns-b has no informer, so the cache cannot answer for it — reporting synced " +
			"lets a caller read an empty list and call it an absence")
	}
	// Cluster-wide is a stronger claim still: namespace-scoped coverage is not it.
	if d.IsNamespaceSynced(gvr, "") {
		t.Error("a namespace-scoped cache cannot license a cluster-wide absence")
	}
}

// A read served by several namespace informers waits once, not once per
// informer: three stalled informers must not triple the caller's timeout.
func TestListBlocking_TimeoutCoversEveryInformer(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "WidgetList"},
	)
	dyn.PrependReactor("list", "widgets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetNamespace() == "" {
			return true, nil, apierrors.NewForbidden(gvr.GroupResource(), "", errors.New("denied"))
		}
		return true, nil, errors.New("apiserver hiccup")
	})
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn, NamespaceFallbacks: []string{"x", "y", "z"}})
	if err != nil {
		t.Fatalf("NewDynamicResourceCache failed: %v", err)
	}
	defer d.Stop()

	const timeout = 500 * time.Millisecond
	start := time.Now()
	_, _ = d.ListBlocking(gvr, "", timeout)
	if elapsed := time.Since(start); elapsed > 2*timeout {
		t.Fatalf("ListBlocking took %v with a %v timeout over three stalled informers", elapsed, timeout)
	}
	if d.IsNamespaceSynced(gvr, "x") {
		t.Fatal("test premise broken: the x informer synced")
	}
}
