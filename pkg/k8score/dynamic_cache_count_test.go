package k8score

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestDynamicResourceCache_CountDirectProbeUsesRemainingCount(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "EndpointSliceList"},
	)
	dyn.PrependReactor("list", "endpointslices", func(action k8stesting.Action) (bool, runtime.Object, error) {
		remaining := int64(7)
		list := &unstructured.UnstructuredList{
			Items: []unstructured.Unstructured{{}, {}},
		}
		list.SetRemainingItemCount(&remaining)
		return true, list, nil
	})

	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn})
	if err != nil {
		t.Fatalf("NewDynamicResourceCache failed: %v", err)
	}

	got, err := d.CountDirectProbe(context.Background(), gvr, nil, 50, 8)
	if err != nil {
		t.Fatalf("CountDirectProbe failed: %v", err)
	}
	if got != 9 {
		t.Fatalf("CountDirectProbe = %d, want 9", got)
	}
}

func TestDynamicResourceCache_CountDirectProbeExactWhenServerReturnsCompleteList(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "EndpointSliceList"},
	)
	dyn.PrependReactor("list", "endpointslices", func(action k8stesting.Action) (bool, runtime.Object, error) {
		list := &unstructured.UnstructuredList{
			Items: []unstructured.Unstructured{{}},
		}
		return true, list, nil
	})

	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn})
	if err != nil {
		t.Fatalf("NewDynamicResourceCache failed: %v", err)
	}

	got, err := d.CountDirectProbe(context.Background(), gvr, nil, 50, 8)
	if err != nil {
		t.Fatalf("CountDirectProbe failed: %v", err)
	}
	if got != 1 {
		t.Fatalf("CountDirectProbe = %d, want 1", got)
	}
}

func TestDynamicResourceCache_CountDirectProbeUnavailableWithoutRemainingCount(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "EndpointSliceList"},
	)
	dyn.PrependReactor("list", "endpointslices", func(action k8stesting.Action) (bool, runtime.Object, error) {
		list := &unstructured.UnstructuredList{
			Items: []unstructured.Unstructured{{}, {}},
		}
		list.SetContinue("next")
		return true, list, nil
	})

	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn})
	if err != nil {
		t.Fatalf("NewDynamicResourceCache failed: %v", err)
	}

	_, err = d.CountDirectProbe(context.Background(), gvr, nil, 50, 8)
	if !errors.Is(err, ErrResourceCountUnavailable) {
		t.Fatalf("CountDirectProbe error = %v, want ErrResourceCountUnavailable", err)
	}
}

func TestDynamicResourceCache_ProbeCountClusterScopedStaysClusterWide(t *testing.T) {
	// Under --namespace-scope, counting a cluster-scoped CRD must stay cluster-wide:
	// a namespaced list of a cluster-scoped resource returns nothing and would
	// misgate the size-based eager-warm decision.
	gvr := schema.GroupVersionResource{Group: "karpenter.sh", Version: "v1", Resource: "nodepools"}
	disc := &ResourceDiscovery{
		resources: []APIResource{{
			Group: gvr.Group, Version: gvr.Version, Kind: "NodePool", Name: gvr.Resource,
			Namespaced: false, IsCRD: true, Verbs: []string{"get", "list", "watch"},
		}},
		resourceMap: map[string]APIResource{},
		gvrMap:      map[string]schema.GroupVersionResource{},
		lastRefresh: time.Now(),
		cacheTTL:    time.Hour,
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "NodePoolList"},
	)
	dyn.PrependReactor("list", "nodepools", func(action k8stesting.Action) (bool, runtime.Object, error) {
		// Any namespaced list returns empty, so the test fails if the pin guard
		// regresses and counts a cluster-scoped resource inside the namespace.
		if action.GetNamespace() != "" {
			return true, &unstructured.UnstructuredList{}, nil
		}
		remaining := int64(11)
		list := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{{}}}
		list.SetRemainingItemCount(&remaining)
		return true, list, nil
	})

	d, err := NewDynamicResourceCache(DynamicCacheConfig{
		DynamicClient:   dyn,
		Discovery:       disc,
		NamespaceScoped: true,
		Namespace:       "foo",
	})
	if err != nil {
		t.Fatalf("NewDynamicResourceCache failed: %v", err)
	}

	if got := d.ProbeCount(gvr); got != 12 {
		t.Fatalf("ProbeCount(cluster-scoped under --namespace-scope) = %d, want 12 (cluster-wide)", got)
	}
}

