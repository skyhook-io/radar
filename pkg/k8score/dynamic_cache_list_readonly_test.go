package k8score

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

// ListReadOnly hands back the cached objects themselves, already stripped by
// the informer transform; List still hands back copies.
func TestListReadOnly_ReturnsCachedObjectsWithoutCopying(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
	w := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.com/v1", "kind": "Widget",
		"metadata": map[string]any{
			"name": "w", "namespace": "ns",
			"managedFields": []any{map[string]any{"manager": "kubectl"}},
			"annotations":   map[string]any{lastAppliedAnnotation: "{}", "keep": "yes"},
		},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"}, w)
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Stop()
	if _, err := d.ListReadOnly(gvr, ""); err != nil {
		t.Fatalf("first read: %v", err)
	}
	if !d.WaitForSync(gvr, 2*time.Second) {
		t.Fatal("never synced")
	}

	shared1, err := d.ListReadOnly(gvr, "")
	if err != nil || len(shared1) != 1 {
		t.Fatalf("ListReadOnly = %v, %v", shared1, err)
	}
	shared2, _ := d.ListReadOnly(gvr, "")
	if shared1[0] != shared2[0] {
		t.Fatal("ListReadOnly copied the object")
	}
	copied, _ := d.List(gvr, "")
	if copied[0] == shared1[0] {
		t.Fatal("List stopped copying")
	}
	got := shared1[0]
	if _, found, _ := unstructured.NestedFieldNoCopy(got.Object, "metadata", "managedFields"); found {
		t.Error("managedFields reached a ListReadOnly reader")
	}
	if ann := got.GetAnnotations(); ann[lastAppliedAnnotation] != "" || ann["keep"] != "yes" {
		t.Errorf("annotations = %v", ann)
	}
}

func TestListBlockingReadOnlyContext_ReturnsCachedObjects(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
	w := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.com/v1", "kind": "Widget",
		"metadata": map[string]any{"name": "w", "namespace": "ns"},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"}, w)
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Stop()
	first, err := d.ListBlockingReadOnlyContext(context.Background(), gvr, "", 2*time.Second)
	if err != nil || len(first) != 1 {
		t.Fatalf("ListBlockingReadOnlyContext = %v, %v", first, err)
	}
	again, _ := d.ListBlockingReadOnlyContext(context.Background(), gvr, "", 0)
	if first[0] != again[0] {
		t.Fatal("ListBlockingReadOnlyContext copied the object")
	}
	if copied, _ := d.ListBlocking(gvr, "", 0); copied[0] == first[0] {
		t.Fatal("ListBlocking stopped copying")
	}
}
