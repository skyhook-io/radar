package timeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func createTestSQLiteStore(t *testing.T) (*SQLiteStore, func()) {
	t.Helper()

	// Create temp directory for test database
	tmpDir, err := os.MkdirTemp("", "timeline-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	dbPath := filepath.Join(tmpDir, "test.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to create SQLite store: %v", err)
	}

	cleanup := func() {
		store.Close()
		os.RemoveAll(tmpDir)
	}

	return store, cleanup
}

func TestSQLiteStore_Append(t *testing.T) {
	store, cleanup := createTestSQLiteStore(t)
	defer cleanup()

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

func TestSQLiteStore_AppendBatch(t *testing.T) {
	store, cleanup := createTestSQLiteStore(t)
	defer cleanup()

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

func TestSQLiteStore_Query_FilterPreset(t *testing.T) {
	store, cleanup := createTestSQLiteStore(t)
	defer cleanup()

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

func TestSQLiteStore_Query_IncludeManaged(t *testing.T) {
	store, cleanup := createTestSQLiteStore(t)
	defer cleanup()

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

func TestSQLiteStore_GroupByOwner(t *testing.T) {
	store, cleanup := createTestSQLiteStore(t)
	defer cleanup()

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

func TestSQLiteStore_Persistence(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "timeline-persist-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "persist.db")
	ctx := context.Background()

	// Create store and add event
	store1, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}

	event := TimelineEvent{
		ID:        "persist-1",
		Timestamp: time.Now(),
		Source:    SourceInformer,
		Kind:      "Deployment",
		Namespace: "default",
		Name:      "persistent-deploy",
		EventType: EventTypeAdd,
	}
	_ = store1.Append(ctx, event)
	store1.Close()

	// Reopen store and verify event persisted
	store2, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("Failed to reopen store: %v", err)
	}
	defer store2.Close()

	result, err := store2.Query(ctx, QueryOptions{Limit: 10, IncludeManaged: true})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if len(result) != 1 {
		t.Errorf("Expected 1 persisted event, got %d", len(result))
	}
	if result[0].ID != "persist-1" {
		t.Errorf("Expected event ID 'persist-1', got '%s'", result[0].ID)
	}
}

func TestSQLiteStore_ResourceSeen(t *testing.T) {
	store, cleanup := createTestSQLiteStore(t)
	defer cleanup()

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

func TestSQLiteStore_Stats(t *testing.T) {
	store, cleanup := createTestSQLiteStore(t)
	defer cleanup()

	ctx := context.Background()

	// Add some events
	events := []TimelineEvent{
		{ID: "stats-1", Timestamp: time.Now().Add(-1 * time.Hour), Kind: "Deployment", Namespace: "default", Name: "deploy-1", EventType: EventTypeAdd, Source: SourceInformer},
		{ID: "stats-2", Timestamp: time.Now(), Kind: "Pod", Namespace: "default", Name: "pod-1", EventType: EventTypeAdd, Source: SourceInformer},
	}
	_ = store.AppendBatch(ctx, events)

	stats := store.Stats()
	if stats.TotalEvents != 2 {
		t.Errorf("Expected TotalEvents=2, got %d", stats.TotalEvents)
	}
	if stats.OldestEvent.IsZero() {
		t.Error("Expected OldestEvent to be set")
	}
	if stats.NewestEvent.IsZero() {
		t.Error("Expected NewestEvent to be set")
	}
	if !stats.OldestEvent.Before(stats.NewestEvent) {
		t.Error("OldestEvent should be before NewestEvent")
	}
}

func TestSQLiteStore_GetEvent(t *testing.T) {
	store, cleanup := createTestSQLiteStore(t)
	defer cleanup()

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

func TestSQLiteStore_GetChangesForOwner(t *testing.T) {
	store, cleanup := createTestSQLiteStore(t)
	defer cleanup()

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

func TestSQLiteStore_DiffStorage(t *testing.T) {
	store, cleanup := createTestSQLiteStore(t)
	defer cleanup()

	ctx := context.Background()

	event := TimelineEvent{
		ID:        "diff-test-1",
		Timestamp: time.Now(),
		Source:    SourceInformer,
		Kind:      "Deployment",
		Namespace: "default",
		Name:      "test-deploy",
		EventType: EventTypeUpdate,
		Diff: &DiffInfo{
			Summary: "replicas changed",
			Fields: []FieldChange{
				{Path: "spec.replicas", OldValue: 2, NewValue: 3},
			},
		},
	}
	_ = store.Append(ctx, event)

	// Retrieve and verify diff is preserved
	result, err := store.GetEvent(ctx, "diff-test-1")
	if err != nil {
		t.Fatalf("GetEvent failed: %v", err)
	}
	if result.Diff == nil {
		t.Fatal("Diff should not be nil")
	}
	if result.Diff.Summary != "replicas changed" {
		t.Errorf("Expected summary 'replicas changed', got '%s'", result.Diff.Summary)
	}
	if len(result.Diff.Fields) != 1 {
		t.Errorf("Expected 1 field change, got %d", len(result.Diff.Fields))
	}
}

func TestSQLiteStore_LabelsStorage(t *testing.T) {
	store, cleanup := createTestSQLiteStore(t)
	defer cleanup()

	ctx := context.Background()

	event := TimelineEvent{
		ID:        "labels-test-1",
		Timestamp: time.Now(),
		Source:    SourceInformer,
		Kind:      "Deployment",
		Namespace: "default",
		Name:      "test-deploy",
		EventType: EventTypeAdd,
		Labels: map[string]string{
			"app":                       "myapp",
			"app.kubernetes.io/name":    "myapp",
			"app.kubernetes.io/version": "v1",
		},
	}
	_ = store.Append(ctx, event)

	// Retrieve and verify labels are preserved
	result, err := store.GetEvent(ctx, "labels-test-1")
	if err != nil {
		t.Fatalf("GetEvent failed: %v", err)
	}
	if result.Labels == nil {
		t.Fatal("Labels should not be nil")
	}
	if result.Labels["app"] != "myapp" {
		t.Errorf("Expected label app='myapp', got '%s'", result.Labels["app"])
	}
	if result.GetAppLabel() != "myapp" {
		t.Errorf("Expected GetAppLabel()='myapp', got '%s'", result.GetAppLabel())
	}
}

func TestSQLiteStore_SeenResources_PersistAcrossRestart(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "timeline-seen-persist-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(tmpDir)
	dbPath := filepath.Join(tmpDir, "seen.db")

	store1, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	store1.MarkResourceSeen("Pod", "default", "p1")
	store1.MarkResourceSeen("Deployment", "kube-system", "d1")
	store1.Close()

	store2, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store2.Close()

	if !store2.IsResourceSeen("Pod", "default", "p1") {
		t.Error("expected Pod default/p1 to be seen after restart")
	}
	if !store2.IsResourceSeen("Deployment", "kube-system", "d1") {
		t.Error("expected Deployment kube-system/d1 to be seen after restart")
	}
	if store2.IsResourceSeen("Pod", "default", "never-marked") {
		t.Error("did not expect unmarked resource to be seen")
	}
}

func TestSQLiteStore_StartCleanupLoop_RunsImmediately(t *testing.T) {
	// Use an interval far longer than the test window so the only way
	// the old event can be deleted within the deadline is the eager
	// pre-ticker run. Catches a regression where someone moves the
	// runCleanup call back inside the for-loop / below the case branch.
	store, cleanup := createTestSQLiteStore(t)
	defer cleanup()

	ctx := context.Background()
	old := TimelineEvent{
		ID:        "old",
		Timestamp: time.Now().Add(-2 * time.Hour),
		Source:    SourceInformer,
		Kind:      "Pod",
		Namespace: "default",
		Name:      "old-pod",
		EventType: EventTypeAdd,
	}
	if err := store.Append(ctx, old); err != nil {
		t.Fatalf("Append: %v", err)
	}

	store.StartCleanupLoop(time.Hour, time.Hour)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		events, err := store.Query(ctx, QueryOptions{Limit: 10, IncludeManaged: true})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(events) == 0 {
			return // eager cleanup ran
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("eager cleanup did not run within 2s — old event still present")
}

func TestSQLiteStore_StartCleanupLoop_RunsAndStopsOnClose(t *testing.T) {
	store, cleanup := createTestSQLiteStore(t)
	defer cleanup()

	ctx := context.Background()

	old := TimelineEvent{
		ID:        "old",
		Timestamp: time.Now().Add(-2 * time.Hour),
		Source:    SourceInformer,
		Kind:      "Pod",
		Namespace: "default",
		Name:      "old-pod",
		EventType: EventTypeAdd,
	}
	fresh := TimelineEvent{
		ID:        "fresh",
		Timestamp: time.Now(),
		Source:    SourceInformer,
		Kind:      "Pod",
		Namespace: "default",
		Name:      "fresh-pod",
		EventType: EventTypeAdd,
	}
	if err := store.AppendBatch(ctx, []TimelineEvent{old, fresh}); err != nil {
		t.Fatalf("AppendBatch: %v", err)
	}

	store.StartCleanupLoop(time.Hour, 20*time.Millisecond)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		events, err := store.Query(ctx, QueryOptions{Limit: 10, IncludeManaged: true})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		if len(events) == 1 && events[0].ID == "fresh" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	events, _ := store.Query(ctx, QueryOptions{Limit: 10, IncludeManaged: true})
	if len(events) != 1 || events[0].ID != "fresh" {
		t.Fatalf("expected only the fresh event after cleanup, got %d: %+v", len(events), events)
	}

	// Close must return promptly — proves the cleanup goroutine exited.
	done := make(chan error, 1)
	go func() { done <- store.Close() }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return within 2s — cleanup goroutine leaked")
	}

	// And idempotent — the deferred cleanup() will Close again.
}

func TestSQLiteStore_StartCleanupLoop_ZeroIsNoop(t *testing.T) {
	cases := []struct {
		name      string
		retention time.Duration
		interval  time.Duration
	}{
		{"zero retention", 0, time.Hour},
		{"zero interval", time.Hour, 0},
		{"both zero", 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir, err := os.MkdirTemp("", "timeline-noop-*")
			if err != nil {
				t.Fatalf("MkdirTemp: %v", err)
			}
			defer os.RemoveAll(tmpDir)

			store, err := NewSQLiteStore(filepath.Join(tmpDir, "test.db"))
			if err != nil {
				t.Fatalf("NewSQLiteStore: %v", err)
			}

			store.StartCleanupLoop(tc.retention, tc.interval)

			done := make(chan error, 1)
			go func() { done <- store.Close() }()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("Close blocked — goroutine started despite zero param")
			}
		})
	}
}
