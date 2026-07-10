package timeline

import (
	"context"
	"slices"
	"sync"
	"time"
)

// MemoryStore is an in-memory implementation of EventStore using a ring buffer.
// Suitable for local development and testing. Events are lost on restart.
type MemoryStore struct {
	records       []TimelineEvent
	maxSize       int
	head          int   // next write position
	count         int
	lastSeq       int64 // arrival counter; every head write (incl. upsert re-append) takes the next value
	index         map[string]int // event id -> ring slot, for dedup + upsert
	mu            sync.RWMutex
	seenResources map[string]bool
	seenMu        sync.RWMutex
	filterCache   map[string]*CompiledFilter

	// degradedReason, when non-empty, marks this store as a fallback for a
	// persistent backend that could not be opened. Set once at construction
	// before the store is published, so it needs no lock. Reported via Stats.
	degradedReason string
}

// NewMemoryStore creates a new in-memory event store
func NewMemoryStore(maxSize int) *MemoryStore {
	if maxSize <= 0 {
		maxSize = 1000
	}
	return &MemoryStore{
		records:       make([]TimelineEvent, maxSize),
		maxSize:       maxSize,
		index:         make(map[string]int),
		seenResources: make(map[string]bool),
		filterCache:   make(map[string]*CompiledFilter),
	}
}

// NewDegradedMemoryStore returns an in-memory store that reports itself as a
// degraded fallback via Stats. Used when the configured persistent backend
// could not be opened, so the timeline stays alive (without persistence) for
// the session instead of the whole subsystem going dark.
func NewDegradedMemoryStore(maxSize int, reason string) *MemoryStore {
	m := NewMemoryStore(maxSize)
	m.degradedReason = reason
	return m
}

// Append adds a single event to the store
func (m *MemoryStore) Append(ctx context.Context, event TimelineEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.appendLocked(event)
	return nil
}

// AppendBatch adds multiple events atomically
func (m *MemoryStore) AppendBatch(ctx context.Context, events []TimelineEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, event := range events {
		m.appendLocked(event)
	}
	return nil
}

// appendLocked writes one event, collapsing duplicate ids so a relist/replay
// never produces two visible rows. An existing id from a mutable K8s Event
// (same uid, bumped count) vacates its old slot and re-appends at the head;
// an identical informer/historical id keeps the original row (same state,
// same first-observed timestamp).
func (m *MemoryStore) appendLocked(event TimelineEvent) {
	if event.ID != "" {
		if idx, ok := m.index[event.ID]; ok && m.records[idx].ID == event.ID {
			// K8s Events bump count/message on the same uid, but an out-of-order
			// older revision must not clobber a newer one.
			if event.Source == SourceK8sEvent && !event.Timestamp.Before(m.records[idx].Timestamp) {
				// A bump that lost its enrichment (tombstone expired, object gone
				// from the live cache) must not erase what the row already knows;
				// a bump that carries enrichment wins as the fresher truth.
				old := m.records[idx]
				if event.CreatedAt == nil {
					event.CreatedAt = old.CreatedAt
				}
				if event.Owner == nil {
					event.Owner = old.Owner
				}
				if event.Labels == nil {
					event.Labels = old.Labels
				}
				// Vacate the old slot and re-append at head. Queries iterate by
				// ring position (newest insert first), so updating in place would
				// leave a count-bump buried at its stale recency; moving it to
				// head reflects the fresh timestamp. Keeps one live row per id.
				m.records[idx] = TimelineEvent{}
				delete(m.index, event.ID)
				m.writeAtHead(event)
			}
			return
		}
	}

	m.writeAtHead(event)
}

