//go:build replay

package meaningfulchanges

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/k8score"
)

// TestReplayDefaultFeed replays a recorded SQLite timeline (a copy: the store
// may migrate it) through the default feed every RADAR_REPLAY_STEP (10m)
// with and without the other-kinds read, and writes what reached the page to
// RADAR_REPLAY_OUT as JSON. It measures the noise the read adds and the
// curated rows it displaces, and grows churnKinds.
//
//	RADAR_REPLAY_DB=/path/copy.db RADAR_REPLAY_OUT=out.json go test -tags replay -run TestReplayDefaultFeed ./internal/meaningfulchanges/
func TestReplayDefaultFeed(t *testing.T) {
	path, outPath := os.Getenv("RADAR_REPLAY_DB"), os.Getenv("RADAR_REPLAY_OUT")
	if path == "" || outPath == "" {
		t.Skip("RADAR_REPLAY_DB and RADAR_REPLAY_OUT are not set")
	}
	step := 10 * time.Minute
	if v := os.Getenv("RADAR_REPLAY_STEP"); v != "" {
		var err error
		if step, err = time.ParseDuration(v); err != nil {
			t.Fatal(err)
		}
	}
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeSQLite, Path: path}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store := timeline.GetStore()
	all, err := store.Query(ctx, timeline.QueryOptions{Limit: 1, Sources: []timeline.EventSource{timeline.SourceInformer}, IncludeManaged: true, SequenceOrder: timeline.SequenceOrderAscending})
	if err != nil || len(all) == 0 {
		t.Fatalf("first event: %v %v", all, err)
	}
	first := all[0].Timestamp
	// The feed reads the active context's rows; replay the recorded one.
	previous := k8s.SetTestContextName(all[0].ClusterContext)
	t.Cleanup(func() { k8s.SetTestContextName(previous) })
	newest, err := store.Query(ctx, timeline.QueryOptions{Limit: 1, Sources: []timeline.EventSource{timeline.SourceInformer}, IncludeManaged: true})
	if err != nil || len(newest) == 0 {
		t.Fatalf("last event: %v %v", newest, err)
	}
	last := newest[0].Timestamp

	type pageRow struct {
		Key, Kind, Group, Category string
	}
	type stepResult struct {
		At      time.Time
		NewRows []pageRow // other-kinds rows on the page
		// Curated spec_config rows the baseline page had and this one
		// doesn't: those other-kinds rows took the place of, and those the
		// per-kind cap gave to other curated kinds.
		DisplacedByOther []pageRow
		DisplacedByCap   []pageRow
		OtherCounts      map[string]int
		Churn            []KindCount
		Crowded          []KindCount
		BaselineMillis   float64
		WidenedMillis    float64
		Saturated        bool
	}
	var steps []stepResult
	rowOf := func(c issuesapi.RecentChange) pageRow {
		g := changeGroupKind(c)
		return pageRow{Key: fmt.Sprintf("%s/%s/%s/%s/%s", c.Kind, c.Namespace, c.Name, c.ChangeType, c.Timestamp), Kind: g.Kind, Group: g.Group, Category: string(c.ChangeCategory)}
	}
	// Windows ending in the first hour start before the recording, like a
	// query against a Radar that started recently.
	for at := first.Add(step); !at.After(last); at = at.Add(step) {
		q := Query{Since: time.Hour, Limit: 50, start: at.Add(-time.Hour), end: at}
		wq := q
		wq.OtherKinds, wq.Uncapped = true, true
		// The fastest of three runs, so other load on the host counts less.
		timed := func(q Query) (RecentResult, float64) {
			var result RecentResult
			best := time.Duration(1 << 62)
			for range 3 {
				began := time.Now()
				var err error
				if result, err = Recent(ctx, q); err != nil {
					t.Fatal(err)
				}
				best = min(best, time.Since(began))
			}
			return result, float64(best.Microseconds()) / 1000
		}
		base, baseMillis := timed(q)
		basePage := base.Changes
		RankAndCap(&basePage, 20)
		wide, wideMillis := timed(wq)
		sel := SelectFeed(wide.Changes, 20)
		// The page the per-kind cap alone picks, without other-kinds rows,
		// tells their displacement apart from the cap's.
		curatedOnly := slices.DeleteFunc(slices.Clone(wide.Changes), func(c issuesapi.RecentChange) bool { return !curatedGroupKind(changeGroupKind(c)) })
		capPage := map[string]bool{}
		for _, c := range SelectFeed(curatedOnly, 20).Changes {
			capPage[rowOf(c).Key] = true
		}

		res := stepResult{At: at, OtherCounts: map[string]int{}, Churn: sel.Churn, Crowded: sel.Crowded,
			BaselineMillis: baseMillis, WidenedMillis: wideMillis, Saturated: wide.FetchSaturated}
		onPage := map[string]bool{}
		for _, c := range sel.Changes {
			r := rowOf(c)
			onPage[r.Key] = true
			if !curatedGroupKind(changeGroupKind(c)) {
				res.NewRows = append(res.NewRows, r)
			}
		}
		for _, c := range wide.Changes {
			if g := changeGroupKind(c); !curatedGroupKind(g) {
				res.OtherCounts[g.Kind+"."+g.Group]++
			}
		}
		for _, c := range basePage {
			r := rowOf(c)
			if c.ChangeCategory != issuesapi.ChangeCategorySpecConfig || onPage[r.Key] {
				continue
			}
			if capPage[r.Key] {
				res.DisplacedByOther = append(res.DisplacedByOther, r)
			} else {
				res.DisplacedByCap = append(res.DisplacedByCap, r)
			}
		}
		steps = append(steps, res)
	}
	sort.Slice(steps, func(i, j int) bool { return steps[i].At.Before(steps[j].At) })
	b, err := json.MarshalIndent(map[string]any{"first": first, "last": last, "step": step.String(), "steps": steps}, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outPath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d steps from %s to %s", len(steps), first, last)
}

