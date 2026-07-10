package timeline

import (
	"fmt"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
)

func boolPtr(b bool) *bool { return &b }

func testPod(name string, created time.Time) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "shop",
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

func TestTombstoneCache_PutGetHit(t *testing.T) {
	c := NewTombstoneCache(15*time.Minute, 10)
	owner := &OwnerInfo{Kind: "ReplicaSet", Name: "web-rs"}
	ct := time.Now().Add(-time.Hour)
	c.Put("", "v1", "Pod", "shop", "web", TombstoneEntry{Owner: owner, Labels: map[string]string{"app": "web"}, CreatedAt: &ct})

	got, ok := c.Get("", "v1", "Pod", "shop", "web")
	if !ok {
		t.Fatal("expected hit")
	}
	if got.Owner == nil || got.Owner.Name != "web-rs" {
		t.Fatalf("owner not preserved: %+v", got.Owner)
	}
	if got.Labels["app"] != "web" {
		t.Fatalf("labels not preserved: %+v", got.Labels)
	}
	if got.CreatedAt == nil || !got.CreatedAt.Equal(ct) {
		t.Fatalf("createdAt not preserved: %v", got.CreatedAt)
	}
}

func TestTombstoneCache_MissSilent(t *testing.T) {
	c := NewTombstoneCache(15*time.Minute, 10)
	if _, ok := c.Get("", "v1", "Pod", "shop", "nope"); ok {
		t.Fatal("expected miss for never-seen key")
	}
	// Different identity must not collide.
	c.Put("", "v1", "Pod", "shop", "web", TombstoneEntry{})
	if _, ok := c.Get("", "v1", "Pod", "other", "web"); ok {
		t.Fatal("namespace must be part of the key")
	}
	if _, ok := c.Get("", "v1", "Deployment", "shop", "web"); ok {
		t.Fatal("kind must be part of the key")
	}
}

func TestTombstoneCache_TTLExpiry(t *testing.T) {
	c := NewTombstoneCache(15*time.Minute, 10)
	base := time.Now()
	c.now = func() time.Time { return base }
	c.Put("", "v1", "Pod", "shop", "web", TombstoneEntry{Owner: &OwnerInfo{Name: "x"}})

	// Just before expiry: still a hit.
	c.now = func() time.Time { return base.Add(15*time.Minute - time.Second) }
	if _, ok := c.Get("", "v1", "Pod", "shop", "web"); !ok {
		t.Fatal("expected hit just before TTL")
	}

	// Past expiry: miss, and the stale entry is dropped on read.
	c.now = func() time.Time { return base.Add(15*time.Minute + time.Second) }
	if _, ok := c.Get("", "v1", "Pod", "shop", "web"); ok {
		t.Fatal("expected miss past TTL")
	}
	if c.Len() != 0 {
		t.Fatalf("expired entry should be evicted on read, len=%d", c.Len())
	}
}

func TestTombstoneCache_LRUCap(t *testing.T) {
	const cap = 4
	c := NewTombstoneCache(15*time.Minute, cap)
	for i := 0; i < 10; i++ {
		c.Put("", "v1", "Pod", "shop", fmt.Sprintf("p-%d", i), TombstoneEntry{Owner: &OwnerInfo{Name: fmt.Sprintf("o-%d", i)}})
	}
	if c.Len() != cap {
		t.Fatalf("expected len capped at %d, got %d", cap, c.Len())
	}
	// Oldest (p-0..p-5) evicted; the last `cap` survive.
	for i := 0; i < 6; i++ {
		if _, ok := c.Get("", "v1", "Pod", "shop", fmt.Sprintf("p-%d", i)); ok {
			t.Fatalf("expected p-%d evicted", i)
		}
	}
	for i := 6; i < 10; i++ {
		if _, ok := c.Get("", "v1", "Pod", "shop", fmt.Sprintf("p-%d", i)); !ok {
			t.Fatalf("expected p-%d retained", i)
		}
	}
}

func TestTombstoneCache_GetRefreshesLRU(t *testing.T) {
	c := NewTombstoneCache(15*time.Minute, 2)
	c.Put("", "v1", "Pod", "shop", "a", TombstoneEntry{})
	c.Put("", "v1", "Pod", "shop", "b", TombstoneEntry{})
	// Touch "a" so it becomes most-recently-used; adding "c" should evict "b".
	if _, ok := c.Get("", "v1", "Pod", "shop", "a"); !ok {
		t.Fatal("a should be present")
	}
	c.Put("", "v1", "Pod", "shop", "c", TombstoneEntry{})
	if _, ok := c.Get("", "v1", "Pod", "shop", "b"); ok {
		t.Fatal("b should have been evicted as LRU")
	}
	if _, ok := c.Get("", "v1", "Pod", "shop", "a"); !ok {
		t.Fatal("a should survive after being touched")
	}
}

func TestExtractTombstoneEntry(t *testing.T) {
	created := time.Now().Add(-2 * time.Hour)
	pod := testPod("web-abc", created)

	entry, ok := ExtractTombstoneEntry(pod)
	if !ok {
		t.Fatal("expected ok for a pod")
	}
	if entry.Owner == nil || entry.Owner.Kind != "ReplicaSet" || entry.Owner.Name != "web-rs" {
		t.Fatalf("owner not extracted: %+v", entry.Owner)
	}
	if entry.Labels["app.kubernetes.io/name"] != "web" {
		t.Fatalf("labels not extracted: %+v", entry.Labels)
	}
	if entry.CreatedAt == nil || !entry.CreatedAt.Equal(created) {
		t.Fatalf("createdAt not extracted: %v", entry.CreatedAt)
	}
}

