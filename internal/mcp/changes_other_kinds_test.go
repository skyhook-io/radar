package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/timeline"
)

// Sixty ConfigMap edits, all newer than a custom resource's edit and ranked
// the same: the unfiltered feed keeps the newest 24 of them, as many as a page
// without the custom resource would at the old default, and still lists the
// edit with its field.
func TestGetChangesListsCustomResourceBesideBusyKind(t *testing.T) {
	store := initCorrelationStore(t)
	now := time.Now()
	appendInformerEvent(t, store, timeline.TimelineEvent{
		ID: "dr", Timestamp: now.Add(-50 * time.Minute), APIVersion: "networking.istio.io/v1", Kind: "DestinationRule",
		Namespace: "shop", Name: "reviews", EventType: timeline.EventTypeUpdate,
		Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{
			{Path: "spec.trafficPolicy.loadBalancer.simple", OldValue: "ROUND_ROBIN", NewValue: "RANDOM"},
			{Path: "metadata.generation", OldValue: int64(1), NewValue: int64(2)},
		}},
	})
	for i := range 60 {
		appendInformerEvent(t, store, timeline.TimelineEvent{
			ID: fmt.Sprintf("cm-%d", i), Timestamp: now.Add(-time.Duration(40*60-i*30) * time.Second), APIVersion: "v1", Kind: "ConfigMap",
			Namespace: "shop", Name: fmt.Sprintf("cfg-%d", i), EventType: timeline.EventTypeUpdate,
			Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "data.key", OldValue: "a", NewValue: "b"}}},
		})
	}

	response, _ := getChangesForTest(t, context.Background(), getChangesInput{})
	if len(response.Changes) != 25 {
		t.Fatalf("got %d rows, want 25", len(response.Changes))
	}
	found := false
	for _, c := range response.Changes {
		if c.Kind == "DestinationRule" {
			found = len(c.Fields) > 0 && c.Fields[0].Path == "spec.trafficPolicy.loadBalancer.simple"
		}
	}
	if !found {
		t.Fatalf("DestinationRule edit missing or without its field: %+v", response.Changes)
	}
	if !strings.Contains(response.NarrowHint, "highest-ranked") {
		t.Fatalf("narrowHint = %q, want the omitted ConfigMaps mentioned", response.NarrowHint)
	}

	// A kind filter lists that kind's history, uncapped by kind.
	response, _ = getChangesForTest(t, context.Background(), getChangesInput{Kind: "ConfigMap", Limit: 50})
	if len(response.Changes) != 50 {
		t.Fatalf("kind=ConfigMap got %d rows, want 50", len(response.Changes))
	}
}

// A failed read of the other kinds costs only those rows, and the answer says
// so instead of claiming it read everything.
func TestGetChangesSurvivesOtherKindsReadFailure(t *testing.T) {
	store := initCorrelationStore(t)
	now := time.Now()
	for i := range 30 {
		appendInformerEvent(t, store, timeline.TimelineEvent{
			ID: fmt.Sprintf("cm-%d", i), Timestamp: now.Add(-time.Duration(i+1) * time.Minute), APIVersion: "v1", Kind: "ConfigMap",
			Namespace: "shop", Name: fmt.Sprintf("cfg-%d", i), EventType: timeline.EventTypeUpdate,
			Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "data.key", OldValue: "a", NewValue: "b"}}},
		})
	}
	timeline.SetStoreForTest(failingOtherKindsStore{EventStore: store})

	response, _ := getChangesForTest(t, context.Background(), getChangesInput{})
	if len(response.Changes) != 25 {
		t.Fatalf("got %d rows, want 25", len(response.Changes))
	}
	if len(response.SourcesErrored) != 1 || !strings.Contains(response.SourcesErrored[0], "custom resources and Nodes") {
		t.Fatalf("sourcesErrored = %v", response.SourcesErrored)
	}
	if !strings.Contains(response.NarrowHint, "apart from custom resources and Nodes") {
		t.Fatalf("narrowHint = %q", response.NarrowHint)
	}
}

