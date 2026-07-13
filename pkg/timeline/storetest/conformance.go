// Package storetest exports the EventStore arrival-order contract suite: seq
// assignment, delta paging, duplicate-id collapse, and same-id upsert
// semantics — the invariants whose violation surfaces as silent client-side
// event loss, not an error. Each store enforces them with unrelated mechanisms
// (ring position + mutex counter vs. an atomic seeded from MAX(seq)), so the
// properties live once, here, and every implementation runs the same suite:
// MemoryStore from the pkg module's own tests, SQLiteStore from
// internal/timeline, and any third store from wherever it lives.
//
// The suite is deliberately NOT a complete EventStore certification —
// grouping, statistics, retention, atomicity, and concurrency safety are each
// implementation's own to test.
package storetest

import (
	"context"
	"fmt"
	"testing"
	"time"

	timeline "github.com/skyhook-io/radar/pkg/timeline"
)

// RunConformance runs the EventStore contract suite against a fresh store per
// property. newStore must return an isolated, empty store.
func RunConformance(t *testing.T, newStore func(t *testing.T) timeline.EventStore) {
	t.Helper()
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)

	informer := func(id string, offset time.Duration) timeline.TimelineEvent {
		return timeline.TimelineEvent{
			ID: id, Timestamp: base.Add(offset), Source: timeline.SourceInformer,
			Kind: "Deployment", Namespace: "default", Name: id, EventType: timeline.EventTypeUpdate,
		}
	}
	k8sEvent := func(id string, offset time.Duration, count int32) timeline.TimelineEvent {
		return timeline.TimelineEvent{
			ID: id, Timestamp: base.Add(offset), Source: timeline.SourceK8sEvent,
			Kind: "Pod", Namespace: "default", Name: "web-abc",
			EventType: timeline.EventTypeWarning, Reason: "BackOff", Count: count,
		}
	}
	queryAll := func(t *testing.T, store timeline.EventStore, sinceSeq int64, limit int) []timeline.TimelineEvent {
		t.Helper()
		events, err := store.Query(ctx, timeline.QueryOptions{
			Limit: limit, SinceSeq: sinceSeq,
			IncludeManaged: true, IncludeK8sEvents: true,
		})
		if err != nil {
			t.Fatalf("Query(sinceSeq=%d): %v", sinceSeq, err)
		}
		return events
	}
	frontier := func(t *testing.T, store timeline.EventStore) int64 {
		t.Helper()
		var max int64
		for _, e := range queryAll(t, store, 0, 1000) {
			if e.Seq > max {
				max = e.Seq
			}
		}
		return max
	}
	mustAppend := func(t *testing.T, store timeline.EventStore, e timeline.TimelineEvent) {
		t.Helper()
		if err := store.Append(ctx, e); err != nil {
			t.Fatalf("Append %s: %v", e.ID, err)
		}
	}

	t.Run("append assigns strictly increasing seq in arrival order", func(t *testing.T) {
		store := newStore(t)
		// Mixed arrival paths: a batch (whose rows must each take their own
		// arrival number) followed by single appends.
		batch := []timeline.TimelineEvent{
			informer("ev-0", 0), informer("ev-1", time.Second), informer("ev-2", 2*time.Second),
		}
		if err := store.AppendBatch(ctx, batch); err != nil {
			t.Fatalf("AppendBatch: %v", err)
		}
		mustAppend(t, store, informer("ev-3", 3*time.Second))
		mustAppend(t, store, informer("ev-4", 4*time.Second))

		events := queryAll(t, store, 0, 100)
		if len(events) != 5 {
			t.Fatalf("got %d events, want 5", len(events))
		}
		// A no-cursor query returns newest event time first — what every list
		// consumer renders.
		for i := 1; i < len(events); i++ {
			if events[i].Timestamp.After(events[i-1].Timestamp) {
				t.Fatalf("no-cursor query not newest-first: %s after %s", events[i].ID, events[i-1].ID)
			}
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

	t.Run("delta pages resume a burst beyond the page limit losslessly", func(t *testing.T) {
		store := newStore(t)
		// The client primes its cursor from a full fetch (SinceSeq=0 is "no
		// cursor" — a plain newest-first query, not a delta read)...
		mustAppend(t, store, informer("pre", 0))
		cursor := queryAll(t, store, 0, 100)[0].Seq
		// ...then a burst larger than the page limit arrives. Delta pages must
		// deliver it oldest-first so paging resumes from the lowest unseen seq
		// — a newest-first LIMIT would silently drop the middle.
		for i := range 10 {
			mustAppend(t, store, informer(fmt.Sprintf("burst-%d", i), time.Duration(i+1)*time.Second))
		}
		seen := map[string]bool{}
		for range 10 { // bounded; must terminate long before this
			page := queryAll(t, store, cursor, 3)
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
			mustAppend(t, store, informer(fmt.Sprintf("f-%d", i), time.Duration(i)*time.Second))
		}
		if extra := queryAll(t, store, frontier(t, store), 100); len(extra) != 0 {
			t.Fatalf("delta past the frontier returned %d events", len(extra))
		}
	})

	t.Run("the cursor keys on arrival order, not event time", func(t *testing.T) {
		store := newStore(t)
		mustAppend(t, store, informer("a", 0))
		mustAppend(t, store, informer("b", time.Second))
		cursor := frontier(t, store)
		// A late arrival carrying an OLDER timestamp still lands past the
		// cursor — a time-keyed cursor would silently skip it.
		mustAppend(t, store, informer("late", -time.Hour))
		delta := queryAll(t, store, cursor, 100)
		if len(delta) != 1 || delta[0].ID != "late" {
			t.Fatalf("expected exactly the late arrival past the cursor, got %+v", delta)
		}
		if delta[0].Seq <= cursor {
			t.Fatalf("late arrival seq %d must exceed cursor %d", delta[0].Seq, cursor)
		}
	})

	t.Run("informer relist dupes stay collapsed; a delete is a distinct row", func(t *testing.T) {
		store := newStore(t)
		add := timeline.NewInformerEvent("Deployment", "apps/v1", "default", "web", "uid-1", "100", timeline.EventTypeAdd, timeline.HealthHealthy, nil, nil, nil, nil)
		relist := timeline.NewInformerEvent("Deployment", "apps/v1", "default", "web", "uid-1", "100", timeline.EventTypeUpdate, timeline.HealthHealthy, nil, nil, nil, nil)
		mustAppend(t, store, add)
		cursor := frontier(t, store)
		mustAppend(t, store, relist)
		rows := queryAll(t, store, 0, 100)
		if len(rows) != 1 {
			t.Fatalf("relist dupe produced %d rows, want 1", len(rows))
		}
		// Keep-first: the surviving row is the ORIGINAL observation — same
		// state, same first-observed identity — not a mutation to the relist's
		// operation label.
		if rows[0].EventType != timeline.EventTypeAdd {
			t.Fatalf("relist mutated the row: event type %q, want %q", rows[0].EventType, timeline.EventTypeAdd)
		}
		if delta := queryAll(t, store, cursor, 100); len(delta) != 0 {
			t.Fatalf("relist dupe re-delivered through delta: %v", delta)
		}
		del := timeline.NewInformerEvent("Deployment", "apps/v1", "default", "web", "uid-1", "100", timeline.EventTypeDelete, timeline.HealthUnknown, nil, nil, nil, nil)
		mustAppend(t, store, del)
		if rows := queryAll(t, store, 0, 100); len(rows) != 2 {
			t.Fatalf("delete must be its own row: got %d rows, want 2", len(rows))
		}
	})

	t.Run("k8s event bump upserts one row, refreshes it, and re-arrives at the frontier", func(t *testing.T) {
		store := newStore(t)
		mustAppend(t, store, k8sEvent("evt-uid-1", 0, 1))
		cursor := frontier(t, store)

		bump := k8sEvent("evt-uid-1", time.Minute, 5)
		bump.Message = "back-off 40s"
		mustAppend(t, store, bump)

		delta := queryAll(t, store, cursor, 100)
		if len(delta) != 1 || delta[0].ID != "evt-uid-1" {
			t.Fatalf("bump did not re-arrive at the frontier exactly once: %v", delta)
		}
		row := delta[0]
		if row.Count != 5 || row.Message != "back-off 40s" {
			t.Fatalf("bump lost its refresh: count=%d message=%q", row.Count, row.Message)
		}
		if !row.Timestamp.Equal(base.Add(time.Minute)) {
			t.Fatalf("bump did not refresh the timestamp: %v", row.Timestamp)
		}
		if rows := queryAll(t, store, 0, 100); len(rows) != 1 {
			t.Fatalf("bump duplicated the row: %d rows", len(rows))
		}
	})

	t.Run("a stale out-of-order bump must not clobber the newer row", func(t *testing.T) {
		store := newStore(t)
		mustAppend(t, store, k8sEvent("evt-uid-1", time.Minute, 5))
		mustAppend(t, store, k8sEvent("evt-uid-1", 0, 1)) // older relay of the same uid
		rows := queryAll(t, store, 0, 100)
		if len(rows) != 1 {
			t.Fatalf("expected 1 row, got %d", len(rows))
		}
		if rows[0].Count != 5 || !rows[0].Timestamp.Equal(base.Add(time.Minute)) {
			t.Fatalf("stale relay clobbered the newer row: %+v", rows[0])
		}
	})

	t.Run("a bare bump keeps the row's enrichment; an enriched bump fills a bare row", func(t *testing.T) {
		store := newStore(t)
		born := base.Add(-time.Hour)
		enriched := k8sEvent("evt-uid-1", 0, 1)
		enriched.CreatedAt = &born
		enriched.Owner = &timeline.OwnerInfo{Kind: "ReplicaSet", Name: "web"}
		enriched.Labels = map[string]string{"app": "web"}
		mustAppend(t, store, enriched)
		// A bump that lost its enrichment (tombstone expired, object gone from
		// the live cache) must not erase what the row already knows.
		mustAppend(t, store, k8sEvent("evt-uid-1", time.Minute, 5))
		assertEnriched := func(where string, row *timeline.TimelineEvent) {
			t.Helper()
			if row.Count != 5 {
				t.Fatalf("%s: bump lost its count: %d", where, row.Count)
			}
			if row.CreatedAt == nil || !row.CreatedAt.Equal(born) || row.Owner == nil || row.Owner.Name != "web" || row.Labels["app"] != "web" {
				t.Fatalf("%s: bare bump erased enrichment: %+v", where, row)
			}
		}
		// Through Query — the path every timeline consumer reads.
		rows := queryAll(t, store, 0, 100)
		if len(rows) != 1 {
			t.Fatalf("expected 1 row, got %d", len(rows))
		}
		assertEnriched("Query", &rows[0])
		// And the point lookup.
		row, err := store.GetEvent(ctx, "evt-uid-1")
		if err != nil || row == nil {
			t.Fatalf("GetEvent: %v %+v", err, row)
		}
		assertEnriched("GetEvent", row)

		// The inverse: a bump that carries enrichment wins as the fresher truth.
		mustAppend(t, store, k8sEvent("evt-uid-2", 0, 1))
		filled := k8sEvent("evt-uid-2", time.Minute, 2)
		filled.Owner = &timeline.OwnerInfo{Kind: "Job", Name: "batch"}
		mustAppend(t, store, filled)
		row, err = store.GetEvent(ctx, "evt-uid-2")
		if err != nil || row == nil {
			t.Fatalf("GetEvent: %v %+v", err, row)
		}
		if row.Owner == nil || row.Owner.Name != "batch" {
			t.Fatalf("enriched bump did not fill Owner: %+v", row.Owner)
		}
	})
}
