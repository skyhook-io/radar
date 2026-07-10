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
	if store.IsResourceSeen("cluster-a", "Pod", "default", "test-pod") {
		t.Error("Resource should not be seen initially")
	}

	// Mark as seen
	store.MarkResourceSeen("cluster-a", "Pod", "default", "test-pod")

	// Now should be seen
	if !store.IsResourceSeen("cluster-a", "Pod", "default", "test-pod") {
		t.Error("Resource should be seen after marking")
	}

	// Clear seen
	store.ClearResourceSeen("cluster-a", "Pod", "default", "test-pod")

	// Should not be seen again
	if store.IsResourceSeen("cluster-a", "Pod", "default", "test-pod") {
		t.Error("Resource should not be seen after clearing")
	}
}

// A same-named resource in a different cluster must not read as already-seen:
// the store is shared across kubeconfig context switches, and an unqualified
// key would drop the add for the second cluster's resource.
func TestMemoryStore_ResourceSeen_ClusterScoped(t *testing.T) {
	store := NewMemoryStore(100)

	store.MarkResourceSeen("cluster-a", "Deployment", "team-a", "web")

	if !store.IsResourceSeen("cluster-a", "Deployment", "team-a", "web") {
		t.Error("cluster-a/web should be seen after marking")
	}
	if store.IsResourceSeen("cluster-b", "Deployment", "team-a", "web") {
		t.Error("cluster-b/web must NOT be suppressed by cluster-a's seen entry")
	}
}

// A relist re-emits the same informer id; the ring must collapse it to one
// row instead of appending a duplicate.
func TestMemoryStore_DedupesIdenticalInformerID(t *testing.T) {
	store := NewMemoryStore(100)
	ctx := context.Background()

	rv := "100"
	add := NewInformerEvent("Deployment", "apps/v1", "team-a", "web", "uid-1", rv, EventTypeAdd, HealthHealthy, nil, nil, nil, nil)
	relistUpdate := NewInformerEvent("Deployment", "apps/v1", "team-a", "web", "uid-1", rv, EventTypeUpdate, HealthHealthy, nil, nil, nil, nil)
	if add.ID != relistUpdate.ID {
		t.Fatalf("relist add/update must share an id: %q vs %q", add.ID, relistUpdate.ID)
	}

	_ = store.Append(ctx, add)
	_ = store.Append(ctx, relistUpdate)

	got, err := store.Query(ctx, QueryOptions{Limit: 10, IncludeManaged: true})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 deduped row, got %d: %+v", len(got), got)
	}

	del := NewInformerEvent("Deployment", "apps/v1", "team-a", "web", "uid-1", rv, EventTypeDelete, HealthUnknown, nil, nil, nil, nil)
	if del.ID == add.ID {
		t.Fatalf("delete must get a distinct id, got %q for both", del.ID)
	}
	_ = store.Append(ctx, del)
	got, _ = store.Query(ctx, QueryOptions{Limit: 10, IncludeManaged: true})
	if len(got) != 2 {
		t.Fatalf("expected add + delete = 2 rows, got %d", len(got))
	}
}

// A K8s Event's count bump reuses the uid-based id; the store must update the
// existing row in place, not drop the bump or append a duplicate.
func TestMemoryStore_K8sEventCountBumpUpserts(t *testing.T) {
	store := NewMemoryStore(100)
	ctx := context.Background()

	mk := func(count int32) TimelineEvent {
		return TimelineEvent{
			ID: "k8s-uid-1", Timestamp: time.Now(), Source: SourceK8sEvent,
			Kind: "Pod", Namespace: "team-a", Name: "web-abc",
			EventType: EventTypeWarning, Reason: "BackOff", Count: count,
		}
	}
	_ = store.Append(ctx, mk(1))
	_ = store.Append(ctx, mk(5))

	got, err := store.Query(ctx, QueryOptions{Limit: 10, IncludeManaged: true, IncludeK8sEvents: true})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 upserted row, got %d: %+v", len(got), got)
	}
	if got[0].Count != 5 {
		t.Fatalf("expected refreshed count 5, got %d", got[0].Count)
	}
}

