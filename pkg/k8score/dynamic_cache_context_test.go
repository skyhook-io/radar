package k8score

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
)

// The permission probe is shared by every caller warming the same scope. A
// caller that stops waiting must not take the probe down with it: the other
// callers would inherit its cancellation and no informer would start.
func TestEnsureWatchingContextCancelDoesNotFailJoinedCallers(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"})
	probing := make(chan struct{})
	release := make(chan struct{})
	releaseProbe := sync.OnceFunc(func() { close(release) })
	defer releaseProbe()
	var probes atomic.Int32
	dyn.PrependReactor("list", "widgets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetNamespace() == "" {
			return true, nil, apierrors.NewForbidden(gvr.GroupResource(), "", errors.New("cluster-wide denied"))
		}
		if action.(interface{ GetListOptions() metav1.ListOptions }).GetListOptions().Limit != 1 {
			return false, nil, nil
		}
		if probes.Add(1) == 1 {
			close(probing)
		}
		<-release
		return false, nil, nil
	})
	d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)

	starter, cancelStarter := context.WithCancel(context.Background())
	defer cancelStarter()
	starterErr := make(chan error, 1)
	go func() { starterErr <- d.EnsureWatchingContext(starter, gvr, "team-a") }()
	select {
	case <-probing:
	case <-time.After(5 * time.Second):
		t.Fatal("permission probe never started")
	}
	joinerErr := make(chan error, 1)
	go func() { joinerErr <- d.EnsureWatchingContext(context.Background(), gvr, "team-a") }()
	// The joiner reaches the in-flight probe within microseconds.
	time.Sleep(100 * time.Millisecond)

	cancelStarter()
	select {
	case err := <-starterErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled caller: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled caller kept waiting for the shared probe")
	}
	releaseProbe()
	select {
	case err := <-joinerErr:
		if err != nil {
			t.Fatalf("joined caller inherited another caller's cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("joined caller never returned")
	}
	if probes.Load() != 1 || !d.hasCoveringInformer(gvr, "team-a") {
		t.Fatalf("probes=%d covered=%v; want one shared probe that started the informer", probes.Load(), d.hasCoveringInformer(gvr, "team-a"))
	}
}

// A probe that runs out of time proves nothing about access: it must neither
// start a watch nor be remembered as a denial.
func TestExpiredProbeStartsNoWatch(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	for _, tc := range []struct {
		name, namespace string
		clusterWide     error
	}{
		{"cluster-wide probe", "", context.DeadlineExceeded},
		{"requested namespace probe", "team-a", apierrors.NewForbidden(gvr.GroupResource(), "", errors.New("cluster-wide denied"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"})
			dyn.PrependReactor("list", "widgets", func(action k8stesting.Action) (bool, runtime.Object, error) {
				if action.GetNamespace() == "" {
					return true, nil, tc.clusterWide
				}
				return true, nil, context.DeadlineExceeded
			})
			d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(d.Stop)
			if err := d.EnsureWatchingContext(context.Background(), gvr, tc.namespace); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("expired probe: %v", err)
			}
			if watched := d.GetWatchedResources(); len(watched) != 0 {
				t.Fatalf("expired probe started a watch: %v", watched)
			}
			if got := d.Observation(gvr); got.State == DynamicObservationDenied {
				t.Fatalf("expired probe recorded as denial: %+v", got)
			}
		})
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
			_, _ = w.Write([]byte(`{"kind":"WidgetList","apiVersion":"example.io/v1","metadata":{"resourceVersion":"1"},"items":[]}`))
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