// TestReplayReadCost times each other-kinds store read over the replayed
// windows, with and without the desired-state filter, to tell the JSON
// predicate's cost from the scan's.
func TestReplayReadCost(t *testing.T) {
	path := os.Getenv("RADAR_REPLAY_DB")
	if path == "" {
		t.Skip("RADAR_REPLAY_DB is not set")
	}
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeSQLite, Path: path}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store := timeline.GetStore()
	informer := []timeline.EventSource{timeline.SourceInformer}
	oldest, err := store.Query(ctx, timeline.QueryOptions{Limit: 1, Sources: informer, IncludeManaged: true, SequenceOrder: timeline.SequenceOrderAscending})
	if err != nil || len(oldest) == 0 {
		t.Fatal(err)
	}
	newest, err := store.Query(ctx, timeline.QueryOptions{Limit: 1, Sources: informer, IncludeManaged: true})
	if err != nil || len(newest) == 0 {
		t.Fatal(err)
	}
	previous := k8s.SetTestContextName(oldest[0].ClusterContext)
	t.Cleanup(func() { k8s.SetTestContextName(previous) })
	variants := map[string]func(q Query) timeline.QueryOptions{}
	base := func(q Query) timeline.QueryOptions {
		return timeline.QueryOptions{Since: q.start, Until: q.end, Sources: informer, ClusterContext: k8s.ActiveClusterContext(), Limit: FetchBound + 1}
	}
	custom := func(q Query, desired bool, eventTypes []timeline.EventType) timeline.QueryOptions {
		o := base(q)
		o.RequireAPIVersion, o.ExcludeAPIGroups, o.ExcludeGroupKinds = true, k8score.BuiltInAPIGroups(), append(trackedCustomGroupKinds(), churnKinds...)
		o.DesiredStateUpdates, o.EventTypes = desired, eventTypes
		return o
	}
	variants["custom updates"] = func(q Query) timeline.QueryOptions { return custom(q, true, updateEventTypes) }
	variants["custom updates, no JSON filter"] = func(q Query) timeline.QueryOptions { return custom(q, false, updateEventTypes) }
	variants["custom lifecycle"] = func(q Query) timeline.QueryOptions { return custom(q, true, lifecycleEventTypes) }
	variants["nodes"] = func(q Query) timeline.QueryOptions {
		o := base(q)
		o.Kinds, o.APIGroups, o.IncludeManaged, o.DesiredStateUpdates = []string{"Node"}, []string{""}, true, true
		return o
	}
	variants["curated updates (baseline read)"] = func(q Query) timeline.QueryOptions {
		o := base(q)
		o.Kinds, o.EventTypes = specKinds, updateEventTypes
		return o
	}
	for name, build := range variants {
		var all []float64
		for at := oldest[0].Timestamp.Add(time.Hour); !at.After(newest[0].Timestamp); at = at.Add(10 * time.Minute) {
			opts := build(Query{start: at.Add(-time.Hour), end: at})
			best := time.Duration(1 << 62)
			for range 3 {
				began := time.Now()
				if _, err := store.Query(ctx, opts); err != nil {
					t.Fatal(err)
				}
				best = min(best, time.Since(began))
			}
			all = append(all, float64(best.Microseconds())/1000)
		}
		sort.Float64s(all)
		t.Logf("%-34s p50 %6.2f ms  p95 %6.2f ms  (%d windows)", name, all[len(all)/2], all[len(all)*95/100], len(all))
	}
}
