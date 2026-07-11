package k8s

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/timeline"
)

// The cache layer computes the update diff once and hands it to
// recordToTimelineStore (diffPrecomputed=true) instead of recomputing it. This
// pins the invariant that the precomputed path produces a timeline entry
// identical to the recompute path — i.e. reusing the diff changed nothing.
func TestRecordToTimelineStore_PrecomputedDiffMatchesRecompute(t *testing.T) {
	now := time.Now()
	oldDep := testDeployment("uid-1", "nginx:1.0", now)
	newDep := testDeployment("uid-1", "nginx:2.0", now)

	pre := ComputeDiff("Deployment", oldDep, newDep)
	if pre == nil {
		t.Fatal("expected a non-nil diff for an image change")
	}

	record := func(precomputed *DiffInfo, diffPrecomputed bool) *timeline.DiffInfo {
		t.Helper()
		timeline.ResetStore()
		if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 10}); err != nil {
			t.Fatalf("InitStore: %v", err)
		}
		recordToTimelineStore(ActiveClusterContext(), "Deployment", "shop", "web", "uid-1", "update", oldDep, newDep, precomputed, diffPrecomputed)
		events, err := timeline.GetStore().Query(context.Background(), timeline.QueryOptions{Kinds: []string{"Deployment"}})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(events) != 1 {
			t.Fatalf("expected exactly 1 recorded event, got %d", len(events))
		}
		return events[0].Diff
	}

	got := record(pre, true)   // production path: reuse the cache-computed diff
	want := record(nil, false) // fallback path: recompute in recordToTimelineStore
	t.Cleanup(timeline.ResetStore)

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("precomputed-diff timeline output differs from recompute:\n got=%+v\nwant=%+v", got, want)
	}
}
