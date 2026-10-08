package k8s

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/skyhook-io/radar/internal/timeline"
)

// With no store installed, changes that would have been recorded must move the
// store_unavailable counter, and objects that merely existed must not.
func TestTimelineStoreDownCountsLostChangesOnly(t *testing.T) {
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	timeline.ResetMetricsForContextSwitch()
	t.Cleanup(timeline.ResetMetricsForContextSwitch)

	prev := initialSyncComplete.Load()
	t.Cleanup(func() { initialSyncComplete.Store(prev) })

	storeDown := func() int64 {
		return timeline.GetMetrics().GetSnapshot().Counters.Dropped[timeline.DropReasonStoreDown]
	}
	pod := func(name string, age time.Duration) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "shop", CreationTimestamp: metav1.NewTime(time.Now().Add(-age)),
		}}
	}

	initialSyncComplete.Store(false)
	recordToTimelineStore("ctx", "Pod", "shop", "listed", "u1", "add", nil, pod("listed", time.Hour), nil, false)
	if got := storeDown(); got != 0 {
		t.Fatalf("an object listed at startup counted as a lost change: store_unavailable=%d", got)
	}

	initialSyncComplete.Store(true)
	recordToTimelineStore("ctx", "Pod", "shop", "relisted", "u2", "add", nil, pod("relisted", time.Hour), nil, false)
	if got := storeDown(); got != 0 {
		t.Fatalf("a relisted old object counted as a lost change: store_unavailable=%d", got)
	}

	recordToTimelineStore("ctx", "Pod", "shop", "new", "u3", "add", nil, pod("new", time.Second), nil, false)
	recordToTimelineStore("ctx", "Pod", "shop", "listed", "u1", "update", pod("listed", time.Hour), pod("listed", time.Hour), nil, false)
	recordToTimelineStore("ctx", "Pod", "shop", "listed", "u1", "delete", pod("listed", time.Hour), nil, nil, false)
	recordK8sEventToTimeline("ctx", k8sEventPod("new", "Started"))
	if got := storeDown(); got != 4 {
		t.Fatalf("store_unavailable=%d, want 4 (new add, update, delete, K8s event)", got)
	}
}
