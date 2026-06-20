package timeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

func sqliteFileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Size()
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

func TestSQLiteStore_Query_Names(t *testing.T) {
	store, cleanup := createTestSQLiteStore(t)
	defer cleanup()

	ctx := context.Background()
	events := []TimelineEvent{
		{ID: "name-1", Timestamp: time.Now(), Kind: "Deployment", Namespace: "default", Name: "deploy-1", EventType: EventTypeAdd, Source: SourceInformer},
		{ID: "name-2", Timestamp: time.Now(), Kind: "Deployment", Namespace: "default", Name: "deploy-2", EventType: EventTypeAdd, Source: SourceInformer},
		{ID: "name-3", Timestamp: time.Now(), Kind: "Service", Namespace: "default", Name: "deploy-1", EventType: EventTypeAdd, Source: SourceInformer},
	}
	if err := store.AppendBatch(ctx, events); err != nil {
		t.Fatalf("AppendBatch failed: %v", err)
	}

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

// The lifecycle candidate query (meaningfulchanges) depends on this filter:
// a SQL-level regression here would silently re-import update churn or starve
// deletes for sqlite-backed timelines.
func TestSQLiteStore_Query_EventTypes(t *testing.T) {
	store, cleanup := createTestSQLiteStore(t)
	defer cleanup()

	ctx := context.Background()
	events := []TimelineEvent{
		{ID: "et-add", Timestamp: time.Now(), Kind: "Deployment", Namespace: "default", Name: "deploy-1", EventType: EventTypeAdd, Source: SourceInformer, Reason: ReasonRecreated},
		{ID: "et-update", Timestamp: time.Now(), Kind: "Deployment", Namespace: "default", Name: "deploy-1", EventType: EventTypeUpdate, Source: SourceInformer},
		{ID: "et-delete", Timestamp: time.Now(), Kind: "Deployment", Namespace: "default", Name: "deploy-2", EventType: EventTypeDelete, Source: SourceInformer},
	}
	if err := store.AppendBatch(ctx, events); err != nil {
		t.Fatalf("AppendBatch failed: %v", err)
	}

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
		// Reason must survive the SQLite round-trip — recreate-join coalescing
		// keys on it.
		if e.ID == "et-add" && e.Reason != ReasonRecreated {
			t.Errorf("Reason = %q, want %q after round-trip", e.Reason, ReasonRecreated)
		}
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

func TestSQLiteStore_Query_DeletedFiltering(t *testing.T) {
	store, cleanup := createTestSQLiteStore(t)
	defer cleanup()

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

func TestSQLiteStore_Query_ExcludeDeleted_LimitSkew(t *testing.T) {
	store, cleanup := createTestSQLiteStore(t)
	defer cleanup()

	ctx := context.Background()
	now := time.Now()

	// Two older non-delete events, then three newer deletes. With ExcludeDeleted
	// applied after a SQL LIMIT, the newest rows (all deletes) would consume the
	// page and the result would come back empty; filtering in SQL must still
	// surface the older non-delete events.
	events := []TimelineEvent{
		{ID: "svc-add", Timestamp: now, Kind: "Service", Namespace: "default", Name: "svc-1", EventType: EventTypeAdd, Source: SourceInformer},
		{ID: "deploy-add", Timestamp: now.Add(time.Second), Kind: "Deployment", Namespace: "default", Name: "deploy-1", EventType: EventTypeAdd, Source: SourceInformer},
		{ID: "del-1", Timestamp: now.Add(2 * time.Second), Kind: "Deployment", Namespace: "default", Name: "d-a", EventType: EventTypeDelete, Source: SourceInformer},
		{ID: "del-2", Timestamp: now.Add(3 * time.Second), Kind: "Deployment", Namespace: "default", Name: "d-b", EventType: EventTypeDelete, Source: SourceInformer},
		{ID: "del-3", Timestamp: now.Add(4 * time.Second), Kind: "Deployment", Namespace: "default", Name: "d-c", EventType: EventTypeDelete, Source: SourceInformer},
	}
	_ = store.AppendBatch(ctx, events)

	result, err := store.Query(ctx, QueryOptions{Limit: 2, ExcludeDeleted: true})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("Expected 2 non-delete events despite newer deletes, got %d: %+v", len(result), result)
	}
	for _, e := range result {
		if e.EventType == EventTypeDelete {
			t.Fatalf("ExcludeDeleted returned a delete event: %+v", e)
		}
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
	result, err := store.GetChangesForOwner(ctx, "Deployment", "default", "my-deploy", "", time.Time{}, 10)
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

func TestGetDiagnosis_WithSQLiteStore(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "timeline-diagnose-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	ResetStore()
	defer ResetStore()

	dbPath := filepath.Join(tmpDir, "diagnose.db")
	if err := InitStore(StoreConfig{Type: StoreTypeSQLite, Path: dbPath}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}

	event := TimelineEvent{
		ID:        "event-1",
		Timestamp: time.Now(),
		Source:    SourceInformer,
		Kind:      "TimelineWidget",
		Namespace: "radar-timeline-test",
		Name:      "noise-check",
		EventType: EventTypeUpdate,
	}
	if err := RecordEvent(context.Background(), event); err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}

	resp := GetDiagnosis("TimelineWidget", "radar-timeline-test", "noise-check")
	if !resp.StorePresent {
		t.Fatal("expected diagnostics to see the global store")
	}
	if len(resp.TimelineEvents) != 1 {
		t.Fatalf("expected one matching event, got %d: %+v", len(resp.TimelineEvents), resp.TimelineEvents)
	}
	if resp.TimelineEvents[0].ID != event.ID {
		t.Fatalf("diagnosis returned wrong event: got %q want %q", resp.TimelineEvents[0].ID, event.ID)
	}
}

func TestSQLiteStore_Stats_RecordsCleanupState(t *testing.T) {
	store, cleanup := createTestSQLiteStore(t)
	defer cleanup()

	if got := store.Stats(); !got.LastCleanupAt.IsZero() || got.RetentionAge != 0 {
		t.Errorf("expected zero cleanup state before StartCleanupLoop, got %+v", got)
	}

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

	store.StartCleanupLoop(time.Hour, time.Hour, 0)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stats := store.Stats()
		if !stats.LastCleanupAt.IsZero() {
			if stats.RetentionAge != time.Hour {
				t.Errorf("RetentionAge = %s, want 1h", stats.RetentionAge)
			}
			if stats.LastCleanupDeletedRows != 1 {
				t.Errorf("LastCleanupDeletedRows = %d, want 1", stats.LastCleanupDeletedRows)
			}
			if stats.LastCleanupError != "" {
				t.Errorf("LastCleanupError = %q, want empty", stats.LastCleanupError)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("LastCleanupAt remained zero — cleanup state not recorded")
}

func TestSQLiteStore_PruneToMaxSize_DropsOldestEvents(t *testing.T) {
	store, cleanup := createTestSQLiteStore(t)
	defer cleanup()

	ctx := context.Background()
	base := time.Now().Add(-time.Hour)
	events := make([]TimelineEvent, 0, 300)
	for i := range 300 {
		events = append(events, TimelineEvent{
			ID:        fmt.Sprintf("event-%03d", i),
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Source:    SourceInformer,
			Kind:      "TimelineWidget",
			Namespace: "default",
			Name:      "noise-check",
			EventType: EventTypeUpdate,
			Message:   strings.Repeat("x", 2048),
		})
	}
	if err := store.AppendBatch(ctx, events); err != nil {
		t.Fatalf("AppendBatch: %v", err)
	}

	if err := store.checkpointWAL(ctx); err != nil {
		t.Fatalf("checkpointWAL: %v", err)
	}
	before := store.storageBytes()
	deleted, err := store.PruneToMaxSize(ctx, before*9/10)
	if err != nil {
		t.Fatalf("PruneToMaxSize: %v", err)
	}
	if deleted == 0 {
		t.Fatal("expected size pruning to delete old events")
	}

	got, err := store.Query(ctx, QueryOptions{Limit: 1000, IncludeManaged: true})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) >= len(events) {
		t.Fatalf("expected fewer than %d events after pruning, got %d", len(events), len(got))
	}
	if len(got) == 0 || got[0].ID != "event-299" {
		t.Fatalf("expected newest event to remain after pruning, got %+v", got)
	}
}

func TestSQLiteStore_PruneToMaxSize_CanDeleteLastOversizedEvent(t *testing.T) {
	store, cleanup := createTestSQLiteStore(t)
	defer cleanup()

	ctx := context.Background()
	maxBytes := store.storageBytes() + 128*1024
	if err := store.Append(ctx, TimelineEvent{
		ID:        "oversized",
		Timestamp: time.Now(),
		Source:    SourceInformer,
		Kind:      "TimelineWidget",
		Namespace: "default",
		Name:      "noise-check",
		EventType: EventTypeUpdate,
		Message:   strings.Repeat("x", 2*1024*1024),
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if store.storageBytes() <= maxBytes {
		t.Fatalf("test setup did not exceed max size: storage=%d max=%d", store.storageBytes(), maxBytes)
	}

	deleted, err := store.PruneToMaxSize(ctx, maxBytes)
	if err != nil {
		t.Fatalf("PruneToMaxSize: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}
	got, err := store.Query(ctx, QueryOptions{Limit: 10, IncludeManaged: true})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected oversized event to be pruned, got %+v", got)
	}
	if storage := store.storageBytes(); storage > maxBytes {
		t.Fatalf("storage still above max after pruning: %d > %d", storage, maxBytes)
	}
}

func TestSQLiteStore_RunCleanup_CheckpointsWALForRetentionOnly(t *testing.T) {
	store, cleanup := createTestSQLiteStore(t)
	defer cleanup()

	ctx := context.Background()
	events := make([]TimelineEvent, 0, 300)
	for i := range 300 {
		events = append(events, TimelineEvent{
			ID:        fmt.Sprintf("event-%03d", i),
			Timestamp: time.Now(),
			Source:    SourceInformer,
			Kind:      "TimelineWidget",
			Namespace: "default",
			Name:      "noise-check",
			EventType: EventTypeUpdate,
			Message:   strings.Repeat("x", 2048),
		})
	}
	if err := store.AppendBatch(ctx, events); err != nil {
		t.Fatalf("AppendBatch: %v", err)
	}
	walBefore := sqliteFileSize(t, store.path+"-wal")
	if walBefore == 0 {
		t.Fatal("test setup did not create a WAL file")
	}

	store.runCleanup(time.Hour, 0)

	if walAfter := sqliteFileSize(t, store.path+"-wal"); walAfter != 0 {
		t.Fatalf("expected retention cleanup to truncate WAL, before=%d after=%d", walBefore, walAfter)
	}
}

func TestSQLiteStore_StartCleanupLoop_PrunesByMaxSizeWithoutRetention(t *testing.T) {
	store, cleanup := createTestSQLiteStore(t)
	defer cleanup()

	ctx := context.Background()
	base := time.Now().Add(-time.Hour)
	events := make([]TimelineEvent, 0, 300)
	for i := range 300 {
		events = append(events, TimelineEvent{
			ID:        fmt.Sprintf("event-%03d", i),
			Timestamp: base.Add(time.Duration(i) * time.Second),
			Source:    SourceInformer,
			Kind:      "TimelineWidget",
			Namespace: "default",
			Name:      "noise-check",
			EventType: EventTypeUpdate,
			Message:   strings.Repeat("x", 2048),
		})
	}
	if err := store.AppendBatch(ctx, events); err != nil {
		t.Fatalf("AppendBatch: %v", err)
	}

	before := store.storageBytes()
	store.StartCleanupLoop(0, time.Hour, before*9/10)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stats := store.Stats()
		if stats.LastCleanupDeletedRows > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatal("max-size-only cleanup did not prune events")
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

	store.StartCleanupLoop(time.Hour, time.Hour, 0)

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

	store.StartCleanupLoop(time.Hour, 20*time.Millisecond, 0)

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
		maxBytes  int64
	}{
		{"zero retention and max size", 0, time.Hour, 0},
		{"zero interval", time.Hour, 0, 1024},
		{"both cleanup modes disabled", 0, time.Hour, 0},
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

			store.StartCleanupLoop(tc.retention, tc.interval, tc.maxBytes)

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

// The persistent store outlives kubeconfig context switches, so a scoped
// query must return only the active cluster's events — and must exclude
// legacy rows recorded before provenance was tracked (cluster_context=”).
func TestSQLiteStore_Query_ClusterContextScoping(t *testing.T) {
	store, cleanup := createTestSQLiteStore(t)
	defer cleanup()
	ctx := context.Background()

	mk := func(id, cluster string) TimelineEvent {
		return TimelineEvent{
			ID: id, Timestamp: time.Now(), Source: SourceInformer,
			Kind: "Deployment", Namespace: "prod", Name: "web",
			EventType: EventTypeUpdate, ClusterContext: cluster,
		}
	}
	if err := store.AppendBatch(ctx, []TimelineEvent{
		mk("a1", "cluster-a"), mk("a2", "cluster-a"),
		mk("b1", "cluster-b"),
		mk("legacy", ""),
	}); err != nil {
		t.Fatalf("AppendBatch: %v", err)
	}

	scoped, err := store.Query(ctx, QueryOptions{ClusterContext: "cluster-a", IncludeManaged: true, IncludeK8sEvents: true})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(scoped) != 2 {
		t.Fatalf("cluster-a scoped query = %d events, want 2: %+v", len(scoped), scoped)
	}
	for _, e := range scoped {
		if e.ClusterContext != "cluster-a" {
			t.Errorf("leaked event %s from %q", e.ID, e.ClusterContext)
		}
	}

	all, err := store.Query(ctx, QueryOptions{IncludeManaged: true, IncludeK8sEvents: true})
	if err != nil {
		t.Fatalf("Query all: %v", err)
	}
	if len(all) != 4 {
		t.Errorf("unscoped query = %d events, want 4 (cross-cluster reads stay possible)", len(all))
	}

	// Owner-scoped reads share the same hazard: owner identity collides
	// across clusters, so the cluster filter must apply there too.
	withOwner := mk("o1", "cluster-a")
	withOwner.Owner = &OwnerInfo{Kind: "Deployment", Name: "web"}
	foreignOwner := mk("o2", "cluster-b")
	foreignOwner.Owner = &OwnerInfo{Kind: "Deployment", Name: "web"}
	if err := store.AppendBatch(ctx, []TimelineEvent{withOwner, foreignOwner}); err != nil {
		t.Fatalf("AppendBatch owners: %v", err)
	}
	owned, err := store.GetChangesForOwner(ctx, "Deployment", "prod", "web", "cluster-a", time.Time{}, 10)
	if err != nil {
		t.Fatalf("GetChangesForOwner: %v", err)
	}
	if len(owned) != 1 || owned[0].ClusterContext != "cluster-a" {
		t.Errorf("owner query must scope to cluster-a, got %+v", owned)
	}
}

// Opening a pre-cluster_context database must migrate it in place: the column
// is added, old rows read back with empty provenance, and scoped queries
// exclude them rather than guess their cluster.
func TestSQLiteStore_Migration_AddsClusterContext(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "timeline-migrate-*")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	defer os.RemoveAll(tmpDir)
	dbPath := filepath.Join(tmpDir, "old.db")

	// Build an old-schema DB by hand (no cluster_context, no api_version).
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := store.db.Exec(`DROP TABLE events`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, err := store.db.Exec(`CREATE TABLE events (
		id TEXT PRIMARY KEY, timestamp TEXT NOT NULL, source TEXT NOT NULL,
		kind TEXT NOT NULL, namespace TEXT, name TEXT NOT NULL, uid TEXT,
		event_type TEXT NOT NULL, reason TEXT, message TEXT, diff_json TEXT,
		health_state TEXT, owner_kind TEXT, owner_name TEXT, labels_json TEXT,
		count INTEGER DEFAULT 0, correlation_id TEXT,
		created_at TEXT DEFAULT (datetime('now')))`); err != nil {
		t.Fatalf("recreate old schema: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO events (id, timestamp, source, kind, namespace, name, event_type, health_state)
		VALUES ('old1', ?, 'informer', 'Deployment', 'prod', 'web', 'update', '')`,
		time.Now().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("seed old row: %v", err)
	}
	store.Close()

	migrated, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("reopen (migration): %v", err)
	}
	defer migrated.Close()
	ctx := context.Background()

	all, err := migrated.Query(ctx, QueryOptions{IncludeManaged: true, IncludeK8sEvents: true})
	if err != nil {
		t.Fatalf("query migrated: %v", err)
	}
	if len(all) != 1 || all[0].ClusterContext != "" {
		t.Fatalf("legacy row must survive with empty provenance, got %+v", all)
	}
	scoped, err := migrated.Query(ctx, QueryOptions{ClusterContext: "cluster-a", IncludeManaged: true, IncludeK8sEvents: true})
	if err != nil {
		t.Fatalf("scoped query: %v", err)
	}
	if len(scoped) != 0 {
		t.Errorf("scoped query must exclude unknowable-provenance legacy rows, got %d", len(scoped))
	}
}
