package mcp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/k8s"
)

// A cold custom-resource read waits for the informer to sync. That wait belongs
// to the caller: an MCP tool call has a deadline, and a wait that outlives it
// only holds the goroutine after nobody is listening.
func TestDynamicReadWaitEndsWithTheCallerDeadline(t *testing.T) {
	if err := k8s.InitTestResourceCache(fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "cold"}},
	)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)

	gvr := schema.GroupVersionResource{Group: "example.test", Version: "v1", Resource: "widgets"}
	widget := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.test/v1",
		"kind":       "Widget",
		"metadata":   map[string]any{"namespace": "cold", "name": "w1"},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		gvr: "WidgetList",
		{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}: "EndpointSliceList",
	}, widget)
	// The kind can't be listed cluster-wide, so its informer is namespaced, and
	// every list of widgets in "cold" is slow: it cannot sync inside the
	// caller's deadline.
	dyn.PrependReactor("list", "widgets", func(a ktesting.Action) (bool, runtime.Object, error) {
		if a.GetNamespace() == "" {
			return true, nil, apierrors.NewForbidden(gvr.GroupResource(), "", errors.New("radar may not list widgets cluster-wide"))
		}
		if a.GetNamespace() == "cold" {
			time.Sleep(800 * time.Millisecond)
		}
		return false, nil, nil
	})
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{
		Group: gvr.Group, Version: gvr.Version, Kind: "Widget", Name: gvr.Resource,
		Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"},
	}}); err != nil {
		t.Fatal(err)
	}
	// The shared informer start keeps running after this test's call returns.
	t.Cleanup(k8s.ResetTestDynamicState)
	t.Cleanup(func() { time.Sleep(time.Second) })

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := checkDynamicResourceReadFrom(ctx, "Widget", "example.test", "cold", "list", nil,
		k8s.GetDynamicResourceCache(), gvr, true)
	elapsed := time.Since(start)

	if elapsed > 400*time.Millisecond {
		t.Fatalf("read waited %v on a cache sync; the caller's 100ms deadline must end it", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the caller's deadline", err)
	}
}

// The same deadline holds when the informer has already started but not
// synced, for example because an earlier read began the watch. The caller
// gets its deadline back, not a "still loading" answer.
func TestDynamicReadWaitOnAStartedInformerEndsWithTheCallerDeadline(t *testing.T) {
	if err := k8s.InitTestResourceCache(fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "warm"}},
	)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)

	gvr := schema.GroupVersionResource{Group: "example.test", Version: "v1", Resource: "widgets"}
	widget := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.test/v1",
		"kind":       "Widget",
		"metadata":   map[string]any{"namespace": "warm", "name": "w1"},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		gvr: "WidgetList",
		{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}: "EndpointSliceList",
	}, widget)
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	var mu sync.Mutex
	nsLists := 0
	// The kind can't be listed cluster-wide, so its informer is namespaced.
	// The first list in "warm" is the access probe and answers at once; the
	// informer's own list after it is held, so the informer starts but
	// cannot sync.
	dyn.PrependReactor("list", "widgets", func(a ktesting.Action) (bool, runtime.Object, error) {
		if a.GetNamespace() == "" {
			return true, nil, apierrors.NewForbidden(gvr.GroupResource(), "", errors.New("radar may not list widgets cluster-wide"))
		}
		mu.Lock()
		nsLists++
		n := nsLists
		mu.Unlock()
		if n > 1 {
			<-release
		}
		return false, nil, nil
	})
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{
		Group: gvr.Group, Version: gvr.Version, Kind: "Widget", Name: gvr.Resource,
		Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"},
	}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)

	dc := k8s.GetDynamicResourceCache()
	if err := dc.EnsureWatchingContext(context.Background(), gvr, "warm"); err != nil {
		t.Fatalf("starting the informer: %v", err)
	}
	if !dc.HasCoveringInformer(gvr, "warm") || dc.IsNamespaceSynced(gvr, "warm") {
		t.Fatal("setup: want a started informer that has not synced")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := checkDynamicResourceReadFrom(ctx, "Widget", "example.test", "warm", "list", nil, dc, gvr, true)
	elapsed := time.Since(start)
	releaseOnce.Do(func() { close(release) })

	if elapsed > 400*time.Millisecond {
		t.Fatalf("read waited %v on a cache sync; the caller's 100ms deadline must end it", elapsed)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the caller's deadline", err)
	}
}
