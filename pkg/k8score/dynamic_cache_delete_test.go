package k8score

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"
)

func TestDynamicDeleteTombstonePassesUnwrappedObjectToCallbacks(t *testing.T) {
	gvr := schema.GroupVersionResource{
		Group: "example.io", Version: "v1", Resource: "widgets",
	}
	deleted := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.io/v1",
		"kind":       "Widget",
		"metadata": map[string]any{
			"namespace": "shop",
			"name":      "checkout",
			"uid":       "widget-uid",
		},
	}}

	var callbackObject any
	dynamicCache := &DynamicResourceCache{config: DynamicCacheConfig{
		OnChange: func(_ ResourceChange, obj, _ any) {
			callbackObject = obj
		},
	}}
	dynamicCache.enqueueDynamicChange(
		"Widget",
		gvr,
		cache.DeletedFinalStateUnknown{Key: "shop/checkout", Obj: deleted},
		nil,
		OpDelete,
	)

	if callbackObject != deleted {
		t.Fatalf("OnChange object = %T %p, want unwrapped object %p", callbackObject, callbackObject, deleted)
	}
}
