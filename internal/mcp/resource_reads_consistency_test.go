package mcp

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/k8s"
)

// A kind Radar first watched namespace by namespace is promoted to one
// cluster-wide informer when its identity gains cluster-wide list. A read that
// lands on the replacement before it has loaded must not stand as complete,
// even though the scope reads as synced both before and after it.
func TestDynamicReadRepeatsWhenItsWatchIsReplacedMidRead(t *testing.T) {
	if err := k8s.InitTestResourceCache(fake.NewClientset()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	gvr := schema.GroupVersionResource{Group: "example.test", Version: "v1", Resource: "widgets"}
	present := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.test/v1", "kind": "Widget",
		"metadata": map[string]any{"name": "present", "namespace": "alpha"},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"}, present)
	// Cluster-wide list is refused until promote is set; while hold is set,
	// the cluster-wide informer's initial list fails, so it serves an empty
	// store until hold clears.
	var promote, hold atomic.Bool
	var clusterLists atomic.Int32
	dyn.PrependReactor("list", "widgets", func(a ktesting.Action) (bool, runtime.Object, error) {
		if a.GetNamespace() != "" {
			return false, nil, nil
		}
		if !promote.Load() {
			return true, nil, collectorRefusal(gvr.GroupResource(), "list", "")
		}
		if clusterLists.Add(1) > 1 && hold.Load() {
			return true, nil, fmt.Errorf("initial list held")
		}
		return false, nil, nil
	})
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{
		Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: "Widget", Namespaced: true,
		Verbs: []string{"get", "list", "watch"},
	}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	d := k8s.GetDynamicResourceCache()
	if _, err := d.List(gvr, "alpha"); err != nil {
		t.Fatal(err)
	}
	if !d.WaitForSync(gvr, 5*time.Second) {
		t.Fatal("the namespace informer did not sync")
	}

	reads := 0
	items, err := readDynamicScope(context.Background(), "widgets", gvr.Group, "alpha", "list", func() ([]*unstructured.Unstructured, error) {
		reads++
		if reads > 1 {
			return d.List(gvr, "alpha")
		}
		promote.Store(true)
		hold.Store(true)
		if err := d.EnsureWatching(gvr); err != nil {
			t.Fatal(err)
		}
		rows, err := d.List(gvr, "alpha")
		if err != nil || len(rows) != 0 || d.IsNamespaceSynced(gvr, "alpha") {
			t.Fatalf("scenario: the first read should land on an unloaded replacement, got %d rows, err %v, synced %v", len(rows), err, d.IsNamespaceSynced(gvr, "alpha"))
		}
		hold.Store(false)
		if !d.WaitForSync(gvr, 5*time.Second) {
			t.Fatal("the replacement did not sync")
		}
		return rows, nil
	})
	if err != nil || len(items) != 1 || reads != 2 {
		t.Fatalf("got %d rows, err %v, after %d reads; want the loaded replacement's 1 row from a second read", len(items), err, reads)
	}
}
