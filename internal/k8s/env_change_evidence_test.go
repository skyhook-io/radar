package k8s

import (
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/envresolve"
)

func TestLatestPodEnvSourceChangesUsesLatestObservedKeyChange(t *testing.T) {
	older := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	newer := older.Add(time.Minute)
	events := []timeline.TimelineEvent{
		{
			Kind: "ConfigMap", Namespace: "shop", Name: "app", Timestamp: older,
			Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "data (modified keys)", NewValue: []string{"HOST"}}}},
		},
		{
			Kind: "ConfigMap", Namespace: "shop", Name: "app", Timestamp: newer,
			Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "data (modified keys)", NewValue: []string{"HOST"}}, {Path: "data (added keys)", NewValue: []string{"PORT"}}}},
		},
		{
			Kind: "ConfigMap", Namespace: "shop", Name: "app", Timestamp: newer.Add(time.Minute),
			Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "metadata.labels", NewValue: map[string]string{"team": "platform"}}}},
		},
		{
			Kind: "ConfigMap", Namespace: "shop", Name: "app", Timestamp: newer.Add(2 * time.Minute),
			Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "data.app.json.feature.enabled", NewValue: true}}},
		},
	}

	got := latestPodEnvSourceChanges(events, map[envresolve.SourceID]envresolve.SourceData{
		{Kind: "ConfigMap", Name: "app"}: {Kind: "ConfigMap", Name: "app", Keys: []string{"app.json"}},
	})
	host := got[podEnvSourceKey{kind: "ConfigMap", namespace: "shop", name: "app", key: "HOST"}]
	if !host.changedAt.Equal(newer) || host.kind != "modified" {
		t.Fatalf("HOST change = %+v, want modified at %v", host, newer)
	}
	port := got[podEnvSourceKey{kind: "ConfigMap", namespace: "shop", name: "app", key: "PORT"}]
	if !port.changedAt.Equal(newer) || port.kind != "added" {
		t.Fatalf("PORT change = %+v, want added at %v", port, newer)
	}
	structured := got[podEnvSourceKey{kind: "ConfigMap", namespace: "shop", name: "app", key: "app.json"}]
	if structured.kind != "modified" || !structured.changedAt.Equal(newer.Add(2*time.Minute)) {
		t.Fatalf("structured change = %+v", structured)
	}
	if len(got) != 3 {
		t.Fatalf("unexpected changes: %+v", got)
	}
}
