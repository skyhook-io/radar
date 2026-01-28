package timeline

import (
	"context"
	"sync"
	"time"
)

// MemoryStore is an in-memory implementation of EventStore using a ring buffer.
// Suitable for local development and testing. Events are lost on restart.
type MemoryStore struct {
	records       []TimelineEvent
	maxSize       int
	head          int // next write position
	count         int
	mu            sync.RWMutex
	seenResources map[string]bool
	seenMu        sync.RWMutex
	filterCache   map[string]*CompiledFilter
}

// NewMemoryStore creates a new in-memory event store
func NewMemoryStore(maxSize int) *MemoryStore {
	if maxSize <= 0 {
		maxSize = 1000
	}
	return &MemoryStore{
		records:       make([]TimelineEvent, maxSize),
		maxSize:       maxSize,
		seenResources: make(map[string]bool),
		filterCache:   make(map[string]*CompiledFilter),
	}
}

// Append adds a single event to the store
func (m *MemoryStore) Append(ctx context.Context, event TimelineEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.records[m.head] = event
	m.head = (m.head + 1) % m.maxSize
	if m.count < m.maxSize {
		m.count++
	}
	return nil
}

// AppendBatch adds multiple events atomically
func (m *MemoryStore) AppendBatch(ctx context.Context, events []TimelineEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, event := range events {
		m.records[m.head] = event
		m.head = (m.head + 1) % m.maxSize
		if m.count < m.maxSize {
			m.count++
		}
	}
	return nil
}

