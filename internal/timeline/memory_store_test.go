package timeline

import (
	"context"
	"testing"
	"time"
)

func TestMemoryStore_Append(t *testing.T) {
	store := NewMemoryStore(100)
	ctx := context.Background()

	event := TimelineEvent{
		ID:        "test-1",
		Timestamp: time.Now(),
		Source:    SourceInformer,
		Kind:      "Pod",
		Namespace: "default",
		Name:      "test-pod",
		EventType: EventTypeAdd,
	}

	err := store.Append(ctx, event)
	if err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	// Verify event was stored
	events, err := store.Query(ctx, QueryOptions{Limit: 10, IncludeManaged: true})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("Expected 1 event, got %d", len(events))
	}
	if events[0].ID != "test-1" {
		t.Errorf("Expected event ID 'test-1', got '%s'", events[0].ID)
	}
}

func TestMemoryStore_AppendBatch(t *testing.T) {
	store := NewMemoryStore(100)
	ctx := context.Background()

	events := []TimelineEvent{
		{ID: "batch-1", Timestamp: time.Now(), Kind: "Pod", Namespace: "default", Name: "pod-1", EventType: EventTypeAdd, Source: SourceInformer},
		{ID: "batch-2", Timestamp: time.Now(), Kind: "Pod", Namespace: "default", Name: "pod-2", EventType: EventTypeAdd, Source: SourceInformer},
		{ID: "batch-3", Timestamp: time.Now(), Kind: "Pod", Namespace: "default", Name: "pod-3", EventType: EventTypeAdd, Source: SourceInformer},
	}

	err := store.AppendBatch(ctx, events)
	if err != nil {
		t.Fatalf("AppendBatch failed: %v", err)
	}

	// Verify all events were stored
	result, err := store.Query(ctx, QueryOptions{Limit: 10, IncludeManaged: true})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if len(result) != 3 {
		t.Errorf("Expected 3 events, got %d", len(result))
	}
}

func TestMemoryStore_Query_Namespace(t *testing.T) {
	store := NewMemoryStore(100)
	ctx := context.Background()

	events := []TimelineEvent{
		{ID: "ns-1", Timestamp: time.Now(), Kind: "Deployment", Namespace: "prod", Name: "deploy-1", EventType: EventTypeAdd, Source: SourceInformer},
		{ID: "ns-2", Timestamp: time.Now(), Kind: "Deployment", Namespace: "staging", Name: "deploy-2", EventType: EventTypeAdd, Source: SourceInformer},
		{ID: "ns-3", Timestamp: time.Now(), Kind: "Deployment", Namespace: "prod", Name: "deploy-3", EventType: EventTypeAdd, Source: SourceInformer},
	}
	_ = store.AppendBatch(ctx, events)

	// Query for prod namespace only
	result, err := store.Query(ctx, QueryOptions{Namespace: "prod", Limit: 10, IncludeManaged: true})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("Expected 2 events for prod namespace, got %d", len(result))
	}
	for _, e := range result {
		if e.Namespace != "prod" {
			t.Errorf("Expected namespace 'prod', got '%s'", e.Namespace)
		}
	}
}

func TestMemoryStore_Query_Kinds(t *testing.T) {
	store := NewMemoryStore(100)
	ctx := context.Background()

	events := []TimelineEvent{
		{ID: "kind-1", Timestamp: time.Now(), Kind: "Deployment", Namespace: "default", Name: "deploy-1", EventType: EventTypeAdd, Source: SourceInformer},
		{ID: "kind-2", Timestamp: time.Now(), Kind: "Service", Namespace: "default", Name: "svc-1", EventType: EventTypeAdd, Source: SourceInformer},
		{ID: "kind-3", Timestamp: time.Now(), Kind: "Deployment", Namespace: "default", Name: "deploy-2", EventType: EventTypeAdd, Source: SourceInformer},
	}
	_ = store.AppendBatch(ctx, events)

	// Query for Deployment kind only
	result, err := store.Query(ctx, QueryOptions{Kinds: []string{"Deployment"}, Limit: 10, IncludeManaged: true})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("Expected 2 Deployment events, got %d", len(result))
	}
	for _, e := range result {
		if e.Kind != "Deployment" {
			t.Errorf("Expected kind 'Deployment', got '%s'", e.Kind)
		}
	}
}

func TestMemoryStore_Query_Since(t *testing.T) {
	store := NewMemoryStore(100)
	ctx := context.Background()

	now := time.Now()
	events := []TimelineEvent{
		{ID: "since-1", Timestamp: now.Add(-2 * time.Hour), Kind: "Deployment", Namespace: "default", Name: "deploy-1", EventType: EventTypeAdd, Source: SourceInformer},
		{ID: "since-2", Timestamp: now.Add(-30 * time.Minute), Kind: "Deployment", Namespace: "default", Name: "deploy-2", EventType: EventTypeAdd, Source: SourceInformer},
		{ID: "since-3", Timestamp: now, Kind: "Deployment", Namespace: "default", Name: "deploy-3", EventType: EventTypeAdd, Source: SourceInformer},
	}
	_ = store.AppendBatch(ctx, events)

	// Query for events in the last hour
	result, err := store.Query(ctx, QueryOptions{Since: now.Add(-1 * time.Hour), Limit: 10, IncludeManaged: true})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("Expected 2 events in last hour, got %d", len(result))
	}
}

