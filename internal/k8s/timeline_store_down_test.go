package k8s

import (
	"os"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/skyhook-io/radar/internal/timeline"
)

// With no store installed, changes that would have been recorded must move the
// store_unavailable counter. Objects that merely existed, and updates with
// nothing to record, must not: they would not have been recorded either.
func TestTimelineStoreDownCountsLostChangesOnly(t *testing.T) {
	state := newConnectionCache()
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)

	dropped := func(reason string) int64 {
		return timeline.GetMetrics().GetSnapshot().Counters.Dropped[reason]
	}
	storeDown := func() int64 { return dropped(timeline.DropReasonStoreDown) }
	pod := func(name string, age time.Duration) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "shop", CreationTimestamp: metav1.NewTime(time.Now().Add(-age)),
		}}
	}

	state.initialSyncComplete.Store(false)
	state.recordToTimelineStore(timeline.CaptureStore(), "ctx", "Pod", "shop", "listed", "u1", "add", nil, pod("listed", time.Hour), nil, false)
	if got := storeDown(); got != 0 {
		t.Fatalf("an object listed at startup counted as a lost change: store_unavailable=%d", got)
	}

	state.initialSyncComplete.Store(true)
	state.recordToTimelineStore(timeline.CaptureStore(), "ctx", "Pod", "shop", "relisted", "u2", "add", nil, pod("relisted", time.Hour), nil, false)
	if got := storeDown(); got != 0 {
		t.Fatalf("a relisted old object counted as a lost change: store_unavailable=%d", got)
	}

	state.recordToTimelineStore(timeline.CaptureStore(), "ctx", "Pod", "shop", "listed", "u1", "update", pod("listed", time.Hour), pod("listed", time.Hour), nil, false)
	if got := storeDown(); got != 0 {
		t.Fatalf("an update with nothing to record counted as a lost change: store_unavailable=%d", got)
	}
	if got := dropped(timeline.DropReasonNoDiff); got != 1 {
		t.Fatalf("no_diff=%d, want 1: the heartbeat must be counted the way the healthy path counts it", got)
	}

	running := pod("listed", time.Hour)
	running.Status.Phase = corev1.PodRunning
	state.recordToTimelineStore(timeline.CaptureStore(), "ctx", "Pod", "shop", "new", "u3", "add", nil, pod("new", time.Second), nil, false)
	state.recordToTimelineStore(timeline.CaptureStore(), "ctx", "Pod", "shop", "listed", "u1", "update", pod("listed", time.Hour), running, nil, false)
	state.recordToTimelineStore(timeline.CaptureStore(), "ctx", "Pod", "shop", "listed", "u1", "delete", pod("listed", time.Hour), nil, nil, false)
	state.recordK8sEventToTimeline(timeline.CaptureStore(), "ctx", k8sEventPod("new", "Started"))
	if got := storeDown(); got != 4 {
		t.Fatalf("store_unavailable=%d, want 4 (new add, update, delete, K8s event)", got)
	}
	// The delete's seen mark is still in the database; the reconnected store
	// has to be told to clear it.
	if got := timeline.DeletedWhileUnavailableCountForTest(); got != 1 {
		t.Fatalf("%d delete(s) noted for the reconnect, want 1", got)
	}
}

// A store that is installed but failing, a database that went away after a good
// start, loses Kubernetes Events the same way it loses resource changes, and
// the drop counter has to say so.
func TestTimelineStoreFailureCountsLostKubernetesEvents(t *testing.T) {
	state := newConnectionCache()
	dsn := os.Getenv("RADAR_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("RADAR_TEST_POSTGRES_DSN not set")
	}
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)

	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypePostgres, DSN: dsn}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	store := timeline.GetStore()
	if store == nil {
		t.Fatal("no store installed against a reachable database")
	}
	// A closed pool fails every write, as a dead database does.
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	state.recordK8sEventToTimeline(timeline.CaptureStore(), "ctx", k8sEventPod("web", "Started"))
	if got := timeline.GetMetrics().GetSnapshot().Counters.Dropped[timeline.DropReasonStoreFailed]; got != 1 {
		t.Fatalf("store_failed=%d after a failed K8s Event write, want 1", got)
	}
}