// A K8s Event count bump carries a fresh timestamp; queries iterate by ring
// position, so the bumped event must move to the head of the recency order
// instead of staying buried at its original insert position.
func TestMemoryStore_K8sEventBumpMovesToRecency(t *testing.T) {
	store := NewMemoryStore(100)
	ctx := context.Background()
	base := time.Now()

	a := TimelineEvent{
		ID: "a", Source: SourceK8sEvent, Kind: "Pod", Namespace: "ns", Name: "web-a",
		EventType: EventTypeWarning, Reason: "BackOff", Count: 1, Timestamp: base,
	}
	b := TimelineEvent{
		ID: "b", Source: SourceInformer, Kind: "Deployment", Namespace: "ns", Name: "web-b",
		EventType: EventTypeUpdate, Timestamp: base.Add(time.Second),
	}
	_ = store.Append(ctx, a)
	_ = store.Append(ctx, b)

	q := func() []TimelineEvent {
		got, err := store.Query(ctx, QueryOptions{Limit: 10, IncludeManaged: true, IncludeK8sEvents: true})
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		return got
	}

	// Before the bump, newest-first order is [b, a].
	got := q()
	if len(got) != 2 || got[0].ID != "b" || got[1].ID != "a" {
		t.Fatalf("pre-bump order = %+v, want [b a]", got)
	}

	aBump := a
	aBump.Count = 5
	aBump.Timestamp = base.Add(2 * time.Second)
	_ = store.Append(ctx, aBump)

	// After the bump, a is newest → order flips to [a, b], still one row per id.
	got = q()
	if len(got) != 2 {
		t.Fatalf("expected 2 rows after bump, got %d: %+v", len(got), got)
	}
	if got[0].ID != "a" || got[0].Count != 5 {
		t.Fatalf("bumped event not at head with refreshed count, got %+v", got)
	}
	if got[1].ID != "b" {
		t.Fatalf("second row = %s, want b", got[1].ID)
	}
	if store.Stats().TotalEvents != 2 {
		t.Fatalf("Stats.TotalEvents = %d, want 2 (vacated slot must not count)", store.Stats().TotalEvents)
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

// Seq is the delta cursor: strictly increasing per arrival, and SinceSeq must
// return exactly the events that arrived after the cursor — regardless of
// their timestamps (a late event with an older timestamp still lands ahead).
func TestMemoryStore_SeqCursor(t *testing.T) {
	store := NewMemoryStore(100)
	ctx := context.Background()

	base := time.Now()
	mk := func(id string, ts time.Time) TimelineEvent {
		return TimelineEvent{
			ID: id, Timestamp: ts, Source: SourceInformer,
			Kind: "Pod", Namespace: "default", Name: id, EventType: EventTypeUpdate,
		}
	}
	if err := store.AppendBatch(ctx, []TimelineEvent{mk("a", base), mk("b", base.Add(time.Second))}); err != nil {
		t.Fatalf("AppendBatch: %v", err)
	}
	all, err := store.Query(ctx, QueryOptions{Limit: 10, IncludeManaged: true})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(all) != 2 || all[0].Seq <= all[1].Seq || all[1].Seq == 0 {
		t.Fatalf("expected 2 events with increasing arrival seq (newest first), got %+v", all)
	}
	cursor := all[0].Seq // newest arrival

	// A late event: OLDER timestamp than anything stored, but it arrives now.
	if err := store.Append(ctx, mk("late", base.Add(-time.Hour))); err != nil {
		t.Fatalf("Append late: %v", err)
	}
	delta, err := store.Query(ctx, QueryOptions{Limit: 10, IncludeManaged: true, SinceSeq: cursor})
	if err != nil {
		t.Fatalf("Query delta: %v", err)
	}
	if len(delta) != 1 || delta[0].ID != "late" {
		t.Fatalf("expected exactly the late arrival past the cursor, got %+v", delta)
	}
	if delta[0].Seq <= cursor {
		t.Fatalf("late arrival seq %d must exceed cursor %d", delta[0].Seq, cursor)
	}
}

// A K8s Event count bump re-appends at head, so its seq must advance past an
// existing cursor — otherwise a delta reader never sees the bump.
func TestMemoryStore_K8sEventBumpAdvancesSeq(t *testing.T) {
	store := NewMemoryStore(100)
	ctx := context.Background()

	base := time.Now()
	mk := func(count int32, ts time.Time) TimelineEvent {
		return TimelineEvent{
			ID: "k8s-uid-1", Timestamp: ts, Source: SourceK8sEvent,
			Kind: "Pod", Namespace: "default", Name: "web-abc",
			EventType: EventTypeWarning, Reason: "BackOff", Count: count,
		}
	}
	if err := store.Append(ctx, mk(1, base)); err != nil {
		t.Fatalf("Append first: %v", err)
	}
	first, err := store.Query(ctx, QueryOptions{Limit: 10, IncludeManaged: true, IncludeK8sEvents: true})
	if err != nil || len(first) != 1 {
		t.Fatalf("Query first: %v %+v", err, first)
	}
	cursor := first[0].Seq

	if err := store.Append(ctx, mk(5, base.Add(30*time.Second))); err != nil {
		t.Fatalf("Append bump: %v", err)
	}
	delta, err := store.Query(ctx, QueryOptions{Limit: 10, IncludeManaged: true, IncludeK8sEvents: true, SinceSeq: cursor})
	if err != nil {
		t.Fatalf("Query delta: %v", err)
	}
	if len(delta) != 1 || delta[0].Count != 5 {
		t.Fatalf("expected the bumped row past the cursor, got %+v", delta)
	}
}

// A bump that lost its enrichment (tombstone expired) must not erase the
// owner/labels/createdAt the row already knows; a bump that carries fresh
// enrichment wins.
func TestMemoryStore_K8sEventBumpPreservesEnrichment(t *testing.T) {
	store := NewMemoryStore(100)
	ctx := context.Background()

	base := time.Now()
	born := base.Add(-time.Hour)
	enriched := TimelineEvent{
		ID: "k8s-uid-1", Timestamp: base, Source: SourceK8sEvent,
		Kind: "Pod", Namespace: "default", Name: "web-abc",
		EventType: EventTypeWarning, Reason: "BackOff", Count: 1,
		CreatedAt: &born,
		Owner:     &OwnerInfo{Kind: "ReplicaSet", Name: "web"},
		Labels:    map[string]string{"app": "web"},
	}
	if err := store.Append(ctx, enriched); err != nil {
		t.Fatalf("Append enriched: %v", err)
	}
	bare := TimelineEvent{
		ID: "k8s-uid-1", Timestamp: base.Add(30 * time.Second), Source: SourceK8sEvent,
		Kind: "Pod", Namespace: "default", Name: "web-abc",
		EventType: EventTypeWarning, Reason: "BackOff", Count: 5,
	}
	if err := store.Append(ctx, bare); err != nil {
		t.Fatalf("Append bare bump: %v", err)
	}

	got, err := store.Query(ctx, QueryOptions{Limit: 10, IncludeManaged: true, IncludeK8sEvents: true})
	if err != nil || len(got) != 1 {
		t.Fatalf("Query: %v %+v", err, got)
	}
	if got[0].Count != 5 {
		t.Fatalf("expected bumped count 5, got %d", got[0].Count)
	}
	if got[0].CreatedAt == nil || !got[0].CreatedAt.Equal(born) {
		t.Fatalf("bare bump erased CreatedAt: %+v", got[0].CreatedAt)
	}
	if got[0].Owner == nil || got[0].Owner.Name != "web" {
		t.Fatalf("bare bump erased Owner: %+v", got[0].Owner)
	}
	if got[0].Labels["app"] != "web" {
		t.Fatalf("bare bump erased Labels: %+v", got[0].Labels)
	}

	// The inverse: a bump that carries enrichment fills a row that lacked it.
	if err := store.Append(ctx, TimelineEvent{
		ID: "k8s-uid-2", Timestamp: base, Source: SourceK8sEvent,
		Kind: "Pod", Namespace: "default", Name: "late-enriched",
		EventType: EventTypeWarning, Reason: "BackOff", Count: 1,
	}); err != nil {
		t.Fatalf("Append bare first: %v", err)
	}
	if err := store.Append(ctx, TimelineEvent{
		ID: "k8s-uid-2", Timestamp: base.Add(time.Minute), Source: SourceK8sEvent,
		Kind: "Pod", Namespace: "default", Name: "late-enriched",
		EventType: EventTypeWarning, Reason: "BackOff", Count: 2,
		Owner: &OwnerInfo{Kind: "Job", Name: "batch"},
	}); err != nil {
		t.Fatalf("Append enriched bump: %v", err)
	}
	row, err := store.GetEvent(ctx, "k8s-uid-2")
	if err != nil || row == nil {
		t.Fatalf("GetEvent: %v %+v", err, row)
	}
	if row.Owner == nil || row.Owner.Name != "batch" {
		t.Fatalf("enriched bump did not fill Owner: %+v", row.Owner)
	}
}

// Delta reads must page by ascending arrival order so a burst larger than the
// page limit isn't skipped. The server advances the client cursor by the max
// seq in each page, so successive polls must cover every unseen event with no
// gaps — even though timestamps ascend with arrival (where a newest-first page
// would return the highest seqs and strand the lower ones).
func TestMemoryStore_DeltaPagesAscendingUnderLimit(t *testing.T) {
	store := NewMemoryStore(100)
	ctx := context.Background()

	base := time.Now()
	for i := 0; i < 6; i++ {
		ev := TimelineEvent{
			ID: string(rune('a' + i)), Timestamp: base.Add(time.Duration(i) * time.Second),
			Source: SourceInformer, Kind: "Deployment", Namespace: "default",
			Name: string(rune('a' + i)), EventType: EventTypeUpdate,
		}
		if err := store.Append(ctx, ev); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	// Arrivals now carry seq 1..6, timestamps ascending with seq.

	cursor := int64(1)
	seen := map[int64]bool{}
	for polls := 0; polls < 10; polls++ {
		page, err := store.Query(ctx, QueryOptions{Limit: 2, IncludeManaged: true, SinceSeq: cursor})
		if err != nil {
			t.Fatalf("delta query: %v", err)
		}
		if len(page) == 0 {
			break
		}
		// Ascending within the page.
		if len(page) == 2 && page[0].Seq >= page[1].Seq {
			t.Fatalf("delta page not ascending by seq: %+v", []int64{page[0].Seq, page[1].Seq})
		}
		var maxSeq int64
		for _, e := range page {
			if seen[e.Seq] {
				t.Fatalf("seq %d returned twice across polls", e.Seq)
			}
			seen[e.Seq] = true
			if e.Seq > maxSeq {
				maxSeq = e.Seq
			}
		}
		cursor = maxSeq // mirror server cursor advancement
	}

	// Every seq strictly above the initial cursor (2..6) must have been seen.
	for want := int64(2); want <= 6; want++ {
		if !seen[want] {
			t.Fatalf("delta paging skipped seq %d; seen=%v", want, seen)
		}
	}
}

// A store standing in for a failed persistent backend reports itself degraded
// through Stats so diagnostics can explain the missing persistence.
func TestNewDegradedMemoryStore_ReportsDegraded(t *testing.T) {
	degraded := NewDegradedMemoryStore(100, "SQLite unusable: boom")
	stats := degraded.Stats()
	if !stats.Degraded || stats.DegradedReason != "SQLite unusable: boom" {
		t.Fatalf("expected degraded stats with reason, got %+v", stats)
	}

	healthy := NewMemoryStore(100)
	if hs := healthy.Stats(); hs.Degraded || hs.DegradedReason != "" {
		t.Fatalf("plain memory store must not report degraded, got %+v", hs)
	}
}
