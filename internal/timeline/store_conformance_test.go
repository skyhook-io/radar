package timeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The Seq/delta contract every EventStore must uphold identically — the two
// implementations enforce it with unrelated mechanisms (ring position + mutex
// counter vs. an atomic seeded from MAX(seq)), and a violation surfaces as
// silent client-side event loss, not an error. Each property runs against
// both stores.
func runStoreConformance(t *testing.T, newStore func(t *testing.T) EventStore) {
	t.Helper()
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)

	informer := func(id string, offset time.Duration) TimelineEvent {
		return TimelineEvent{
			ID: id, Timestamp: base.Add(offset), Source: SourceInformer,
			Kind: "Deployment", Namespace: "default", Name: id, EventType: EventTypeUpdate,
		}
	}
	queryAll := func(store EventStore, sinceSeq int64, limit int) []TimelineEvent {
		t.Helper()
		events, err := store.Query(ctx, QueryOptions{
			Limit: limit, SinceSeq: sinceSeq,
			IncludeManaged: true, IncludeK8sEvents: true,
		})
		if err != nil {
			t.Fatalf("Query(sinceSeq=%d): %v", sinceSeq, err)
		}
		return events
	}

	t.Run("append assigns strictly increasing seq in arrival order", func(t *testing.T) {
		store := newStore(t)
		for i := range 5 {
			if err := store.Append(ctx, informer(fmt.Sprintf("ev-%d", i), time.Duration(i)*time.Second)); err != nil {
				t.Fatalf("Append: %v", err)
			}
		}
		events := queryAll(store, 0, 100)
		if len(events) != 5 {
			t.Fatalf("got %d events, want 5", len(events))
		}
		seqs := map[string]int64{}
		for _, e := range events {
			if e.Seq <= 0 {
				t.Fatalf("event %s has unassigned seq %d", e.ID, e.Seq)
			}
			seqs[e.ID] = e.Seq
		}
		for i := 1; i < 5; i++ {
			prev, cur := seqs[fmt.Sprintf("ev-%d", i-1)], seqs[fmt.Sprintf("ev-%d", i)]
			if cur <= prev {
				t.Fatalf("seq not increasing with arrival: ev-%d=%d, ev-%d=%d", i-1, prev, i, cur)
			}
		}
	})

	t.Run("delta pages ascend by seq and resume a burst beyond limit losslessly", func(t *testing.T) {
		store := newStore(t)
		// The client primes its cursor from a full fetch (SinceSeq=0 is "no
		// cursor" — a plain newest-first query, not a delta read)...
		if err := store.Append(ctx, informer("pre", 0)); err != nil {
			t.Fatalf("Append: %v", err)
		}
		cursor := queryAll(store, 0, 100)[0].Seq
		// ...then a burst larger than the page limit arrives. Delta pages must
		// deliver it oldest-first so paging resumes from the lowest unseen seq
		// — a newest-first LIMIT would silently drop the middle.
		for i := range 10 {
			if err := store.Append(ctx, informer(fmt.Sprintf("burst-%d", i), time.Duration(i+1)*time.Second)); err != nil {
				t.Fatalf("Append: %v", err)
			}
		}
		seen := map[string]bool{}
		for range 10 { // bounded; must terminate long before this
			page := queryAll(store, cursor, 3)
			if len(page) == 0 {
				break
			}
			if len(page) > 3 {
				t.Fatalf("page exceeded limit: %d", len(page))
			}
			for i, e := range page {
				if e.Seq <= cursor {
					t.Fatalf("page returned seq %d at or below cursor %d", e.Seq, cursor)
				}
				if i > 0 && page[i].Seq < page[i-1].Seq {
					t.Fatalf("delta page not ascending: %d after %d", page[i].Seq, page[i-1].Seq)
				}
				if seen[e.ID] {
					t.Fatalf("event %s delivered twice", e.ID)
				}
				seen[e.ID] = true
				if e.Seq > cursor {
					cursor = e.Seq
				}
			}
		}
		if len(seen) != 10 {
			t.Fatalf("burst paging lost events: delivered %d of 10", len(seen))
		}
	})

	t.Run("cursor at the frontier yields an empty delta", func(t *testing.T) {
		store := newStore(t)
		for i := range 3 {
			if err := store.Append(ctx, informer(fmt.Sprintf("f-%d", i), time.Duration(i)*time.Second)); err != nil {
				t.Fatalf("Append: %v", err)
			}
		}
		all := queryAll(store, 0, 100)
		var frontier int64
		for _, e := range all {
			if e.Seq > frontier {
				frontier = e.Seq
			}
		}
		if extra := queryAll(store, frontier, 100); len(extra) != 0 {
			t.Fatalf("delta past the frontier returned %d events", len(extra))
		}
	})

	t.Run("informer relist dupes stay collapsed and are not re-delivered", func(t *testing.T) {
		store := newStore(t)
		first := informer("dup", 0)
		if err := store.Append(ctx, first); err != nil {
			t.Fatalf("Append: %v", err)
		}
		all := queryAll(store, 0, 100)
		frontier := all[0].Seq
		// A relist re-emits the identical id; the row must not duplicate and
		// must not re-arrive at the delta frontier.
		if err := store.Append(ctx, first); err != nil {
			t.Fatalf("re-Append: %v", err)
		}
		if rows := queryAll(store, 0, 100); len(rows) != 1 {
			t.Fatalf("relist dupe produced %d rows, want 1", len(rows))
		}
		if delta := queryAll(store, frontier, 100); len(delta) != 0 {
			t.Fatalf("relist dupe re-delivered through delta: %v", delta)
		}
	})

	t.Run("k8s event count bump re-arrives at the delta frontier exactly once", func(t *testing.T) {
		store := newStore(t)
		bump := TimelineEvent{
			ID: "evt-uid-1", Timestamp: base, Source: SourceK8sEvent,
			Kind: "Pod", Namespace: "default", Name: "web", EventType: EventTypeWarning,
			Reason: "BackOff", Count: 1,
		}
		if err := store.Append(ctx, bump); err != nil {
			t.Fatalf("Append: %v", err)
		}
		all := queryAll(store, 0, 100)
		frontier := all[0].Seq

		bump.Count = 5
		bump.Timestamp = base.Add(time.Minute)
		if err := store.Append(ctx, bump); err != nil {
			t.Fatalf("bump Append: %v", err)
		}
		delta := queryAll(store, frontier, 100)
		if len(delta) != 1 || delta[0].ID != "evt-uid-1" {
			t.Fatalf("bump did not re-arrive at the frontier exactly once: %v", delta)
		}
		if delta[0].Count != 5 {
			t.Fatalf("bump lost its count: %d", delta[0].Count)
		}
		if rows := queryAll(store, 0, 100); len(rows) != 1 {
			t.Fatalf("bump duplicated the row: %d rows", len(rows))
		}
	})
}

func TestStoreConformance_Memory(t *testing.T) {
	runStoreConformance(t, func(t *testing.T) EventStore {
		return NewMemoryStore(100)
	})
}

func TestStoreConformance_SQLite(t *testing.T) {
	runStoreConformance(t, func(t *testing.T) EventStore {
		t.Helper()
		dir, err := os.MkdirTemp("", "timeline-conformance-*")
		if err != nil {
			t.Fatalf("MkdirTemp: %v", err)
		}
		t.Cleanup(func() { os.RemoveAll(dir) })
		store, err := NewSQLiteStore(filepath.Join(dir, "test.db"))
		if err != nil {
			t.Fatalf("NewSQLiteStore: %v", err)
		}
		t.Cleanup(func() { store.Close() })
		return store
	})
}