func TestMemoryStore_Query_Limit(t *testing.T) {
	store := NewMemoryStore(100)
	ctx := context.Background()

	// Add 10 events
	events := make([]TimelineEvent, 10)
	for i := 0; i < 10; i++ {
		events[i] = TimelineEvent{
			ID:        "limit-" + string(rune('0'+i)),
			Timestamp: time.Now(),
			Kind:      "Deployment",
			Namespace: "default",
			Name:      "deploy-" + string(rune('0'+i)),
			EventType: EventTypeAdd,
			Source:    SourceInformer,
		}
	}
	_ = store.AppendBatch(ctx, events)

	// Query with limit of 5
	result, err := store.Query(ctx, QueryOptions{Limit: 5, IncludeManaged: true})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if len(result) != 5 {
		t.Errorf("Expected 5 events with limit, got %d", len(result))
	}
}

func TestMemoryStore_ResourceSeen(t *testing.T) {
	store := NewMemoryStore(100)

	// Initially not seen
	if store.IsResourceSeen("Pod", "default", "test-pod") {
		t.Error("Resource should not be seen initially")
	}

	// Mark as seen
	store.MarkResourceSeen("Pod", "default", "test-pod")

	// Now should be seen
	if !store.IsResourceSeen("Pod", "default", "test-pod") {
		t.Error("Resource should be seen after marking")
	}

	// Clear seen
	store.ClearResourceSeen("Pod", "default", "test-pod")

	// Should not be seen again
	if store.IsResourceSeen("Pod", "default", "test-pod") {
		t.Error("Resource should not be seen after clearing")
	}
}

func TestMemoryStore_RingBufferOverflow(t *testing.T) {
	// Create a small store that will overflow
	store := NewMemoryStore(5)
	ctx := context.Background()

	// Add 10 events (more than buffer size)
	for i := 0; i < 10; i++ {
		event := TimelineEvent{
			ID:        "overflow-" + string(rune('0'+i)),
			Timestamp: time.Now().Add(time.Duration(i) * time.Second),
			Kind:      "Deployment",
			Namespace: "default",
			Name:      "deploy-" + string(rune('0'+i)),
			EventType: EventTypeAdd,
			Source:    SourceInformer,
		}
		_ = store.Append(ctx, event)
	}

	// Should only have 5 events (the most recent ones)
	result, err := store.Query(ctx, QueryOptions{Limit: 10, IncludeManaged: true})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if len(result) != 5 {
		t.Errorf("Expected 5 events after overflow, got %d", len(result))
	}

	// Verify stats
	stats := store.Stats()
	if stats.TotalEvents != 5 {
		t.Errorf("Expected TotalEvents=5, got %d", stats.TotalEvents)
	}
}

func TestMemoryStore_GetEvent(t *testing.T) {
	store := NewMemoryStore(100)
	ctx := context.Background()

	event := TimelineEvent{
		ID:        "get-test-1",
		Timestamp: time.Now(),
		Source:    SourceInformer,
		Kind:      "Pod",
		Namespace: "default",
		Name:      "test-pod",
		EventType: EventTypeAdd,
	}
	_ = store.Append(ctx, event)

	// Get the event by ID
	result, err := store.GetEvent(ctx, "get-test-1")
	if err != nil {
		t.Fatalf("GetEvent failed: %v", err)
	}
	if result == nil {
		t.Fatal("GetEvent returned nil")
	}
	if result.ID != "get-test-1" {
		t.Errorf("Expected ID 'get-test-1', got '%s'", result.ID)
	}

	// Try to get non-existent event
	result, err = store.GetEvent(ctx, "non-existent")
	if err != nil {
		t.Fatalf("GetEvent failed: %v", err)
	}
	if result != nil {
		t.Error("Expected nil for non-existent event")
	}
}