func TestDynamicResourceCache_ProbeCountUsesRemainingCount(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "WidgetList"},
	)
	dyn.PrependReactor("list", "widgets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		remaining := int64(41)
		list := &unstructured.UnstructuredList{
			Items: []unstructured.Unstructured{{}},
		}
		list.SetRemainingItemCount(&remaining)
		list.SetContinue("next")
		return true, list, nil
	})

	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn})
	if err != nil {
		t.Fatalf("NewDynamicResourceCache failed: %v", err)
	}

	if got := d.ProbeCount(gvr); got != 42 {
		t.Fatalf("ProbeCount = %d, want 42", got)
	}
}

func TestDynamicResourceCache_ProbeCountDefersWithoutRemainingCount(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "WidgetList"},
	)
	dyn.PrependReactor("list", "widgets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		list := &unstructured.UnstructuredList{
			Items: []unstructured.Unstructured{{}},
		}
		list.SetContinue("next")
		return true, list, nil
	})

	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn})
	if err != nil {
		t.Fatalf("NewDynamicResourceCache failed: %v", err)
	}

	if got := d.ProbeCount(gvr); got != -2 {
		t.Fatalf("ProbeCount = %d, want -2", got)
	}
}

func TestDynamicResourceCache_CountDirectProbeSumsNamespacesAndCapsFanout(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "EndpointSliceList"},
	)
	remainingByNamespace := map[string]int64{
		"team-a": 3,
		"team-b": 1,
	}
	var seenMu sync.Mutex
	seen := map[string]bool{}
	dyn.PrependReactor("list", "endpointslices", func(action k8stesting.Action) (bool, runtime.Object, error) {
		listAction := action.(k8stesting.ListAction)
		ns := listAction.GetNamespace()
		seenMu.Lock()
		seen[ns] = true
		seenMu.Unlock()
		remaining := remainingByNamespace[ns]
		list := &unstructured.UnstructuredList{
			Items: []unstructured.Unstructured{{}, {}},
		}
		list.SetRemainingItemCount(&remaining)
		return true, list, nil
	})

	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn})
	if err != nil {
		t.Fatalf("NewDynamicResourceCache failed: %v", err)
	}

	got, err := d.CountDirectProbe(context.Background(), gvr, []string{"team-a", "team-b"}, 2, 2)
	if err != nil {
		t.Fatalf("CountDirectProbe failed: %v", err)
	}
	if got != 8 {
		t.Fatalf("CountDirectProbe = %d, want 8", got)
	}
	for _, ns := range []string{"team-a", "team-b"} {
		if !seen[ns] {
			t.Fatalf("namespace %q was not probed", ns)
		}
	}

	_, err = d.CountDirectProbe(context.Background(), gvr, []string{"team-a", "team-b"}, 1, 2)
	if !errors.Is(err, ErrResourceCountUnavailable) {
		t.Fatalf("over-cap CountDirectProbe error = %v, want ErrResourceCountUnavailable", err)
	}
}

func TestDynamicResourceCache_CountWatchedRespectsInformerScope(t *testing.T) {
	const nsA, nsB = "team-a", "team-b"
	gvr := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
	dyn := fakeDynamicForListAccess(t, map[schema.GroupVersionResource]string{
		gvr: "WidgetList",
	}, func(_ schema.GroupVersionResource, namespace string) bool {
		return namespace == nsA || namespace == nsB
	})
	for _, ns := range []string{nsA, nsB} {
		obj := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "example.com/v1",
			"kind":       "Widget",
			"metadata":   map[string]any{"name": "w-" + ns, "namespace": ns},
		}}
		if _, err := dyn.Resource(gvr).Namespace(ns).Create(context.Background(), obj, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seed %s: %v", ns, err)
		}
	}

	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn})
	if err != nil {
		t.Fatalf("NewDynamicResourceCache failed: %v", err)
	}
	for _, ns := range []string{nsA, nsB} {
		if _, err := d.ListBlocking(gvr, ns, 2*time.Second); err != nil {
			t.Fatalf("ListBlocking(%q) failed: %v", ns, err)
		}
	}

	all := d.CountWatched(nil)
	if _, ok := all[gvr]; ok {
		t.Fatalf("CountWatched(nil)[gvr] returned a partial namespace-scoped count: %v", all[gvr])
	}
	filtered := d.CountWatched([]string{nsA})
	if got := filtered[gvr]; got != 1 {
		t.Fatalf("CountWatched([%s])[gvr] = %d, want 1", nsA, got)
	}
	multiNamespace := d.CountWatched([]string{nsA, nsB})
	if got := multiNamespace[gvr]; got != 2 {
		t.Fatalf("CountWatched([%s,%s])[gvr] = %d, want 2", nsA, nsB, got)
	}
	partial := d.CountWatched([]string{nsA, "team-c"})
	if _, ok := partial[gvr]; ok {
		t.Fatalf("CountWatched returned partial count for missing namespace informer: %v", partial[gvr])
	}
}
