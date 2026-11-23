package k8s

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/k8score"
)

func TestInformerCallbacksAfterCacheClose(t *testing.T) {
	t.Cleanup(SnapshotTestConnection())
	conn := &Connection{contextName: "shared-name"}
	restoreTestConnection(conn)
	clearTestConnectionIdentity()
	initMemoryTimeline(t)
	timeline.ResetMetricsForContextSwitch()
	t.Cleanup(timeline.ResetMetricsForContextSwitch)

	var typed k8score.CacheConfig
	observed := 0
	conn.configureTypedCallbacks(&typed, func(ResourceChange, any, any) { observed++ })
	var dynamic k8score.DynamicCacheConfig
	conn.configureDynamicCallbacks(&dynamic)

	pod := tombstoneTestPod("web", time.Now())
	event := k8sEventPod("web", "Killing")
	typedDelete := ResourceChange{Kind: "Pod", Namespace: "shop", Name: "web", UID: string(pod.UID), Operation: "delete"}
	fireTyped := func() {
		typed.OnReceived("Pod")
		typed.OnObservedChange(typedDelete, pod, nil)
		typed.OnChange(typedDelete, pod, nil)
		typed.OnEventChange(event, "add")
		typed.OnDrop("Pod", "shop", "web", timeline.DropReasonChannelFull, "delete")
		typed.SyncProgress(1, 2, false)
	}
	widget := &unstructured.Unstructured{}
	widget.SetAPIVersion("example.org/v1")
	widget.SetKind("Widget")
	widget.SetNamespace("shop")
	widget.SetName("widget")
	dynamicDelete := ResourceChange{Kind: "Widget", Namespace: "shop", Name: "widget", Operation: "delete"}
	fireDynamic := func() {
		dynamic.OnReceived("Widget")
		dynamic.OnChange(dynamicDelete, widget, nil)
		dynamic.OnDrop("Widget", "shop", "widget", timeline.DropReasonChannelFull, "delete")
		if dynamic.OnRecorded != nil {
			dynamic.OnRecorded("Widget")
		}
	}

	SetConnectionStatus(ConnectionStatus{State: StateConnected})
	firstID := ServingConnectionID()
	RetireServingConnection()
	SetConnectionStatus(ConnectionStatus{State: StateConnected})
	if firstID == "" || ServingConnectionID() == firstID || Current() != conn {
		t.Fatal("expected the same cache build under a new admission")
	}
	SetConnectionStatus(ConnectionStatus{State: StateConnecting})
	fireTyped()
	fireDynamic()
	events, err := timeline.QueryEvents(t.Context(), timeline.QueryOptions{IncludeManaged: true, IncludeK8sEvents: true})
	if err != nil || len(events) != 3 {
		t.Fatalf("open caches recorded %d rows, want a Pod, its Kubernetes Event and a Widget: %v", len(events), err)
	}
	for _, event := range events {
		if event.ClusterContext != conn.ContextName() {
			t.Fatalf("row has cluster context %q, want %q", event.ClusterContext, conn.ContextName())
		}
	}
	counts := timeline.GetMetrics().GetSnapshot().Counters
	if counts.Received["Pod"] != 1 || counts.Received["Widget"] != 1 || counts.Recorded["Pod"] != 1 || counts.Recorded["Widget"] != 1 || counts.Dropped[timeline.DropReasonChannelFull] != 2 {
		t.Fatalf("unexpected open-cache counters: %+v", counts)
	}
	if observed != 1 || GetConnectionStatus().ProgressMsg == "" {
		t.Fatal("open typed cache did not publish its observation and sync progress")
	}

	// The dynamic cache closes first in a switch. Its callbacks must stop
	// even while the typed cache that owns the enrichment data is open.
	conn.closeDynamicResourceCache()
	fireDynamic()
	if got := timeline.GetMetrics().GetSnapshot().Counters; got.Received["Widget"] != 1 || got.Recorded["Widget"] != 1 || got.Dropped[timeline.DropReasonChannelFull] != 2 {
		t.Fatalf("closed dynamic cache changed counters: %+v", got)
	}

	RetireServingConnection()
	conn.closeResourceCache()
	ResetResourceDiscovery()
	if Current() == conn || Current().ContextName() != conn.ContextName() {
		t.Fatal("expected a replacement connection with the same context name")
	}
	initMemoryTimeline(t)
	timeline.ResetMetricsForContextSwitch()
	SetConnectionStatus(ConnectionStatus{State: StateConnecting, ProgressMsg: "replacement"})
	fireTyped()
	fireDynamic()
	events, err = timeline.QueryEvents(t.Context(), timeline.QueryOptions{IncludeManaged: true, IncludeK8sEvents: true})
	if err != nil || len(events) != 0 {
		t.Fatalf("late callbacks recorded %d rows in replacement store: %v", len(events), err)
	}
	metrics := timeline.GetMetrics().GetSnapshot()
	if len(metrics.Counters.Received)+len(metrics.Counters.Recorded)+len(metrics.Counters.Dropped)+len(metrics.RecentDrops) != 0 {
		t.Fatalf("late callbacks changed replacement metrics: %+v", metrics)
	}
	if observed != 1 || GetConnectionStatus().ProgressMsg != "replacement" {
		t.Fatal("late typed callback published an observation or overwrote replacement progress")
	}
}

