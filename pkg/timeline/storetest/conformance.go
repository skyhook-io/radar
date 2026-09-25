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
	"sort"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/resourceid"
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
	queryBefore := func(t *testing.T, store timeline.EventStore, untilSeq int64, limit int) []timeline.TimelineEvent {
		t.Helper()
		events, err := store.Query(ctx, timeline.QueryOptions{
			Limit: limit, UntilSeq: untilSeq,
			IncludeManaged: true, IncludeK8sEvents: true,
		})
		if err != nil {
			t.Fatalf("Query(untilSeq=%d): %v", untilSeq, err)
		}
		return events
	}
	queryLatestArrivals := func(t *testing.T, store timeline.EventStore, limit int) []timeline.TimelineEvent {
		t.Helper()
		events, err := store.Query(ctx, timeline.QueryOptions{
			Limit: limit, SequenceOrder: timeline.SequenceOrderDescending,
			IncludeManaged: true, IncludeK8sEvents: true,
		})
		if err != nil {
			t.Fatalf("Query(sequenceOrder=descending): %v", err)
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
		stats := store.Stats()
		if stats.OldestSeq != seqs["ev-0"] || stats.NewestSeq != seqs["ev-4"] {
			t.Fatalf("store seq bounds = %d..%d, want %d..%d", stats.OldestSeq, stats.NewestSeq, seqs["ev-0"], seqs["ev-4"])
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

	t.Run("backwards pages use arrival order rather than event time", func(t *testing.T) {
		store := newStore(t)
		mustAppend(t, store, informer("first-arrival", 30*time.Second))
		mustAppend(t, store, informer("second-arrival", 10*time.Second))
		mustAppend(t, store, informer("third-arrival", 20*time.Second))

		latest := queryAll(t, store, 0, 10)
		var thirdSeq int64
		for _, event := range latest {
			if event.ID == "third-arrival" {
				thirdSeq = event.Seq
			}
		}
		page := queryBefore(t, store, thirdSeq, 10)
		if len(page) != 2 || page[0].ID != "second-arrival" || page[1].ID != "first-arrival" {
			t.Fatalf("backwards page = %#v, want descending arrival order", page)
		}
	})

	t.Run("bounded sequence snapshots retain late arrivals with old timestamps", func(t *testing.T) {
		store := newStore(t)
		mustAppend(t, store, informer("first-arrival", 30*time.Second))
		mustAppend(t, store, informer("second-arrival", 40*time.Second))
		mustAppend(t, store, informer("late-old-timestamp", -time.Hour))

		page := queryLatestArrivals(t, store, 2)
		if len(page) != 2 || page[0].ID != "late-old-timestamp" || page[1].ID != "second-arrival" {
			t.Fatalf("sequence snapshot = %#v, want the two latest arrivals", page)
		}
		if page[0].Seq <= page[1].Seq {
			t.Fatalf("sequence snapshot not descending: %d then %d", page[0].Seq, page[1].Seq)
		}
		earliest, err := store.Query(ctx, timeline.QueryOptions{
			Limit: 2, SequenceOrder: timeline.SequenceOrderAscending,
			IncludeManaged: true, IncludeK8sEvents: true,
		})
		if err != nil {
			t.Fatalf("Query(sequenceOrder=ascending): %v", err)
		}
		if len(earliest) != 2 || earliest[0].ID != "first-arrival" || earliest[1].ID != "second-arrival" {
			t.Fatalf("ascending sequence snapshot = %#v, want the two earliest arrivals", earliest)
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

	t.Run("a k8s event bump refreshes its subject's namespace and incarnation", func(t *testing.T) {
		store := newStore(t)
		stale := k8sEvent("evt-node-1", 0, 1)
		stale.Kind, stale.Name, stale.Namespace, stale.UID = "Node", "node-a", "default", "node-a"
		mustAppend(t, store, stale)

		corrected := k8sEvent("evt-node-1", time.Minute, 2)
		corrected.Kind, corrected.Name, corrected.Namespace, corrected.UID = "Node", "node-a", "", ""
		mustAppend(t, store, corrected)

		rows := queryAll(t, store, 0, 100)
		if len(rows) != 1 || rows[0].Namespace != "" || rows[0].UID != "" {
			t.Fatalf("bump kept the stale subject: %+v", rows)
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

	t.Run("a re-ingested k8s event moves its owner tuple as one", func(t *testing.T) {
		storedOwner := &timeline.OwnerInfo{Kind: "ReplicaSet", Name: "web-old", APIVersion: "apps/v1", UID: "rs-old"}
		replacement := &timeline.OwnerInfo{Kind: "Job", Name: "batch"}
		for _, tc := range []struct {
			name         string
			storedEvid   timeline.OwnerEvidence
			incoming     *timeline.OwnerInfo
			incomingEvid timeline.OwnerEvidence
			offset       time.Duration
			wantOwner    *timeline.OwnerInfo
			wantEvidence timeline.OwnerEvidence
		}{
			{"a missed lookup keeps what the row knew", timeline.OwnerEnriched, nil, timeline.OwnerMissed, time.Minute, storedOwner, timeline.OwnerEnriched},
			{"a found subject without an owner replaces it", timeline.OwnerEnriched, nil, timeline.OwnerEnriched, time.Minute, nil, timeline.OwnerEnriched},
			{"a found owner replaces every field, not just kind and name", timeline.OwnerEnriched, replacement, timeline.OwnerEnriched, time.Minute, replacement, timeline.OwnerEnriched},
			{"an unidentified subject clears an enriched owner", timeline.OwnerEnriched, nil, timeline.OwnerUnidentified, time.Minute, nil, timeline.OwnerUnidentified},
			{"an unidentified subject clears an owner of unknown provenance", "", nil, timeline.OwnerUnidentified, time.Minute, nil, timeline.OwnerUnidentified},
			{"an unidentified bump at the same instant still applies", timeline.OwnerEnriched, nil, timeline.OwnerUnidentified, 0, nil, timeline.OwnerUnidentified},
			{"an older unidentified revision does not clobber the row", timeline.OwnerEnriched, nil, timeline.OwnerUnidentified, -time.Minute, storedOwner, timeline.OwnerEnriched},
		} {
			t.Run(tc.name, func(t *testing.T) {
				store := newStore(t)
				stored := k8sEvent("evt-owner", 0, 1)
				owner := *storedOwner
				stored.Owner, stored.OwnerEvidence = &owner, tc.storedEvid
				mustAppend(t, store, stored)

				bump := k8sEvent("evt-owner", tc.offset, 2)
				bump.Owner, bump.OwnerEvidence = tc.incoming, tc.incomingEvid
				mustAppend(t, store, bump)

				check := func(where string, got timeline.TimelineEvent) {
					t.Helper()
					if (got.Owner == nil) != (tc.wantOwner == nil) || (got.Owner != nil && *got.Owner != *tc.wantOwner) || got.OwnerEvidence != tc.wantEvidence {
						t.Fatalf("%s: owner = %+v (%q), want %+v (%q)", where, got.Owner, got.OwnerEvidence, tc.wantOwner, tc.wantEvidence)
					}
				}
				rows := queryAll(t, store, 0, 100)
				if len(rows) != 1 {
					t.Fatalf("rows = %d, want 1", len(rows))
				}
				check("Query", rows[0])
				one, err := store.GetEvent(ctx, "evt-owner")
				if err != nil || one == nil {
					t.Fatalf("GetEvent: %v %v", one, err)
				}
				check("GetEvent", *one)
			})
		}
	})

	t.Run("seq paging from zero backfills every row in arrival order", func(t *testing.T) {
		store := newStore(t)
		for i := range 7 {
			mustAppend(t, store, informer(fmt.Sprintf("bf-%d", i), time.Duration(i)*time.Second))
		}
		// A full backfill pages with SeqPaging from cursor 0 — every row,
		// oldest arrival first, resumable by max seq. Plain SinceSeq=0 keeps
		// its historical newest-first meaning (asserted implicitly by every
		// queryAll above); this flag is the ONLY way to page from the floor.
		cursor := int64(0)
		var order []string
		for range 10 { // bounded; must terminate long before this
			page, err := store.Query(ctx, timeline.QueryOptions{
				Limit: 3, SinceSeq: cursor, SeqPaging: true,
				IncludeManaged: true, IncludeK8sEvents: true,
			})
			if err != nil {
				t.Fatalf("Query(seqPaging, cursor=%d): %v", cursor, err)
			}
			if len(page) == 0 {
				break
			}
			for _, e := range page {
				if e.Seq <= cursor {
					t.Fatalf("page returned seq %d not after cursor %d", e.Seq, cursor)
				}
				order = append(order, e.ID)
				cursor = e.Seq
			}
		}
		if len(order) != 7 {
			t.Fatalf("backfill returned %d rows, want 7: %v", len(order), order)
		}
		for i, id := range order {
			if id != fmt.Sprintf("bf-%d", i) {
				t.Fatalf("backfill out of arrival order at %d: %v", i, order)
			}
		}
	})

	// --- Filter and scoping properties -------------------------------------
	//
	// Each store applies these with a different mechanism: the memory store
	// filters in Go, the SQL stores translate to WHERE clauses in two different
	// dialects. Without these properties a backend can ignore a filter field
	// entirely and still satisfy every arrival-order property above, so the
	// omission surfaces as wrong rows rather than as a failing test.

	// A resource of the same kind/namespace/name can exist in two clusters. The
	// store must not let one cluster's history leak into the other's view - the
	// reason clusterContext is carried on the row at all, and the reason it
	// matters most for a store that outlives a context switch.
	t.Run("cluster context scopes reads, and unscoped reads see everything", func(t *testing.T) {
		store := newStore(t)
		a := informer("shared-name", 0)
		a.ID, a.ClusterContext = "ctx-a", "cluster-a"
		b := informer("shared-name", time.Second)
		b.ID, b.ClusterContext = "ctx-b", "cluster-b"
		mustAppend(t, store, a)
		mustAppend(t, store, b)

		scoped, err := store.Query(ctx, timeline.QueryOptions{
			Limit: 100, ClusterContext: "cluster-a",
			IncludeManaged: true, IncludeK8sEvents: true,
		})
		if err != nil {
			t.Fatalf("Query(clusterContext): %v", err)
		}
		if len(scoped) != 1 || scoped[0].ID != "ctx-a" {
			t.Fatalf("cluster scope leaked: %v", idsOfEvents(scoped))
		}
		if scoped[0].ClusterContext != "cluster-a" {
			t.Fatalf("clusterContext not round-tripped: %q", scoped[0].ClusterContext)
		}
		if all := queryAll(t, store, 0, 100); len(all) != 2 {
			t.Fatalf("unscoped read returned %d rows, want both: %v", len(all), idsOfEvents(all))
		}
	})

	// Namespace, kind, name, source and event-type narrowing. One event is the
	// intended match and the others differ in exactly one field, so a store that
	// drops a filter returns extra rows rather than passing by luck.
	t.Run("field filters narrow the result set", func(t *testing.T) {
		store := newStore(t)
		want := informer("match", 0)
		want.Namespace, want.Kind, want.Name = "prod", "Deployment", "api"

		otherNS := want
		otherNS.ID, otherNS.Namespace = "other-ns", "staging"
		otherKind := want
		otherKind.ID, otherKind.Kind = "other-kind", "StatefulSet"
		otherName := want
		otherName.ID, otherName.Name = "other-name", "worker"
		otherSource := want
		otherSource.ID, otherSource.Source = "other-source", timeline.SourceK8sEvent
		otherType := want
		otherType.ID, otherType.EventType = "other-type", timeline.EventTypeDelete

		for _, e := range []timeline.TimelineEvent{want, otherNS, otherKind, otherName, otherSource, otherType} {
			mustAppend(t, store, e)
		}

		check := func(name string, opts timeline.QueryOptions, wantIDs ...string) {
			t.Helper()
			opts.Limit, opts.IncludeManaged, opts.IncludeK8sEvents = 100, true, true
			got, err := store.Query(ctx, opts)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			gotIDs := idsOfEvents(got)
			sort.Strings(gotIDs)
			expect := append([]string(nil), wantIDs...)
			sort.Strings(expect)
			if fmt.Sprint(gotIDs) != fmt.Sprint(expect) {
				t.Errorf("%s: got %v, want %v", name, gotIDs, expect)
			}
		}

		check("Namespaces", timeline.QueryOptions{Namespaces: []string{"prod"}},
			"match", "other-kind", "other-name", "other-source", "other-type")
		check("Kinds", timeline.QueryOptions{Kinds: []string{"Deployment"}},
			"match", "other-ns", "other-name", "other-source", "other-type")
		check("Names", timeline.QueryOptions{Names: []string{"api"}},
			"match", "other-ns", "other-kind", "other-source", "other-type")
		check("Sources", timeline.QueryOptions{Sources: []timeline.EventSource{timeline.SourceK8sEvent}},
			"other-source")
		check("EventTypes", timeline.QueryOptions{EventTypes: []timeline.EventType{timeline.EventTypeDelete}},
			"other-type")
		check("ExcludeDeleted", timeline.QueryOptions{ExcludeDeleted: true},
			"match", "other-ns", "other-kind", "other-name", "other-source")
	})

	// APIGroups narrows on the group parsed from APIVersion before the limit
	// applies, so same-kind rows from another group cannot crowd out the
	// requested resource's history. Rows with no recorded APIVersion are
	// unknown rather than mismatches and stay visible under every group filter.
	t.Run("api group filter runs before the limit and keeps unknown versions", func(t *testing.T) {
		store := newStore(t)
		web := func(id string, offset time.Duration, apiVersion string) timeline.TimelineEvent {
			e := informer(id, offset)
			e.Name, e.APIVersion = "web", apiVersion
			return e
		}
		mustAppend(t, store, web("matching", 0, "apps/v1"))
		mustAppend(t, store, web("unknown", time.Minute, ""))
		mustAppend(t, store, web("wrong-core", 2*time.Minute, "v1"))
		for i := 0; i < 5; i++ {
			mustAppend(t, store, web(fmt.Sprintf("wrong-%d", i), 3*time.Minute+time.Duration(i)*time.Second, "other.example/v1"))
		}

		query := func(name string, groups []string, limit int) []string {
			t.Helper()
			got, err := store.Query(ctx, timeline.QueryOptions{
				Kinds: []string{"Deployment"}, Names: []string{"web"}, APIGroups: groups,
				Limit: limit, IncludeManaged: true,
			})
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			return idsOfEvents(got)
		}

		if got := query("apps", []string{"apps"}, 2); fmt.Sprint(got) != "[unknown matching]" {
			t.Errorf("apps group with limit 2: got %v, want [unknown matching]", got)
		}
		if got := query("core", []string{""}, 10); fmt.Sprint(got) != "[wrong-core unknown]" {
			t.Errorf("core group: got %v, want [wrong-core unknown]", got)
		}
		exact, err := store.Query(ctx, timeline.QueryOptions{
			Kinds: []string{"Deployment"}, Names: []string{"web"}, APIGroups: []string{"apps"},
			RequireAPIVersion: true, Limit: 1, IncludeManaged: true,
		})
		if err != nil || fmt.Sprint(idsOfEvents(exact)) != "[matching]" {
			t.Fatalf("exact identity filter must run before limit: got %v, error %v", idsOfEvents(exact), err)
		}

	})

	// A workload's history: the workload, what it owns (by owner UID), K8s
	// Events about any of those (subject UID), and attached resources by key.
	scopedFixture := func(t *testing.T, store timeline.EventStore) {
		t.Helper()
		row := func(id string, offset time.Duration, src timeline.EventSource, apiVersion, kind, name, uid string, owner *timeline.OwnerInfo) timeline.TimelineEvent {
			return timeline.TimelineEvent{
				ID: id, Timestamp: base.Add(offset), Source: src, ClusterContext: "ctx-a",
				APIVersion: apiVersion, Kind: kind, Namespace: "default", Name: name, UID: uid,
				Owner: owner, EventType: timeline.EventTypeUpdate,
			}
		}
		ownedBy := func(kind, name, uid string) *timeline.OwnerInfo {
			return &timeline.OwnerInfo{Kind: kind, Name: name, UID: uid}
		}
		mustAppend(t, store, row("dep", 0, timeline.SourceInformer, "apps/v1", "Deployment", "web", "uid-dep", nil))
		mustAppend(t, store, row("rs", time.Minute, timeline.SourceInformer, "apps/v1", "ReplicaSet", "web-1", "uid-rs", ownedBy("Deployment", "web", "uid-dep")))
		mustAppend(t, store, row("pod", 2*time.Minute, timeline.SourceInformer, "v1", "Pod", "web-1-a", "uid-pod", ownedBy("ReplicaSet", "web-1", "uid-rs")))
		podEvent := row("pod-event", 3*time.Minute, timeline.SourceK8sEvent, "v1", "Pod", "web-1-a", "uid-pod", ownedBy("ReplicaSet", "web-1", "uid-rs"))
		podEvent.EventType = timeline.EventTypeWarning
		mustAppend(t, store, podEvent)
		mustAppend(t, store, row("svc", 4*time.Minute, timeline.SourceInformer, "v1", "Service", "web", "uid-svc", nil))
		// Same name, other API group: not the workload.
		mustAppend(t, store, row("collision", 5*time.Minute, timeline.SourceInformer, "other.example/v1", "Deployment", "web", "uid-other-web", nil))
		// A sibling workload and its ReplicaSet, newer than everything in scope.
		for i := 0; i < 5; i++ {
			mustAppend(t, store, row(fmt.Sprintf("sibling-%d", i), 6*time.Minute+time.Duration(i)*time.Second, timeline.SourceInformer, "apps/v1", "ReplicaSet", "api-1", "uid-api-rs", ownedBy("Deployment", "api", "uid-api")))
		}
		// Another cluster's pod owned by a same-UID ReplicaSet must not leak in.
		other := row("other-cluster-pod", 7*time.Minute, timeline.SourceInformer, "v1", "Pod", "web-1-b", "uid-pod-b", ownedBy("ReplicaSet", "web-1", "uid-rs"))
		other.ClusterContext = "ctx-b"
		mustAppend(t, store, other)
	}

	t.Run("resource scope selects by subject uid, owner uid or exact ref before the limit", func(t *testing.T) {
		store := newStore(t)
		scopedFixture(t, store)
		query := func(scope timeline.ResourceScope, limit int) []string {
			t.Helper()
			got, err := store.Query(ctx, timeline.QueryOptions{
				Scope: scope, ClusterContext: "ctx-a", Limit: limit,
				IncludeManaged: true, IncludeK8sEvents: true,
			})
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			ids := idsOfEvents(got)
			sort.Strings(ids)
			return ids
		}
		workload := timeline.ResourceScope{
			UIDs: []string{"uid-dep", "uid-rs", "uid-pod"},
			Refs: []resourceid.Ref{resourceid.NewRef("apps", "Deployment", "default", "web"), resourceid.NewRef("", "Service", "default", "web")},
		}
		if got := query(workload, 5); fmt.Sprint(got) != "[dep pod pod-event rs svc]" {
			t.Errorf("workload scope with limit 5: got %v, want [dep pod pod-event rs svc]", got)
		}
		if got := query(timeline.ResourceScope{OwnerUIDs: []string{"uid-rs"}}, 10); fmt.Sprint(got) != "[pod pod-event]" {
			t.Errorf("owner uid scope: got %v, want [pod pod-event]", got)
		}
		if got := query(timeline.ResourceScope{Refs: []resourceid.Ref{resourceid.NewRef("apps", "Deployment", "default", "web")}}, 10); fmt.Sprint(got) != "[dep]" {
			t.Errorf("ref scope must honor the API group: got %v, want [dep]", got)
		}
	})

	t.Run("owned uids walk one ownership level, distinct, within a cluster", func(t *testing.T) {
		store := newStore(t)
		scopedFixture(t, store)
		owned := func(clusterContext string, owners ...string) []string {
			t.Helper()
			got, err := store.OwnedUIDs(ctx, clusterContext, owners, 100)
			if err != nil {
				t.Fatalf("OwnedUIDs: %v", err)
			}
			sort.Strings(got)
			return got
		}
		if got := owned("ctx-a", "uid-dep"); fmt.Sprint(got) != "[uid-rs]" {
			t.Errorf("deployment's children: got %v, want [uid-rs]", got)
		}
		if got := owned("ctx-a", "uid-rs"); fmt.Sprint(got) != "[uid-pod]" {
			t.Errorf("replicaset's children (pod row and its k8s event collapse): got %v, want [uid-pod]", got)
		}
		if got := owned("", "uid-rs"); fmt.Sprint(got) != "[uid-pod uid-pod-b]" {
			t.Errorf("unscoped cluster context sees every cluster: got %v", got)
		}
		if got := owned("ctx-a"); len(got) != 0 {
			t.Errorf("no owners: got %v, want none", got)
		}
	})

	// Time-range narrowing is separate from arrival-order narrowing: Since/Until
	// bound the event's own timestamp, which for a k8s Event is when the cluster
	// says it happened, not when Radar saw it.
	t.Run("since and until bound the event time", func(t *testing.T) {
		store := newStore(t)
		for i := 0; i < 5; i++ {
			mustAppend(t, store, informer(fmt.Sprintf("t-%d", i), time.Duration(i)*time.Minute))
		}
		got, err := store.Query(ctx, timeline.QueryOptions{
			Limit: 100, Since: base.Add(time.Minute), Until: base.Add(3 * time.Minute),
			IncludeManaged: true, IncludeK8sEvents: true,
		})
		if err != nil {
			t.Fatalf("Query(since,until): %v", err)
		}
		ids := idsOfEvents(got)
		sort.Strings(ids)
		if fmt.Sprint(ids) != fmt.Sprint([]string{"t-1", "t-2", "t-3"}) {
			t.Fatalf("since/until window = %v, want [t-1 t-2 t-3]", ids)
		}
	})

	// Whatever a store is handed, it must hand back. Each backend encodes these
	// differently - Go structs in memory, TEXT and a fixed-width time layout in
	// SQLite, jsonb and bigint nanoseconds in PostgreSQL - and only a
	// round-trip through the store surface catches an encoding that quietly
	// loses precision or drops a nested field.
	t.Run("event payload round-trips through the store", func(t *testing.T) {
		store := newStore(t)
		// Deliberately sub-microsecond, and not taken from the wall clock: on
		// darwin time.Now() is already microsecond-resolution, so a fixture
		// built from it cannot detect a backend that truncates to microseconds.
		// These fixed offsets make the property fail on every platform, not
		// only the ones with a nanosecond clock.
		born := base.Add(-30*time.Minute + 456789321*time.Nanosecond).Truncate(time.Nanosecond)
		want := informer("payload", 0)
		want.Timestamp = base.Add(819945123 * time.Nanosecond)
		want.APIVersion, want.UID, want.CorrelationID = "apps/v1", "uid-9", "corr-9"
		want.Reason, want.Message = "Scaled", "scaled up"
		want.HealthState = timeline.HealthHealthy
		want.ClusterContext = "cluster-x"
		want.CreatedAt = &born
		want.Owner = &timeline.OwnerInfo{Kind: "ReplicaSet", Name: "api-rs", APIVersion: "apps/v1", UID: "rs-uid"}
		want.OwnerEvidence = timeline.OwnerObserved
		want.Labels = map[string]string{"app": "api", "tier": "back"}
		want.Diff = &timeline.DiffInfo{
			Summary: "replicas 1 -> 3",
			Fields:  []timeline.FieldChange{{Path: "spec.replicas", OldValue: "1", NewValue: "3"}},
		}
		mustAppend(t, store, want)

		verify := func(where string, got *timeline.TimelineEvent) {
			t.Helper()
			if !got.Timestamp.Equal(want.Timestamp) {
				t.Errorf("%s: timestamp = %v (%d ns), want %v (%d ns)", where,
					got.Timestamp, got.Timestamp.UnixNano(), want.Timestamp, want.Timestamp.UnixNano())
			}
			if got.CreatedAt == nil || !got.CreatedAt.Equal(born) {
				t.Errorf("%s: createdAt = %v, want %v", where, got.CreatedAt, born)
			}
			if got.APIVersion != want.APIVersion || got.UID != want.UID ||
				got.CorrelationID != want.CorrelationID || got.Reason != want.Reason ||
				got.Message != want.Message || got.HealthState != want.HealthState ||
				got.ClusterContext != want.ClusterContext {
				t.Errorf("%s: scalar field lost: %+v", where, got)
			}
			if got.Owner == nil || *got.Owner != *want.Owner || got.OwnerEvidence != want.OwnerEvidence {
				t.Errorf("%s: owner = %+v (%q), want %+v (%q)", where, got.Owner, got.OwnerEvidence, want.Owner, want.OwnerEvidence)
			}
			if len(got.Labels) != 2 || got.Labels["app"] != "api" || got.Labels["tier"] != "back" {
				t.Errorf("%s: labels = %v", where, got.Labels)
			}
			if got.Diff == nil || got.Diff.Summary != want.Diff.Summary || len(got.Diff.Fields) != 1 {
				t.Errorf("%s: diff = %+v", where, got.Diff)
			} else if f := got.Diff.Fields[0]; f.Path != "spec.replicas" ||
				fmt.Sprint(f.OldValue) != "1" || fmt.Sprint(f.NewValue) != "3" {
				t.Errorf("%s: diff field = %+v", where, f)
			}
		}

		one, err := store.GetEvent(ctx, "payload")
		if err != nil || one == nil {
			t.Fatalf("GetEvent: %v %+v", err, one)
		}
		verify("GetEvent", one)

		page := queryAll(t, store, 0, 10)
		if len(page) != 1 {
			t.Fatalf("Query returned %d rows, want 1", len(page))
		}
		verify("Query", &page[0])
	})

	// Stats backs the retention-gap headers and the Capacity Activity coverage
	// notes. A store that leaves a field at its zero value reports "no gap" and
	// "nothing evicted", which reads as clean history rather than missing data.
	t.Run("stats report counts, seq bounds and event times", func(t *testing.T) {
		store := newStore(t)
		for i := 0; i < 3; i++ {
			mustAppend(t, store, informer(fmt.Sprintf("s-%d", i), time.Duration(i)*time.Minute))
		}
		stats := store.Stats()
		if stats.TotalEvents != 3 {
			t.Errorf("TotalEvents = %d, want 3", stats.TotalEvents)
		}
		if stats.OldestSeq <= 0 || stats.NewestSeq <= stats.OldestSeq {
			t.Errorf("seq bounds = %d..%d, want an increasing pair", stats.OldestSeq, stats.NewestSeq)
		}
		if !stats.OldestEvent.Equal(base) {
			t.Errorf("OldestEvent = %v, want %v", stats.OldestEvent, base)
		}
		if !stats.NewestEvent.Equal(base.Add(2 * time.Minute)) {
			t.Errorf("NewestEvent = %v, want %v", stats.NewestEvent, base.Add(2*time.Minute))
		}
	})

	// The seen-set decides whether a resource's first sighting is reported as a
	// change. It is keyed by cluster as well as identity, so the same workload in
	// two clusters is two distinct sightings.
	t.Run("seen resources are tracked per cluster and can be cleared", func(t *testing.T) {
		store := newStore(t)
		if store.IsResourceSeen("cluster-a", "", "Deployment", "default", "api") {
			t.Fatal("resource reported seen before it was marked")
		}
		store.MarkResourceSeen("cluster-a", "", "Deployment", "default", "api")
		if !store.IsResourceSeen("cluster-a", "", "Deployment", "default", "api") {
			t.Fatal("resource not reported seen after MarkResourceSeen")
		}
		if store.IsResourceSeen("cluster-b", "", "Deployment", "default", "api") {
			t.Fatal("seen state leaked across cluster contexts")
		}
		store.ClearResourceSeen("cluster-a", "", "Deployment", "default", "api")
		if store.IsResourceSeen("cluster-a", "", "Deployment", "default", "api") {
			t.Fatal("resource still reported seen after ClearResourceSeen")
		}
	})

	// Same Kind/namespace/name can legitimately name two different resources
	// in different API groups (a core Service vs. a Knative Service, say) —
	// their seen state and clears must stay independent.
	t.Run("seen resources are isolated by API group and cleared independently", func(t *testing.T) {
		store := newStore(t)
		store.MarkResourceSeen("cluster-a", "", "Service", "shop", "api")
		store.MarkResourceSeen("cluster-a", "serving.knative.dev", "Service", "shop", "api")

		if !store.IsResourceSeen("cluster-a", "", "Service", "shop", "api") {
			t.Fatal("core Service should be seen")
		}
		if !store.IsResourceSeen("cluster-a", "serving.knative.dev", "Service", "shop", "api") {
			t.Fatal("Knative Service should be seen independently of the core Service")
		}

		store.ClearResourceSeen("cluster-a", "", "Service", "shop", "api")
		if store.IsResourceSeen("cluster-a", "", "Service", "shop", "api") {
			t.Fatal("core Service should be cleared")
		}
		if !store.IsResourceSeen("cluster-a", "serving.knative.dev", "Service", "shop", "api") {
			t.Fatal("clearing the core Service must not clear the Knative Service in another group")
		}
	})
}

func idsOfEvents(events []timeline.TimelineEvent) []string {
	ids := make([]string, len(events))
	for i, e := range events {
		ids[i] = e.ID
	}
	return ids
}
