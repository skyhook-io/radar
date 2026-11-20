package meaningfulchanges

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
)

// Rows outside Query.Groups, from another group or with no recorded
// apiVersion, are dropped before the store's limit: however many there are,
// they can't push the caller's rows out or mark the read saturated.
func TestRecentGroupsBoundEveryReadBeforeTheLimit(t *testing.T) {
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 2000}); err != nil {
		t.Fatal(err)
	}
	store := timeline.GetStore()
	now := time.Now().UTC().Truncate(time.Second)
	add := func(id, apiVersion string, at time.Time) {
		t.Helper()
		if err := store.Append(context.Background(), timeline.TimelineEvent{
			ID: id, Timestamp: at, Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
			APIVersion: apiVersion, Kind: "Gadget", Name: "g", EventType: timeline.EventTypeAdd,
		}); err != nil {
			t.Fatal(err)
		}
	}
	add("readable", "new.example/v1", now.Add(-50*time.Minute))
	for i := 0; i < 4*NamedFetchBound+1; i++ {
		apiVersion := "old.example/v1"
		if i%2 == 0 {
			apiVersion = ""
		}
		add(fmt.Sprintf("other-%d", i), apiVersion, now.Add(-time.Duration(i)*time.Second))
	}

	q := Query{Kinds: []string{"Gadget"}, Name: "g", Namespaces: []string{""}, Since: time.Hour}
	unbounded, err := Recent(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if !unbounded.FetchSaturated {
		t.Fatal("fixture must saturate the read without Groups")
	}
	q.Groups = []string{"new.example"}
	got, err := Recent(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if got.FetchSaturated || len(got.Changes) != 1 || got.Changes[0].APIVersion != "new.example/v1" {
		t.Fatalf("saturated=%v changes=%+v, want only the readable row and no saturation", got.FetchSaturated, got.Changes)
	}
}

// A tracked kind's own group is intersected with Query.Groups; a read whose
// groups don't overlap them is skipped rather than left unfiltered.
func TestRecentGroupsIntersectTrackedGroups(t *testing.T) {
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 20}); err != nil {
		t.Fatal(err)
	}
	if err := timeline.GetStore().Append(context.Background(), timeline.TimelineEvent{
		ID: "gc", Timestamp: time.Now().Add(-time.Minute), Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
		APIVersion: "gateway.networking.k8s.io/v1", Kind: "GatewayClass", Name: "edge", EventType: timeline.EventTypeAdd,
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		groups []string
		want   int
	}{
		{[]string{"gateway.networking.k8s.io"}, 1},
		{[]string{"other.example"}, 0},
	} {
		got, err := Recent(context.Background(), Query{Kinds: []string{"GatewayClass"}, Namespaces: []string{""}, Groups: tc.groups, Since: time.Hour})
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Changes) != tc.want {
			t.Fatalf("groups %v: changes=%+v, want %d", tc.groups, got.Changes, tc.want)
		}
	}
}
