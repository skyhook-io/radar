package k8score

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

func TestEnsureWatchingContextBoundsNamespaceProbes(t *testing.T) {
	var requests atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path == "/apis/example.io/v1/widgets" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403}`))
			return
		}
		<-r.Context().Done()
	}))
	t.Cleanup(api.Close)
	client, err := dynamic.NewForConfig(&rest.Config{Host: api.URL})
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: client, NamespaceFallbacks: []string{"a", "b", "c"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	for _, namespace := range []string{"b", ""} {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		start := time.Now()
		err := d.EnsureWatchingContext(ctx, gvr, namespace)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
			t.Fatalf("namespace %q: error=%v elapsed=%s", namespace, err, time.Since(start))
		}
	}
	if requests.Load() != 4 || len(d.GetWatchedResources()) != 0 {
		t.Fatalf("expired probes started watches or walked extra namespaces: requests=%d watches=%v", requests.Load(), d.GetWatchedResources())
	}
}

func TestEnsureWatchingContextAddsRequestedNamespace(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	client := fakeDynamicForListAccess(t, map[schema.GroupVersionResource]string{gvr: "WidgetList"}, func(_ schema.GroupVersionResource, ns string) bool { return ns == "a" || ns == "b" })
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: client, NamespaceFallback: "a"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for _, namespace := range []string{"a", "b"} {
		if err := d.EnsureWatchingContext(ctx, gvr, namespace); err != nil {
			t.Fatal(err)
		}
		if !d.WaitForSyncContext(ctx, gvr, namespace) {
			t.Fatalf("namespace %s did not sync", namespace)
		}
	}
	observation := d.Observation(gvr)
	if observation.State != DynamicObservationSynced || len(observation.Namespaces) != 2 || !d.hasCoveringInformer(gvr, "b") {
		t.Fatalf("namespace expansion: %+v", observation)
	}
	canceled, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	if err := d.EnsureWatchingContext(canceled, gvr, "c"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled warm: %v", err)
	}
}

func TestWaitForSyncContextStopsWaitingWithoutStoppingInformer(t *testing.T) {
	var lists atomic.Int32
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if lists.Add(1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"kind":"WidgetList","apiVersion":"example.io/v1","metadata":{"resourceVersion":"1"},"items":[]}`))
			return
		}
		<-r.Context().Done()
	}))
	t.Cleanup(api.Close)
	client, err := dynamic.NewForConfig(&rest.Config{Host: api.URL})
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: client})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := d.EnsureWatchingContext(ctx, gvr, ""); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if d.WaitForSyncContext(ctx, gvr, "") || time.Since(start) > time.Second {
		t.Fatalf("sync wait ignored deadline: %s", time.Since(start))
	}
	if got := d.Observation(gvr); got.State != DynamicObservationSyncing || len(d.GetWatchedResources()) != 1 {
		t.Fatalf("request timeout stopped the cache-owned informer: %+v", got)
	}
}