// writeAtHead writes event at the head slot, advancing the ring. count tracks
// the window span behind head (holes left by upsert vacating are counted here
// and skipped on read), so the oldest live row is never scanned past.
func (m *MemoryStore) writeAtHead(event TimelineEvent) {
	// Drop the slot's current occupant from the index before overwriting it,
	// so a wrapped-over id can't leave a dangling mapping.
	if evicted := m.records[m.head]; evicted.ID != "" {
		if idx, ok := m.index[evicted.ID]; ok && idx == m.head {
			delete(m.index, evicted.ID)
		}
	}

	m.lastSeq++
	event.Seq = m.lastSeq
	m.records[m.head] = event
	if event.ID != "" {
		m.index[event.ID] = m.head
	}
	m.head = (m.head + 1) % m.maxSize
	if m.count < m.maxSize {
		m.count++
	}
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

	// Delta reads (SinceSeq>0) page by ascending arrival order so a burst larger
	// than the limit isn't skipped: the server advances the client cursor by the
	// max seq in the page, so the next poll must resume from the lowest unseen
	// seq. Ring position tracks arrival order (each writeAtHead takes the next
	// seq), so oldest-first iteration yields ascending seq. Non-delta reads page
	// newest-first.
	deltaAscending := opts.SinceSeq > 0

	for i := 0; i < m.count && len(results) < limit; i++ {
		var idx int
		if deltaAscending {
			idx = (m.head - m.count + i + m.maxSize) % m.maxSize
		} else {
			idx = (m.head - 1 - i + m.maxSize) % m.maxSize
		}
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

	// First get all matching events, with a higher limit for grouping. Copy the
	// full option struct so new filters can't drift between Query and grouped
	// queries.
	queryOpts := opts
	queryOpts.Limit = opts.Limit * 10
	events, err := m.Query(ctx, queryOpts)
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
	groups := GroupEvents(events, opts.GroupBy)

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
func (m *MemoryStore) GetChangesForOwner(ctx context.Context, ownerKind, ownerNamespace, ownerName, clusterContext string, since time.Time, limit int) ([]TimelineEvent, error) {
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

		if clusterContext != "" && event.ClusterContext != clusterContext {
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
func (m *MemoryStore) MarkResourceSeen(clusterContext, kind, namespace, name string) {
	m.seenMu.Lock()
	defer m.seenMu.Unlock()
	m.seenResources[SeenResourceKey(clusterContext, kind, namespace, name)] = true
}

// IsResourceSeen checks if a resource has been seen before
func (m *MemoryStore) IsResourceSeen(clusterContext, kind, namespace, name string) bool {
	m.seenMu.RLock()
	defer m.seenMu.RUnlock()
	return m.seenResources[SeenResourceKey(clusterContext, kind, namespace, name)]
}

// ClearResourceSeen removes a resource from the seen set
func (m *MemoryStore) ClearResourceSeen(clusterContext, kind, namespace, name string) {
	m.seenMu.Lock()
	defer m.seenMu.Unlock()
	delete(m.seenResources, SeenResourceKey(clusterContext, kind, namespace, name))
}

// Stats returns storage statistics
func (m *MemoryStore) Stats() StoreStats {
	m.mu.RLock()
	defer m.mu.RUnlock()
	m.seenMu.RLock()
	defer m.seenMu.RUnlock()

	var oldest, newest time.Time
	// count is the ring window span, which can include holes left by upsert
	// vacating a slot — count live records instead of reporting the span.
	var total int64
	for i := 0; i < m.count; i++ {
		idx := (m.head - 1 - i + m.maxSize) % m.maxSize
		if m.records[idx].ID == "" {
			continue
		}
		total++
		ts := m.records[idx].Timestamp
		if newest.IsZero() || ts.After(newest) {
			newest = ts
		}
		if oldest.IsZero() || ts.Before(oldest) {
			oldest = ts
		}
	}

	return StoreStats{
		TotalEvents:    total,
		OldestEvent:    oldest,
		NewestEvent:    newest,
		SeenResources:  len(m.seenResources),
		Degraded:       m.degradedReason != "",
		DegradedReason: m.degradedReason,
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
	if opts.ClusterContext != "" && event.ClusterContext != opts.ClusterContext {
		return false
	}

	if opts.SinceSeq > 0 && event.Seq <= opts.SinceSeq {
		return false
	}

	if !opts.Since.IsZero() && event.Timestamp.Before(opts.Since) {
		return false
	}

	if !opts.Until.IsZero() && event.Timestamp.After(opts.Until) {
		return false
	}

	if len(opts.Namespaces) > 0 {
		found := slices.Contains(opts.Namespaces, event.Namespace)
		if !found {
			return false
		}
	}

	if len(opts.Kinds) > 0 {
		found := slices.Contains(opts.Kinds, event.Kind)
		if !found {
			return false
		}
	}

	if len(opts.Names) > 0 {
		found := slices.Contains(opts.Names, event.Name)
		if !found {
			return false
		}
	}

	if len(opts.Sources) > 0 {
		found := slices.Contains(opts.Sources, event.Source)
		if !found {
			return false
		}
	}

	if len(opts.EventTypes) > 0 {
		found := slices.Contains(opts.EventTypes, event.EventType)
		if !found {
			return false
		}
	}

	if opts.ExcludeDeleted && event.EventType == EventTypeDelete {
		return false
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

// Note: groupEvents and helpers are defined in grouping.go
