package k8score

import (
	"context"
	"errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

func TestConclusiveAbsentAPINeverStartsInformerOrProbes(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "absent.example", Version: "v1", Resource: "widgets"}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"})
	dc, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: client, Discovery: &ResourceDiscovery{initialized: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer dc.Stop()
	_, err = dc.ListComplete(context.Background(), gvr, nil)
	var readErr *ResourceReadError
	if !errors.As(err, &readErr) || readErr.Code != "kind_not_served" {
		t.Fatalf("err=%v", err)
	}
	if len(client.Actions()) != 0 || len(dc.informers) != 0 {
		t.Fatal("probed or started informer for a conclusively absent endpoint")
	}
}

func TestCallerCancellationDoesNotCancelSharedScopeProbe(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "test.example", Version: "v1", Resource: "widgets"}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"})
	started, release := make(chan struct{}), make(chan struct{})
	var probes atomic.Int32
	client.PrependReactor("list", "widgets", func(action ktesting.Action) (bool, runtime.Object, error) {
		if probes.Add(1) == 1 {
			close(started)
			<-release
		}
		if action.GetNamespace() == "" {
			return true, nil, apierrors.NewForbidden(gvr.GroupResource(), "", errors.New("cluster denied"))
		}
		return false, nil, nil
	})
	disc := &ResourceDiscovery{resourceMap: make(map[string]APIResource), gvrMap: make(map[string]schema.GroupVersionResource)}
	disc.AddAPIResource(APIResource{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: "Widget", Namespaced: true, Verbs: []string{"list", "watch"}})
	dc, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: client, Discovery: disc})
	if err != nil {
		t.Fatal(err)
	}
	defer dc.Stop()
	firstCtx, firstCancel := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() { _, err := dc.ListComplete(firstCtx, gvr, []string{"allowed"}); firstDone <- err }()
	<-started
	firstCancel()
	if err := <-firstDone; err == nil {
		t.Fatal("canceled caller succeeded")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	secondDone := make(chan error, 1)
	go func() { _, err := dc.ListComplete(ctx, gvr, []string{"allowed"}); secondDone <- err }()
	close(release)
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	if dc.IsClusterWideSynced(gvr) || !dc.IsNamespaceSynced(gvr, "allowed") {
		t.Fatal("unverified cluster-wide informer or missing scoped informer")
	}
}

func TestListedEndpoint404NeverStartsAnInformer(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "test.example", Version: "v1", Resource: "widgets"}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"})
	client.PrependReactor("list", "widgets", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(gvr.GroupResource(), "")
	})
	dc, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: client})
	if err != nil {
		t.Fatal(err)
	}
	defer dc.Stop()
	_, err = dc.ListComplete(context.Background(), gvr, nil)
	var readErr *ResourceReadError
	if !errors.As(err, &readErr) || readErr.Code != "kind_not_served" || len(dc.WatchedGVRs()) != 0 {
		t.Fatalf("404 observed as %v; informers=%v", err, dc.WatchedGVRs())
	}
}
func TestDynamicNoiseDoesNotReachTimelineOrChangeChannel(t *testing.T) {
	changes := make(chan ResourceChange, 2)
	callbacks := 0
	dc, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), Changes: changes, OnChange: func(ResourceChange, any, any) { callbacks++ }, IsNoisyResource: func(kind, name, op string) bool { return kind == "Lease" && op == OpUpdate }})
	if err != nil {
		t.Fatal(err)
	}
	defer dc.Stop()
	gvr := schema.GroupVersionResource{Group: "coordination.k8s.io", Version: "v1", Resource: "leases"}
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "coordination.k8s.io/v1", "kind": "Lease", "metadata": map[string]any{"name": "leader"}}}
	dc.enqueueDynamicChange("Lease", gvr, obj, nil, OpUpdate)
	if callbacks != 0 || len(changes) != 0 {
		t.Fatal("lease renewal reached timeline or SSE")
	}
	dc.enqueueDynamicChange("Lease", gvr, obj, nil, OpDelete)
	if callbacks != 1 || len(changes) != 1 {
		t.Fatal("lease deletion was hidden")
	}
	dc.MarkDiscoveryOnDemand()
	if dc.GetDiscoveryStatus() != CRDDiscoveryOnDemand {
		t.Fatal("on-demand observation reported idle")
	}
}