func TestMemoryStore_GetChangesForOwner(t *testing.T) {
	store := NewMemoryStore(100)
	ctx := context.Background()

	events := []TimelineEvent{
		{
			ID: "owner-1", Timestamp: time.Now(), Kind: "Pod", Namespace: "default", Name: "pod-1",
			EventType: EventTypeAdd, Source: SourceInformer,
			Owner: &OwnerInfo{Kind: "Deployment", Name: "my-deploy"},
		},
		{
			ID: "owner-2", Timestamp: time.Now(), Kind: "Pod", Namespace: "default", Name: "pod-2",
			EventType: EventTypeAdd, Source: SourceInformer,
			Owner: &OwnerInfo{Kind: "Deployment", Name: "other-deploy"},
		},
		{
			ID: "owner-3", Timestamp: time.Now(), Kind: "Pod", Namespace: "default", Name: "pod-3",
			EventType: EventTypeAdd, Source: SourceInformer,
			Owner: &OwnerInfo{Kind: "Deployment", Name: "my-deploy"},
		},
	}
	_ = store.AppendBatch(ctx, events)

	// Query for pods owned by my-deploy
	result, err := store.GetChangesForOwner(ctx, "Deployment", "default", "my-deploy", time.Time{}, 10)
	if err != nil {
		t.Fatalf("GetChangesForOwner failed: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("Expected 2 events for owner my-deploy, got %d", len(result))
	}
}

func TestMemoryStore_QueryGrouped_ByOwner(t *testing.T) {
	store := NewMemoryStore(100)
	ctx := context.Background()

	events := []TimelineEvent{
		{ID: "group-1", Timestamp: time.Now(), Kind: "Deployment", Namespace: "default", Name: "my-deploy", EventType: EventTypeAdd, Source: SourceInformer},
		{
			ID: "group-2", Timestamp: time.Now(), Kind: "Pod", Namespace: "default", Name: "pod-1",
			EventType: EventTypeAdd, Source: SourceInformer,
			Owner: &OwnerInfo{Kind: "Deployment", Name: "my-deploy"},
		},
		{
			ID: "group-3", Timestamp: time.Now(), Kind: "Pod", Namespace: "default", Name: "pod-2",
			EventType: EventTypeAdd, Source: SourceInformer,
			Owner: &OwnerInfo{Kind: "Deployment", Name: "my-deploy"},
		},
	}
	_ = store.AppendBatch(ctx, events)

	// Query grouped by owner
	result, err := store.QueryGrouped(ctx, QueryOptions{
		GroupBy:        GroupByOwner,
		Limit:          10,
		IncludeManaged: true,
	})
	if err != nil {
		t.Fatalf("QueryGrouped failed: %v", err)
	}
	if len(result.Groups) != 1 {
		t.Errorf("Expected 1 group, got %d", len(result.Groups))
	}
	if result.Groups[0].Name != "my-deploy" {
		t.Errorf("Expected group name 'my-deploy', got '%s'", result.Groups[0].Name)
	}
}

func TestMemoryStore_IncludeManaged(t *testing.T) {
	store := NewMemoryStore(100)
	ctx := context.Background()

	events := []TimelineEvent{
		{ID: "managed-1", Timestamp: time.Now(), Kind: "Deployment", Namespace: "default", Name: "deploy-1", EventType: EventTypeAdd, Source: SourceInformer},
		{
			ID: "managed-2", Timestamp: time.Now(), Kind: "Pod", Namespace: "default", Name: "pod-1",
			EventType: EventTypeAdd, Source: SourceInformer,
			Owner: &OwnerInfo{Kind: "Deployment", Name: "deploy-1"},
		},
	}
	_ = store.AppendBatch(ctx, events)

	// Without IncludeManaged - should only get Deployment
	result, err := store.Query(ctx, QueryOptions{Limit: 10, IncludeManaged: false})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if len(result) != 1 {
		t.Errorf("Expected 1 event without IncludeManaged, got %d", len(result))
	}
	if result[0].Kind != "Deployment" {
		t.Errorf("Expected Deployment, got %s", result[0].Kind)
	}

	// With IncludeManaged - should get both
	result, err = store.Query(ctx, QueryOptions{Limit: 10, IncludeManaged: true})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if len(result) != 2 {
		t.Errorf("Expected 2 events with IncludeManaged, got %d", len(result))
	}
}

func TestMemoryStore_FilterPreset(t *testing.T) {
	store := NewMemoryStore(100)
	ctx := context.Background()

	events := []TimelineEvent{
		{ID: "preset-1", Timestamp: time.Now(), Kind: "Deployment", Namespace: "default", Name: "deploy-1", EventType: EventTypeAdd, Source: SourceInformer},
		{ID: "preset-2", Timestamp: time.Now(), Kind: "Lease", Namespace: "kube-system", Name: "lease-1", EventType: EventTypeUpdate, Source: SourceInformer},
		{ID: "preset-3", Timestamp: time.Now(), Kind: "Endpoints", Namespace: "default", Name: "svc-1", EventType: EventTypeUpdate, Source: SourceInformer},
	}
	_ = store.AppendBatch(ctx, events)

	// Query with default preset - should filter out Lease and Endpoints
	result, err := store.Query(ctx, QueryOptions{Limit: 10, FilterPreset: "default", IncludeManaged: true})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if len(result) != 1 {
		t.Errorf("Expected 1 event with default preset, got %d", len(result))
	}
	if result[0].Kind != "Deployment" {
		t.Errorf("Expected Deployment, got %s", result[0].Kind)
	}

	// Query with 'all' preset - should include everything
	result, err = store.Query(ctx, QueryOptions{Limit: 10, FilterPreset: "all", IncludeManaged: true})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if len(result) != 3 {
		t.Errorf("Expected 3 events with 'all' preset, got %d", len(result))
	}
}
