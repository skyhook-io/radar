package k8s

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/internal/timeline"
)

func testDeployment(uid, image string, created time.Time) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "web",
			Namespace:         "shop",
			UID:               types.UID(uid),
			CreationTimestamp: metav1.NewTime(created),
		},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "web", Image: image}},
				},
			},
		},
	}
}

// Delete + recreate under the same name must produce an add event that
// carries the diff against the deleted predecessor, marked ReasonRecreated.
func TestRecreateJoin_DeleteThenRecreateCarriesDiff(t *testing.T) {
	prev := initialSyncComplete
	initialSyncComplete = true
	defer func() { initialSyncComplete = prev }()
	resetRecreateStash()
	defer resetRecreateStash()

	timeline.ResetStore()
	defer timeline.ResetStore()
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 100}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}

	oldDep := testDeployment("uid-1", "nginx:1.0", time.Now().Add(-time.Hour))
	recordToTimelineStore("Deployment", "shop", "web", "uid-1", "delete", nil, oldDep)

	newDep := testDeployment("uid-2", "nginx:2.0", time.Now())
	recordToTimelineStore("Deployment", "shop", "web", "uid-2", "add", nil, newDep)

	events, err := timeline.GetStore().Query(context.Background(), timeline.QueryOptions{
		Kinds: []string{"Deployment"}, Namespaces: []string{"shop"},
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	var add, del *timeline.TimelineEvent
	for i := range events {
		switch events[i].EventType {
		case timeline.EventTypeAdd:
			add = &events[i]
		case timeline.EventTypeDelete:
			del = &events[i]
		}
	}
	if del == nil {
		t.Fatal("delete event missing — the timeline must keep the raw delete")
	}
	if add == nil {
		t.Fatal("add event missing")
	}
	if add.Reason != timeline.ReasonRecreated {
		t.Fatalf("add.Reason = %q, want %q", add.Reason, timeline.ReasonRecreated)
	}
	if add.Diff == nil || len(add.Diff.Fields) == 0 {
		t.Fatalf("recreate add carries no diff: %+v", add)
	}
	if !strings.HasPrefix(add.Diff.Summary, "recreated with changes: ") {
		t.Fatalf("summary = %q, want 'recreated with changes: …' prefix", add.Diff.Summary)
	}
	foundImage := false
	for _, f := range add.Diff.Fields {
		if strings.Contains(strings.ToLower(f.Path), "image") {
			foundImage = true
		}
	}
	if !foundImage {
		t.Fatalf("diff lacks the image change: %+v", add.Diff.Fields)
	}
}

// A re-add with the same UID is an informer replay, not a recreate.
func TestRecreateJoin_SameUIDReAdd_NoJoin(t *testing.T) {
	prev := initialSyncComplete
	initialSyncComplete = true
	defer func() { initialSyncComplete = prev }()
	resetRecreateStash()
	defer resetRecreateStash()

	timeline.ResetStore()
	defer timeline.ResetStore()
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 100}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}

	dep := testDeployment("uid-1", "nginx:1.0", time.Now())
	recordToTimelineStore("Deployment", "shop", "web", "uid-1", "delete", nil, dep)
	recordToTimelineStore("Deployment", "shop", "web", "uid-1", "add", nil, dep)

	events, err := timeline.GetStore().Query(context.Background(), timeline.QueryOptions{
		Kinds: []string{"Deployment"}, EventTypes: []timeline.EventType{timeline.EventTypeAdd},
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	for _, e := range events {
		if e.Reason == timeline.ReasonRecreated {
			t.Fatalf("same-UID re-add must not join: %+v", e)
		}
	}
}

func TestTakeRecreateMatch_TTLExpiry(t *testing.T) {
	resetRecreateStash()
	defer resetRecreateStash()

	recreateStashMu.Lock()
	recreateStash[recreateKey("Deployment", "shop", "web")] = recreateEntry{
		obj: testDeployment("uid-1", "nginx:1.0", time.Now()), uid: "uid-1",
		deletedAt: time.Now().Add(-recreateJoinTTL - time.Minute),
	}
	recreateStashMu.Unlock()

	if _, ok := takeRecreateMatch("Deployment", "shop", "web", "uid-2"); ok {
		t.Fatal("expired stash entry must not join")
	}
	recreateStashMu.Lock()
	remaining := len(recreateStash)
	recreateStashMu.Unlock()
	if remaining != 0 {
		t.Fatalf("expired entry should be consumed, %d left", remaining)
	}
}

func TestRecreateStash_KindGateAndReset(t *testing.T) {
	resetRecreateStash()
	defer resetRecreateStash()

	stashDeletedForRecreate("Pod", "shop", "p", "uid-1", &corev1.Pod{})
	if _, ok := takeRecreateMatch("Pod", "shop", "p", "uid-2"); ok {
		t.Fatal("Pod is outside the stash allowlist")
	}

	stashDeletedForRecreate("Deployment", "shop", "web", "uid-1", testDeployment("uid-1", "nginx:1.0", time.Now()))
	resetRecreateStash()
	if _, ok := takeRecreateMatch("Deployment", "shop", "web", "uid-2"); ok {
		t.Fatal("reset must clear the stash")
	}
}

func TestRecreateStash_CapEviction(t *testing.T) {
	resetRecreateStash()
	defer resetRecreateStash()

	for i := 0; i < recreateStashCap+100; i++ {
		stashDeletedForRecreate("ConfigMap", "shop", fmt.Sprintf("cm-%d", i), fmt.Sprintf("uid-%d", i), &corev1.ConfigMap{})
	}
	recreateStashMu.Lock()
	size := len(recreateStash)
	recreateStashMu.Unlock()
	if size > recreateStashCap {
		t.Fatalf("stash size %d exceeds cap %d", size, recreateStashCap)
	}
	// The newest entries must have survived eviction — recreates that matter
	// happen seconds after the delete.
	if _, ok := takeRecreateMatch("ConfigMap", "shop", fmt.Sprintf("cm-%d", recreateStashCap+99), "other-uid"); !ok {
		t.Fatal("newest entry was evicted; eviction must drop oldest first")
	}
}
