package k8score

import (
	"context"
	"errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
	"sync/atomic"
	"testing"
	"time"
)

func TestCompleteReadNeverReturnsColdEmpty(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "test.example", Version: "v1", Resource: "widgets"}
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "test.example/v1", "kind": "Widget", "metadata": map[string]any{"name": "one", "namespace": "a"}}}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"}, obj)
	var calls atomic.Int32
	client.PrependReactor("list", "widgets", func(action ktesting.Action) (bool, runtime.Object, error) {
		if calls.Add(1) > 1 {
			time.Sleep(100 * time.Millisecond)
		}
		return false, nil, nil
	})
	dc, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: client})
	if err != nil {
		t.Fatal(err)
	}
	defer dc.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	_, err = dc.ListComplete(ctx, gvr, []string{"a"})
	cancel()
	var pending *ResourceReadError
	if !errors.As(err, &pending) || pending.Code != "kind_sync_pending" {
		t.Fatalf("cold read = %v", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got, err := dc.ListComplete(ctx, gvr, []string{"a"})
	if err != nil || len(got) != 1 {
		t.Fatalf("warm read = %v, %v", got, err)
	}
}