// A call that sets a limit keeps its curated rows whatever the other kinds
// hold; the hint names what it left out.
func TestGetChangesExplicitLimitKeepsCuratedRows(t *testing.T) {
	store := initCorrelationStore(t)
	now := time.Now()
	appendInformerEvent(t, store, timeline.TimelineEvent{
		ID: "dr", Timestamp: now.Add(-time.Minute), APIVersion: "networking.istio.io/v1", Kind: "DestinationRule",
		Namespace: "shop", Name: "reviews", EventType: timeline.EventTypeUpdate,
		Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "spec.host", OldValue: "a", NewValue: "b"}}},
	})
	for i := range 30 {
		appendInformerEvent(t, store, timeline.TimelineEvent{
			ID: fmt.Sprintf("cm-%d", i), Timestamp: now.Add(-time.Duration(i+2) * time.Minute), APIVersion: "v1", Kind: "ConfigMap",
			Namespace: "shop", Name: fmt.Sprintf("cfg-%d", i), EventType: timeline.EventTypeUpdate,
			Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "data.key", OldValue: "a", NewValue: "b"}}},
		})
	}

	response, _ := getChangesForTest(t, context.Background(), getChangesInput{Limit: 20})
	if len(response.Changes) != 20 {
		t.Fatalf("got %d rows, want 20", len(response.Changes))
	}
	for _, c := range response.Changes {
		if c.Kind != "ConfigMap" {
			t.Fatalf("explicit limit listed %s/%s ahead of a curated row", c.Kind, c.Name)
		}
	}
	if !strings.Contains(response.NarrowHint, "1 DestinationRule change") || !strings.Contains(response.NarrowHint, "kind=DestinationRule") {
		t.Fatalf("narrowHint = %q", response.NarrowHint)
	}

	// With no limit the same window reserves the edit a slot, and says so.
	response, _ = getChangesForTest(t, context.Background(), getChangesInput{})
	if len(response.Changes) != 25 || !strings.Contains(response.NarrowHint, "the highest-ranked and some custom resources and Nodes") {
		t.Fatalf("%d rows, narrowHint = %q", len(response.Changes), response.NarrowHint)
	}
}

// A slow read of the other kinds runs out its own budget and costs only its
// own rows.
func TestGetChangesBoundsOtherKindsRead(t *testing.T) {
	store := initCorrelationStore(t)
	appendInformerEvent(t, store, timeline.TimelineEvent{
		ID: "cm", Timestamp: time.Now().Add(-time.Minute), APIVersion: "v1", Kind: "ConfigMap",
		Namespace: "shop", Name: "cfg", EventType: timeline.EventTypeUpdate,
		Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "data.key", OldValue: "a", NewValue: "b"}}},
	})
	timeline.SetStoreForTest(blockingOtherKindsStore{EventStore: store})
	budget := otherKindsBudget
	otherKindsBudget = 20 * time.Millisecond
	t.Cleanup(func() { otherKindsBudget = budget })

	response, _ := getChangesForTest(t, context.Background(), getChangesInput{})
	if len(response.Changes) != 1 || response.Changes[0].Name != "cfg" {
		t.Fatalf("changes = %+v, want the curated row", response.Changes)
	}
	if len(response.SourcesErrored) != 1 || !strings.Contains(response.SourcesErrored[0], "deadline") {
		t.Fatalf("sourcesErrored = %v", response.SourcesErrored)
	}
}

// failingOtherKindsStore fails only the desired-state reads the other kinds use.
type failingOtherKindsStore struct{ timeline.EventStore }

func (s failingOtherKindsStore) Query(ctx context.Context, opts timeline.QueryOptions) ([]timeline.TimelineEvent, error) {
	if opts.DesiredStateUpdates {
		return nil, errors.New("read failed")
	}
	return s.EventStore.Query(ctx, opts)
}

// blockingOtherKindsStore holds the other kinds' reads until their context ends.
type blockingOtherKindsStore struct{ timeline.EventStore }

func (s blockingOtherKindsStore) Query(ctx context.Context, opts timeline.QueryOptions) ([]timeline.TimelineEvent, error) {
	if opts.DesiredStateUpdates {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return s.EventStore.Query(ctx, opts)
}