// The delete handler often receives the final object inside a
// DeletedFinalStateUnknown wrapper; extraction must unwrap it so the tombstone
// still gets real owner/labels/createdAt.
func TestExtractTombstoneEntry_DeletedFinalStateUnknown(t *testing.T) {
	created := time.Now().Add(-30 * time.Minute)
	pod := testPod("web-xyz", created)
	tomb := cache.DeletedFinalStateUnknown{Key: "shop/web-xyz", Obj: pod}

	entry, ok := ExtractTombstoneEntry(tomb)
	if !ok {
		t.Fatal("expected ok for a wrapped pod")
	}
	if entry.Owner == nil || entry.Owner.Name != "web-rs" {
		t.Fatalf("owner not extracted from wrapper: %+v", entry.Owner)
	}
	if entry.CreatedAt == nil || !entry.CreatedAt.Equal(created) {
		t.Fatalf("createdAt not extracted from wrapper: %v", entry.CreatedAt)
	}
}

func TestExtractTombstoneEntry_NonObject(t *testing.T) {
	if _, ok := ExtractTombstoneEntry("not-an-object"); ok {
		t.Fatal("expected ok=false for a non-object")
	}
	if _, ok := ExtractTombstoneEntry(cache.DeletedFinalStateUnknown{Obj: "junk"}); ok {
		t.Fatal("expected ok=false for a wrapper holding a non-object")
	}
}

// Concurrent Put/Get from many goroutines must not race (run with -race).
func TestTombstoneCache_ConcurrentAccess(t *testing.T) {
	c := NewTombstoneCache(15*time.Minute, 256)
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				name := fmt.Sprintf("p-%d-%d", g, i%50)
				c.Put("", "v1", "Pod", "shop", name, TombstoneEntry{Owner: &OwnerInfo{Name: name}})
				c.Get("", "v1", "Pod", "shop", name)
			}
		}(g)
	}
	wg.Wait()
	if c.Len() > 256 {
		t.Fatalf("cache exceeded capacity under concurrency: %d", c.Len())
	}
}

func TestNewInformerEvent_StampsCreatedAt(t *testing.T) {
	created := time.Now().Add(-90 * time.Minute)
	ev := NewInformerEvent("Pod", "", "shop", "web", "uid-1", "42",
		EventTypeAdd, HealthHealthy, nil, nil, nil, &created)
	if ev.CreatedAt == nil || !ev.CreatedAt.Equal(created) {
		t.Fatalf("informer event did not carry createdAt: %v", ev.CreatedAt)
	}
}

// UID is the primary key: it disambiguates same-named objects across API
// groups and across delete/recreate cycles, and a UID entry is not findable
// by a UID-less (composite) lookup — the silent-miss contract.
func TestTombstoneCache_UIDKeying(t *testing.T) {
	c := NewTombstoneCache(time.Minute, 10)

	c.Put("uid-core", "v1", "Service", "shop", "web", TombstoneEntry{Owner: &OwnerInfo{Name: "core-owner"}})
	c.Put("uid-knative", "serving.knative.dev/v1", "Service", "shop", "web", TombstoneEntry{Owner: &OwnerInfo{Name: "knative-owner"}})

	got, ok := c.Get("uid-core", "v1", "Service", "shop", "web")
	if !ok || got.Owner.Name != "core-owner" {
		t.Fatalf("uid-core lookup = %+v %v, want core-owner", got, ok)
	}
	got, ok = c.Get("uid-knative", "serving.knative.dev/v1", "Service", "shop", "web")
	if !ok || got.Owner.Name != "knative-owner" {
		t.Fatalf("uid-knative lookup = %+v %v, want knative-owner", got, ok)
	}

	// A recreated object (same name, new uid) must not inherit the old entry.
	if _, ok := c.Get("uid-recreated", "v1", "Service", "shop", "web"); ok {
		t.Fatal("new uid must not match the old object's tombstone")
	}
	// A uid-less lookup must not match uid-keyed entries.
	if _, ok := c.Get("", "v1", "Service", "shop", "web"); ok {
		t.Fatal("composite lookup must not match uid-keyed entries")
	}
}

// Get hands entries to enrichment code that stitches them into TimelineEvents;
// a shared map or Owner pointer would let one event's mutation corrupt the
// cache and every sibling event retroactively.
func TestTombstoneCache_GetReturnsClone(t *testing.T) {
	c := NewTombstoneCache(15*time.Minute, 10)
	ct := time.Now().Add(-time.Hour)
	c.Put("", "v1", "Pod", "shop", "web", TombstoneEntry{
		Owner:     &OwnerInfo{Kind: "ReplicaSet", Name: "web-rs"},
		Labels:    map[string]string{"app": "web"},
		CreatedAt: &ct,
	})

	first, ok := c.Get("", "v1", "Pod", "shop", "web")
	if !ok {
		t.Fatal("expected hit")
	}
	first.Labels["app"] = "corrupted"
	first.Labels["extra"] = "x"
	first.Owner.Name = "corrupted-rs"
	*first.CreatedAt = time.Time{}

	second, ok := c.Get("", "v1", "Pod", "shop", "web")
	if !ok {
		t.Fatal("expected hit on second read")
	}
	if second.Labels["app"] != "web" || len(second.Labels) != 1 {
		t.Fatalf("cached labels mutated through Get's return: %+v", second.Labels)
	}
	if second.Owner.Name != "web-rs" {
		t.Fatalf("cached owner mutated through Get's return: %+v", second.Owner)
	}
	if second.CreatedAt == nil || !second.CreatedAt.Equal(ct) {
		t.Fatalf("cached createdAt mutated through Get's return: %v", second.CreatedAt)
	}
}