type pausedInformerObject struct {
	metav1.Object
	admitted chan struct{}
	resume   chan struct{}
}

func (o *pausedInformerObject) GetResourceVersion() string {
	close(o.admitted)
	<-o.resume
	return o.Object.GetResourceVersion()
}

func TestAdmittedInformerWriteAcrossSameNameSwitch(t *testing.T) {
	for _, backend := range []timeline.StoreType{timeline.StoreTypeMemory, timeline.StoreTypeSQLite, timeline.StoreTypePostgres} {
		t.Run(string(backend), func(t *testing.T) {
			cfg := timeline.StoreConfig{Type: backend}
			switch backend {
			case timeline.StoreTypeSQLite:
				cfg.Path = filepath.Join(t.TempDir(), "timeline.db")
			case timeline.StoreTypePostgres:
				cfg.DSN = testPostgresDSN(t)
			}
			for _, tc := range []struct{ kind, op string }{{"Pod", "delete"}, {"Widget", "delete"}, {"Pod", "update"}} {
				t.Run(tc.kind+"/"+tc.op, func(t *testing.T) {
					testAdmittedInformerWrite(t, cfg, tc.kind, tc.op)
				})
			}
		})
	}
}

func testAdmittedInformerWrite(t *testing.T, cfg timeline.StoreConfig, kind, op string) {
	t.Cleanup(SnapshotTestConnection())
	a := &Connection{contextName: "shared-name"}
	restoreTestConnection(a)
	resetStore := func() {
		t.Helper()
		timeline.ResetStore()
		timeline.ResetMetricsForContextSwitch()
		if err := timeline.InitStore(cfg); err != nil {
			t.Fatal(err)
		}
		store := timeline.GetStore()
		if store == nil || store.Stats().Degraded {
			t.Fatalf("could not initialize %s timeline", cfg.Type)
		}
	}
	t.Cleanup(timeline.ResetStore)
	t.Cleanup(timeline.ResetMetricsForContextSwitch)
	resetStore()

	storeA := timeline.GetStore()
	var resource metav1.Object
	var onChange func(ResourceChange, any, any)
	if kind == "Pod" {
		resource = tombstoneTestPod("from-a", time.Now())
		var cfg k8score.CacheConfig
		a.configureTypedCallbacks(&cfg, nil)
		onChange = cfg.OnChange
	} else {
		resource = &unstructured.Unstructured{}
		var cfg k8score.DynamicCacheConfig
		a.configureDynamicCallbacks(&cfg)
		onChange = cfg.OnChange
	}
	obj := &pausedInformerObject{Object: resource, admitted: make(chan struct{}), resume: make(chan struct{})}
	obj.SetName("from-a")
	obj.SetNamespace("shop")
	done := make(chan struct{})
	go func() {
		defer close(done)
		var oldObj any
		if op == "update" {
			oldObj = resource
		}
		onChange(ResourceChange{Kind: kind, Operation: op, Namespace: "shop", Name: "from-a"}, obj, oldObj)
	}()
	<-obj.admitted
	resume := sync.OnceFunc(func() {
		close(obj.resume)
		<-done
	})
	t.Cleanup(resume)
	a.closeDynamicResourceCache()
	a.closeResourceCache()
	ResetResourceDiscovery()
	resetStore()
	resume()

	if b := Current(); b == a || b.ContextName() != a.ContextName() {
		t.Fatal("expected a replacement connection with the same context name")
	}
	if n := timeline.GetStore().Stats().TotalEvents; n != 0 {
		t.Fatalf("replacement timeline contains %d row(s) from A", n)
	}
	if n := storeA.Stats().TotalEvents; n != 0 {
		t.Fatalf("closed timeline accepted %d late row(s)", n)
	}
	// A write rejected by the retired store lost nothing for B, so it
	// must not count against B's coverage.
	snap := timeline.GetMetrics().GetSnapshot()
	failure := timeline.LastStoreFailure(kind)
	if len(snap.RecentDrops) != 0 || len(snap.Counters.Dropped) != 0 || len(snap.Counters.Recorded) != 0 || len(snap.Counters.Received) != 0 || !failure.IsZero() {
		t.Fatalf("retired callback changed replacement diagnostics: %+v, last store failure: %v", snap, failure)
	}
}
