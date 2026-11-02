package mcp

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/timeline"
)

// Sixty ConfigMap edits, all newer than a custom resource's edit and ranked
// the same: the unfiltered feed still lists the edit with its field, and
// names the ConfigMaps it left out. The per-kind cap runs over every
// candidate; a 50-row cut before it would drop the edit first.
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
	if len(response.Changes) != 20 {
		t.Fatalf("got %d rows, want 20", len(response.Changes))
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
	if !strings.Contains(response.NarrowHint, "41 ConfigMap changes") || !strings.Contains(response.NarrowHint, "kind=ConfigMap") || strings.Contains(response.NarrowHint, "highest-ranked") {
		t.Fatalf("narrowHint = %q, want the ConfigMaps left out named", response.NarrowHint)
	}

	// A kind filter lists that kind's history, uncapped by kind.
	response, _ = getChangesForTest(t, context.Background(), getChangesInput{Kind: "ConfigMap", Limit: 50})
	if len(response.Changes) != 50 {
		t.Fatalf("kind=ConfigMap got %d rows, want 50", len(response.Changes))
	}
}
