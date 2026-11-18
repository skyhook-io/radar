package issuecorrelation

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

// initStore starts a memory timeline store that has been observing for two
// hours, connected so the timeline answers.
func initStore(t *testing.T) timeline.EventStore {
	t.Helper()
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 2000}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	timeline.SetObservationStartForTest(time.Now().Add(-2 * time.Hour))
	prev := k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected, Context: prev.Context})
	t.Cleanup(func() { k8s.SetConnectionStatus(prev) })
	return timeline.GetStore()
}

// initStoreConnected marks the cluster connected for an already initialized
// store, so the timeline answers.
func initStoreConnected(t *testing.T) timeline.EventStore {
	t.Helper()
	prev := k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected, Context: prev.Context})
	t.Cleanup(func() { k8s.SetConnectionStatus(prev) })
	return timeline.GetStore()
}

func initCache(t *testing.T, objs ...runtime.Object) {
	t.Helper()
	if err := k8s.InitTestResourceCache(k8sfake.NewSimpleClientset(objs...)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestState)
}

func appendChange(t *testing.T, store timeline.EventStore, e timeline.TimelineEvent) {
	t.Helper()
	e.Source, e.ClusterContext = timeline.SourceInformer, k8s.ActiveClusterContext()
	if e.ID == "" {
		e.ID = e.Kind + "/" + e.Name + "/" + string(e.EventType) + "/" + e.Timestamp.String()
	}
	if err := store.Append(context.Background(), e); err != nil {
		t.Fatalf("append %s: %v", e.ID, err)
	}
}

// lookbackRun is a run with the MCP tool's options: the last hour, ending
// whenever the read runs.
func lookbackRun(iss issuesapi.Issue, access Access) *run {
	now := time.Now()
	return &run{
		ctx: context.Background(), iss: iss, access: access,
		opts:    Options{Window: Window{From: now.Add(-time.Hour), To: now, OpenEnded: true}, MinWindow: 5 * time.Minute},
		unknown: map[issuesapi.CorrelationUnknownReason][]issuesapi.UncheckedKind{},
	}
}

func utc(t time.Time) string { return t.UTC().Format(time.RFC3339) }