// Query retrieves events matching the given options
func (m *MemoryStore) Query(ctx context.Context, opts QueryOptions) ([]TimelineEvent, error) {
	// Get filter preset BEFORE acquiring the read lock to avoid deadlock
	// (getOrCompileFilter may acquire its own lock)
	var cf *CompiledFilter
	if opts.FilterPreset != "" {
		var err error
		cf, err = m.getOrCompileFilter(opts.FilterPreset)
		if err != nil {
			return nil, err
		}
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	limit := opts.Limit
	if limit <= 0 {
		limit = 200
	}
	if limit > 10000 {
		limit = 10000
	}

	results := make([]TimelineEvent, 0, limit)
	skipped := 0

	// Iterate backwards from most recent
	for i := 0; i < m.count && len(results) < limit; i++ {
		idx := (m.head - 1 - i + m.maxSize) % m.maxSize
		event := m.records[idx]

		// Skip empty records
		if event.ID == "" {
			continue
		}

		// Apply filters
		if !m.matchesFilters(&event, opts, cf) {
			continue
		}

		// Handle offset
		if opts.Offset > 0 && skipped < opts.Offset {
			skipped++
			continue
		}

		results = append(results, event)
	}

	return results, nil
}

// QueryGrouped retrieves events grouped according to the specified mode
func (m *MemoryStore) QueryGrouped(ctx context.Context, opts QueryOptions) (*TimelineResponse, error) {
	startTime := time.Now()

	// First get all matching events
	events, err := m.Query(ctx, QueryOptions{
		Namespace:        opts.Namespace,
		Kinds:            opts.Kinds,
		Since:            opts.Since,
		Until:            opts.Until,
		Sources:          opts.Sources,
		FilterPreset:     opts.FilterPreset,
		Limit:            opts.Limit * 10, // Get more events for grouping
		IncludeManaged:   opts.IncludeManaged,
		IncludeK8sEvents: opts.IncludeK8sEvents,
	})
	if err != nil {
		return nil, err
	}

	if opts.GroupBy == GroupByNone {
		// No grouping - return flat list
		if len(events) > opts.Limit {
			events = events[:opts.Limit]
		}
		return &TimelineResponse{
			Ungrouped: events,
			Meta: TimelineMeta{
				TotalEvents: len(events),
				QueryTimeMs: time.Since(startTime).Milliseconds(),
				HasMore:     len(events) == opts.Limit,
			},
		}, nil
	}

	// Group events using shared function
	groups := groupEvents(events, opts.GroupBy)

	// Apply limit to groups
	limit := opts.Limit
	if limit <= 0 {
		limit = 200
	}
	hasMore := len(groups) > limit
	if hasMore {
		groups = groups[:limit]
	}

	return &TimelineResponse{
		Groups: groups,
		Meta: TimelineMeta{
			TotalEvents: len(events),
			GroupCount:  len(groups),
			QueryTimeMs: time.Since(startTime).Milliseconds(),
			HasMore:     hasMore,
		},
	}, nil
}

// GetEvent retrieves a single event by ID
func (m *MemoryStore) GetEvent(ctx context.Context, id string) (*TimelineEvent, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for i := 0; i < m.count; i++ {
		idx := (m.head - 1 - i + m.maxSize) % m.maxSize
		if m.records[idx].ID == id {
			event := m.records[idx]
			return &event, nil
		}
	}
	return nil, nil
}

// GetChangesForOwner retrieves changes for resources owned by the given owner
func (m *MemoryStore) GetChangesForOwner(ctx context.Context, ownerKind, ownerNamespace, ownerName string, since time.Time, limit int) ([]TimelineEvent, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if limit <= 0 {
		limit = 100
	}

	results := make([]TimelineEvent, 0, limit)

	for i := 0; i < m.count && len(results) < limit; i++ {
		idx := (m.head - 1 - i + m.maxSize) % m.maxSize
		event := m.records[idx]

		if event.ID == "" {
			continue
		}

		if !since.IsZero() && event.Timestamp.Before(since) {
			continue
		}

		if event.Namespace != ownerNamespace {
			continue
		}

		// Check if this event's owner matches
		if event.Owner != nil && event.Owner.Kind == ownerKind && event.Owner.Name == ownerName {
			results = append(results, event)
		}
	}

	return results, nil
}

// MarkResourceSeen records that a resource has been seen
func (m *MemoryStore) MarkResourceSeen(kind, namespace, name string) {
	m.seenMu.Lock()
	defer m.seenMu.Unlock()
	m.seenResources[ResourceKey(kind, namespace, name)] = true
}

// IsResourceSeen checks if a resource has been seen before
func (m *MemoryStore) IsResourceSeen(kind, namespace, name string) bool {
	m.seenMu.RLock()
	defer m.seenMu.RUnlock()
	return m.seenResources[ResourceKey(kind, namespace, name)]
}

// ClearResourceSeen removes a resource from the seen set
func (m *MemoryStore) ClearResourceSeen(kind, namespace, name string) {
	m.seenMu.Lock()
	defer m.seenMu.Unlock()
	delete(m.seenResources, ResourceKey(kind, namespace, name))
}

// Stats returns storage statistics
func (m *MemoryStore) Stats() StoreStats {
	m.mu.RLock()
	defer m.mu.RUnlock()
	m.seenMu.RLock()
	defer m.seenMu.RUnlock()

	var oldest, newest time.Time
	for i := 0; i < m.count; i++ {
		idx := (m.head - 1 - i + m.maxSize) % m.maxSize
		if m.records[idx].ID == "" {
			continue
		}
		ts := m.records[idx].Timestamp
		if newest.IsZero() || ts.After(newest) {
			newest = ts
		}
		if oldest.IsZero() || ts.Before(oldest) {
			oldest = ts
		}
	}

	return StoreStats{
		TotalEvents:   int64(m.count),
		OldestEvent:   oldest,
		NewestEvent:   newest,
		SeenResources: len(m.seenResources),
	}
}

// Close releases any resources held by the store
func (m *MemoryStore) Close() error {
	return nil
}

// matchesFilters checks if an event matches the query filters
func (m *MemoryStore) matchesFilters(event *TimelineEvent, opts QueryOptions, cf *CompiledFilter) bool {
	// Apply compiled filter preset
	if cf != nil && !cf.Matches(event) {
		return false
	}

	// Apply individual filters (these override preset if both specified)
	if !opts.Since.IsZero() && event.Timestamp.Before(opts.Since) {
		return false
	}

	if !opts.Until.IsZero() && event.Timestamp.After(opts.Until) {
		return false
	}

	if opts.Namespace != "" && event.Namespace != opts.Namespace {
		return false
	}

	if len(opts.Kinds) > 0 {
		found := false
		for _, k := range opts.Kinds {
			if event.Kind == k {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	if len(opts.Sources) > 0 {
		found := false
		for _, s := range opts.Sources {
			if event.Source == s {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	// Handle IncludeManaged
	// If opts.IncludeManaged is true, it overrides the preset's IncludeManaged setting
	// This allows queries to explicitly request managed resources even with "default" preset
	if event.IsManaged() && !opts.IncludeManaged {
		// Check preset's IncludeManaged if a preset is applied
		if cf != nil && cf.preset != nil && !cf.preset.IncludeManaged {
			return false
		}
		// If no preset, exclude managed by default
		if cf == nil {
			return false
		}
	}

	// Handle IncludeK8sEvents
	if !opts.IncludeK8sEvents && event.Source == SourceK8sEvent {
		return false
	}

	return true
}

// getOrCompileFilter returns a cached compiled filter or compiles a new one
func (m *MemoryStore) getOrCompileFilter(presetName string) (*CompiledFilter, error) {
	m.mu.RLock()
	if cf, ok := m.filterCache[presetName]; ok {
		m.mu.RUnlock()
		return cf, nil
	}
	m.mu.RUnlock()

	presets := DefaultFilterPresets()
	preset, ok := presets[presetName]
	if !ok {
		return nil, nil // Unknown preset - no filtering
	}

	cf, err := CompileFilter(&preset)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.filterCache[presetName] = cf
	m.mu.Unlock()

	return cf, nil
}

// Note: groupEvents, groupByOwner, groupByApp, groupByNamespace, and worseHealth
// are defined in sqlite_store.go and shared by all store implementations
