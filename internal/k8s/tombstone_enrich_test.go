package k8s

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/internal/timeline"
)

func tombstoneTestPod(name string, created time.Time) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "shop",
			UID:               types.UID("pod-" + name),
			CreationTimestamp: metav1.NewTime(created),
			Labels:            map[string]string{"app": "web", "app.kubernetes.io/name": "web"},
			OwnerReferences: []metav1.OwnerReference{{
				Kind:       "ReplicaSet",
				Name:       "web-rs",
				Controller: boolPtr(true),
			}},
		},
	}
}

func initMemoryTimeline(t *testing.T) {
	t.Helper()
	timeline.ResetStore()
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 100}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	// Clean tombstone cache (resourceCache is nil in tests, so this is safe).
	ResetResourceCache()
	t.Cleanup(timeline.ResetStore)
}

func k8sEventPod(name, reason string) *corev1.Event {
	return &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name + ".evt",
			Namespace: "shop",
			UID:       types.UID("evt-" + name),
		},
		InvolvedObject: corev1.ObjectReference{
			Kind:       "Pod",
			APIVersion: "v1",
			Namespace:  "shop",
			Name:       name,
			// Real K8s Events carry the involved object's UID; the tombstone
			// key is UID-first, so the fixture must too.
			UID: types.UID("pod-" + name),
		},
		Reason:         reason,
		Message:        "Stopping container web",
		Type:           "Normal",
		LastTimestamp:  metav1.NewTime(time.Now()),
		FirstTimestamp: metav1.NewTime(time.Now()),
		Count:          1,
	}
}

func k8sEventFromStore(t *testing.T) timeline.TimelineEvent {
	t.Helper()
	events, err := timeline.GetStore().Query(context.Background(), timeline.QueryOptions{
		Kinds:            []string{"Pod"},
		IncludeManaged:   true,
		IncludeK8sEvents: true,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	for _, e := range events {
		if e.Source == timeline.SourceK8sEvent {
			return e
		}
	}
	t.Fatal("no k8s_event recorded")
	return timeline.TimelineEvent{}
}

// A "Killing"-class K8s Event arrives after its involved Pod has left the
// informer cache. Because the delete fed the tombstone with the final object's
// enrichment, the event still ships owner + labels + createdAt as fact.
func TestK8sEvent_TombstoneEnrichesAfterDelete(t *testing.T) {
	initMemoryTimeline(t)

	created := time.Now().Add(-45 * time.Minute)
	pod := tombstoneTestPod("web-abc", created)

	// Informer delete: production passes the deleted object as newObj, oldObj=nil.
	recordToTimelineStore(ActiveClusterContext(), "Pod", "shop", "web-abc", string(pod.UID), "delete", nil, pod, nil, false)

	// Late K8s event; live cache is empty (GetResourceCache()==nil), so
	// enrichment must come from the tombstone.
	recordK8sEventToTimeline(ActiveClusterContext(), k8sEventPod("web-abc", "Killing"))

	got := k8sEventFromStore(t)
	if got.Owner == nil || got.Owner.Kind != "ReplicaSet" || got.Owner.Name != "web-rs" {
		t.Fatalf("owner not enriched from tombstone: %+v", got.Owner)
	}
	if got.Labels == nil || got.Labels["app.kubernetes.io/name"] != "web" {
		t.Fatalf("labels not enriched from tombstone: %+v", got.Labels)
	}
	if got.CreatedAt == nil || !got.CreatedAt.Equal(created) {
		t.Fatalf("createdAt not stamped from tombstone: %v", got.CreatedAt)
	}
}

// Without a tombstone (or live cache) the event ships anonymous — the silent
// null-enrichment fallback, unchanged from before the cache existed.
func TestK8sEvent_MissStaysSilentNull(t *testing.T) {
	initMemoryTimeline(t)

	recordK8sEventToTimeline(ActiveClusterContext(), k8sEventPod("ghost", "Killing"))

	got := k8sEventFromStore(t)
	if got.Owner != nil {
		t.Fatalf("expected nil owner on miss, got %+v", got.Owner)
	}
	if got.Labels != nil {
		t.Fatalf("expected nil labels on miss, got %+v", got.Labels)
	}
	if got.CreatedAt != nil {
		t.Fatalf("expected nil createdAt on miss, got %v", got.CreatedAt)
	}
}

// An informer add feeds the tombstone too, so a later event for a still-recent
// (or just-evicted) object is enriched even without a delete.
func TestK8sEvent_TombstoneFedOnAdd(t *testing.T) {
	initMemoryTimeline(t)
	initialSyncComplete = true
	t.Cleanup(func() { initialSyncComplete = false })

	created := time.Now() // fresh add (age <= 30s so it is recorded, not treated as sync)
	pod := tombstoneTestPod("web-new", created)
	recordToTimelineStore(ActiveClusterContext(), "Pod", "shop", "web-new", string(pod.UID), "add", nil, pod, nil, false)

	recordK8sEventToTimeline(ActiveClusterContext(), k8sEventPod("web-new", "Started"))

	got := k8sEventFromStore(t)
	if got.Owner == nil || got.Owner.Name != "web-rs" {
		t.Fatalf("owner not enriched from add-fed tombstone: %+v", got.Owner)
	}
	if got.CreatedAt == nil || !got.CreatedAt.Equal(created) {
		t.Fatalf("createdAt not stamped: %v", got.CreatedAt)
	}
}

// A callback whose object can't be unwrapped (extract failure) must not feed
// the tombstone — an empty entry would clobber the enrichment a prior good
// entry was preserving.
func TestK8sEvent_FailedExtractDoesNotClobberTombstone(t *testing.T) {
	initMemoryTimeline(t)

	created := time.Now().Add(-45 * time.Minute)
	pod := tombstoneTestPod("web-abc", created)
	recordToTimelineStore(ActiveClusterContext(), "Pod", "shop", "web-abc", string(pod.UID), "delete", nil, pod, nil, false)

	// Same resource identity, but a payload ExtractTombstoneEntry can't
	// unwrap (not a metav1.Object, not a DeletedFinalStateUnknown).
	recordToTimelineStore(ActiveClusterContext(), "Pod", "shop", "web-abc", string(pod.UID), "delete", nil, "not-an-object", nil, false)

	recordK8sEventToTimeline(ActiveClusterContext(), k8sEventPod("web-abc", "Killing"))

	got := k8sEventFromStore(t)
	if got.Owner == nil || got.Owner.Kind != "ReplicaSet" || got.Owner.Name != "web-rs" {
		t.Fatalf("prior good tombstone entry was clobbered: owner = %+v", got.Owner)
	}
	if got.Labels == nil || got.Labels["app.kubernetes.io/name"] != "web" {
		t.Fatalf("prior good tombstone entry was clobbered: labels = %+v", got.Labels)
	}
	if got.CreatedAt == nil || !got.CreatedAt.Equal(created) {
		t.Fatalf("prior good tombstone entry was clobbered: createdAt = %v", got.CreatedAt)
	}
}
