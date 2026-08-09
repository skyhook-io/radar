package server

import (
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
)

func TestCapacityNodeUsageSamplesPreservePerNodeTimestamps(t *testing.T) {
	oldest := time.Date(2026, time.July, 13, 8, 0, 0, 0, time.UTC)
	newest := oldest.Add(4 * time.Minute)
	metrics := []k8s.TopNodeMetrics{
		{Name: "node-new", CPU: 2_000_000_000, Memory: 4 << 30, ObservedAt: newest},
		{Name: "node-old", CPU: 1_000_000_000, Memory: 2 << 30, ObservedAt: oldest},
	}

	got, aggregateAsOf := capacityNodeUsageSamples(metrics)
	if !got["node-new"].ObservedAt.Equal(newest) {
		t.Errorf("node-new observedAt = %s, want %s", got["node-new"].ObservedAt, newest)
	}
	if !got["node-old"].ObservedAt.Equal(oldest) {
		t.Errorf("node-old observedAt = %s, want %s", got["node-old"].ObservedAt, oldest)
	}
	if !aggregateAsOf.Equal(oldest) {
		t.Errorf("aggregate asOf = %s, want oldest contributing sample %s", aggregateAsOf, oldest)
	}
}
