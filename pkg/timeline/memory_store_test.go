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
	result, err := store.Query(ctx, QueryOptions{Namespaces: []string{"prod"}, Limit: 10, IncludeManaged: true})
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

func TestMemoryStore_Query_Names(t *testing.T) {
	store := NewMemoryStore(100)
	ctx := context.Background()

	events := []TimelineEvent{
		{ID: "name-1", Timestamp: time.Now(), Kind: "Deployment", Namespace: "default", Name: "deploy-1", EventType: EventTypeAdd, Source: SourceInformer},
		{ID: "name-2", Timestamp: time.Now(), Kind: "Deployment", Namespace: "default", Name: "deploy-2", EventType: EventTypeAdd, Source: SourceInformer},
		{ID: "name-3", Timestamp: time.Now(), Kind: "Service", Namespace: "default", Name: "deploy-1", EventType: EventTypeAdd, Source: SourceInformer},
	}
	_ = store.AppendBatch(ctx, events)

	result, err := store.Query(ctx, QueryOptions{Names: []string{"deploy-1"}, Limit: 10, IncludeManaged: true})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("Expected 2 deploy-1 events, got %d", len(result))
	}
	for _, e := range result {
		if e.Name != "deploy-1" {
			t.Errorf("Expected name 'deploy-1', got %q", e.Name)
		}
	}
}

func TestMemoryStore_Query_EventTypes(t *testing.T) {
	store := NewMemoryStore(100)
	ctx := context.Background()

	events := []TimelineEvent{
		{ID: "et-add", Timestamp: time.Now(), Kind: "Deployment", Namespace: "default", Name: "deploy-1", EventType: EventTypeAdd, Source: SourceInformer, Reason: ReasonRecreated},
		{ID: "et-update", Timestamp: time.Now(), Kind: "Deployment", Namespace: "default", Name: "deploy-1", EventType: EventTypeUpdate, Source: SourceInformer},
		{ID: "et-delete", Timestamp: time.Now(), Kind: "Deployment", Namespace: "default", Name: "deploy-2", EventType: EventTypeDelete, Source: SourceInformer},
	}
	_ = store.AppendBatch(ctx, events)

	result, err := store.Query(ctx, QueryOptions{EventTypes: []EventType{EventTypeAdd, EventTypeDelete}, Limit: 10, IncludeManaged: true})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("Expected 2 add/delete events, got %d", len(result))
	}
	for _, e := range result {
		if e.EventType == EventTypeUpdate {
			t.Errorf("update event %q leaked through the EventTypes filter", e.ID)
		}
		if e.ID == "et-add" && e.Reason != ReasonRecreated {
			t.Errorf("Reason = %q, want %q", e.Reason, ReasonRecreated)
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
	for i := range 10 {
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
	for i := range 10 {
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
	result, err := store.GetChangesForOwner(ctx, "Deployment", "default", "my-deploy", "", time.Time{}, 10)
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

func TestMemoryStore_DeletedFiltering(t *testing.T) {
	store := NewMemoryStore(100)
	ctx := context.Background()
	now := time.Now()

	events := []TimelineEvent{
		{ID: "deploy-add", Timestamp: now, Kind: "Deployment", Namespace: "default", Name: "deploy-1", EventType: EventTypeAdd, Source: SourceInformer},
		{ID: "deploy-delete", Timestamp: now.Add(time.Second), Kind: "Deployment", Namespace: "default", Name: "deploy-2", EventType: EventTypeDelete, Source: SourceInformer},
		{
			ID: "pod-delete", Timestamp: now.Add(2 * time.Second), Kind: "Pod", Namespace: "default", Name: "pod-1",
			EventType: EventTypeDelete, Source: SourceInformer,
			Owner: &OwnerInfo{Kind: "ReplicaSet", Name: "deploy-1-abc"},
		},
	}
	_ = store.AppendBatch(ctx, events)

	// Default: top-level deletes show, managed (Pod) deletes do not — they follow IncludeManaged.
	result, err := store.Query(ctx, QueryOptions{Limit: 10})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("Expected Deployment add + Deployment delete, got %d: %+v", len(result), result)
	}
	if result[0].ID != "deploy-delete" || result[1].ID != "deploy-add" {
		t.Fatalf("unexpected result order: %+v", result)
	}

	// ExcludeDeleted drops the top-level delete too.
	result, err = store.Query(ctx, QueryOptions{Limit: 10, ExcludeDeleted: true})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if len(result) != 1 || result[0].ID != "deploy-add" {
		t.Fatalf("Expected only Deployment add with ExcludeDeleted, got %+v", result)
	}

	// IncludeManaged surfaces the managed Pod delete alongside the rest.
	result, err = store.Query(ctx, QueryOptions{Limit: 10, IncludeManaged: true})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("Expected all 3 events with IncludeManaged, got %d: %+v", len(result), result)
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
