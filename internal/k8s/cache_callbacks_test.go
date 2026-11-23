package k8s

import (
	"testing"
	"time"

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
		dynamic.OnRecorded("Widget")
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
	if counts.Received["Pod"] != 1 || counts.Received["Widget"] != 1 || counts.Recorded["Pod"] != 1 || counts.Recorded["Widget"] != 2 || counts.Dropped[timeline.DropReasonChannelFull] != 2 {
		t.Fatalf("unexpected open-cache counters: %+v", counts)
	}
	if observed != 1 || GetConnectionStatus().ProgressMsg == "" {
		t.Fatal("open typed cache did not publish its observation and sync progress")
	}

	// The dynamic cache closes first in a switch. Its callbacks must stop
	// even while the typed cache that owns the enrichment data is open.
	conn.closeDynamicResourceCache()
	fireDynamic()
	if got := timeline.GetMetrics().GetSnapshot().Counters; got.Received["Widget"] != 1 || got.Recorded["Widget"] != 2 || got.Dropped[timeline.DropReasonChannelFull] != 2 {
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
