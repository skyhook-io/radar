package k8score

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestDynamicResourceCache_ListDirectSelectedPassesOptionsAndReportsContinue(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "EndpointSliceList"},
	)
	var seen k8stesting.ListActionImpl
	dyn.PrependReactor("list", "endpointslices", func(action k8stesting.Action) (bool, runtime.Object, error) {
		seen = action.(k8stesting.ListActionImpl)
		item := unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "discovery.k8s.io/v1",
			"kind":       "EndpointSlice",
			"metadata": map[string]any{
				"name":          "web-abc",
				"namespace":     "team",
				"labels":        map[string]any{"kubernetes.io/service-name": "web"},
				"managedFields": []any{map[string]any{"manager": "kube-controller-manager"}},
			},
		}}
		list := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{item}}
		list.SetContinue("next")
		return true, list, nil
	})

	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn})
	if err != nil {
		t.Fatalf("NewDynamicResourceCache failed: %v", err)
	}
	items, truncated, err := d.ListDirectSelected(context.Background(), gvr, "team", "kubernetes.io/service-name=web", 500)
	if err != nil {
		t.Fatalf("ListDirectSelected failed: %v", err)
	}
	if !truncated {
		t.Fatal("truncated = false despite a continue token")
	}
	if len(items) != 1 || items[0].GetManagedFields() != nil {
		t.Fatalf("items = %v, want one item with managedFields stripped", items)
	}
	if seen.GetNamespace() != "team" || seen.ListOptions.LabelSelector != "kubernetes.io/service-name=web" || seen.ListOptions.Limit != 500 {
		t.Fatalf("list action = ns %q selector %q limit %d", seen.GetNamespace(), seen.ListOptions.LabelSelector, seen.ListOptions.Limit)
	}
}
