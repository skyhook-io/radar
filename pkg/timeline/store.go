package timeline

import (
	"context"
	"regexp"
	"time"
)

// EventStore is the interface for timeline event storage backends.
// Implementations must be safe for concurrent use.
type EventStore interface {
	// Append adds a single event to the store
	Append(ctx context.Context, event TimelineEvent) error

	// AppendBatch adds multiple events atomically
	AppendBatch(ctx context.Context, events []TimelineEvent) error

	// Query retrieves events matching the given options
	Query(ctx context.Context, opts QueryOptions) ([]TimelineEvent, error)

	// QueryGrouped retrieves events grouped according to the specified mode
	QueryGrouped(ctx context.Context, opts QueryOptions) (*TimelineResponse, error)

	// GetEvent retrieves a single event by ID
	GetEvent(ctx context.Context, id string) (*TimelineEvent, error)

	// GetChangesForOwner retrieves changes for resources owned by the given
	// owner. clusterContext scopes to one cluster's events ("" = all) — owner
	// identity (kind/namespace/name) collides across clusters in a persistent
	// store, so current-cluster callers must pass it.
	GetChangesForOwner(ctx context.Context, ownerKind, ownerNamespace, ownerName, clusterContext string, since time.Time, limit int) ([]TimelineEvent, error)

	// MarkResourceSeen records that a resource has been seen (for dedup on restart)
	MarkResourceSeen(kind, namespace, name string)

	// IsResourceSeen checks if a resource has been seen before
	IsResourceSeen(kind, namespace, name string) bool

	// ClearResourceSeen removes a resource from the seen set (on delete)
	ClearResourceSeen(kind, namespace, name string)

	// Stats returns storage statistics
	Stats() StoreStats

	// Close releases any resources held by the store
	Close() error
}

// QueryOptions configures event queries
type QueryOptions struct {
	// Filters
	Namespaces []string      // Filter by namespaces (empty = all)
	Kinds      []string      // Filter by resource kinds (empty = all)
	Names      []string      // Filter by resource names (empty = all)
	Since      time.Time     // Filter events after this time
	Until      time.Time     // Filter events before this time
	Sources    []EventSource // Filter by event source (empty = all)
	EventTypes []EventType   // Filter by event type, e.g. add/delete (empty = all)
	// ClusterContext scopes results to one cluster's events (empty = all).
	// Anything answering "what happened on THIS cluster" must set it: the
	// SQLite store outlives context switches, and rows written before the
	// column existed carry "" (unknowable provenance), which a non-empty
	// filter deliberately excludes.
	ClusterContext string

	// Filter preset (overrides individual filters if set)
	FilterPreset string

	// Pagination
	Limit  int // Max results (default 200, max 1000)
	Offset int // Skip first N results

	// Grouping
	GroupBy GroupingMode // How to group results

	// Include/exclude options
	IncludeManaged   bool // Include ReplicaSets, Pods, Events (default false)
	ExcludeDeleted   bool // Exclude delete events
	IncludeK8sEvents bool // Include K8s Event resources (default true)
}

// DefaultQueryOptions returns sensible defaults
func DefaultQueryOptions() QueryOptions {
	return QueryOptions{
		Limit:            200,
		GroupBy:          GroupByNone,
		IncludeManaged:   false,
		ExcludeDeleted:   false,
		IncludeK8sEvents: true,
	}
}

// StoreStats contains statistics about the event store
type StoreStats struct {
	TotalEvents   int64     `json:"totalEvents"`
	OldestEvent   time.Time `json:"oldestEvent"`
	NewestEvent   time.Time `json:"newestEvent"`
	StorageBytes  int64     `json:"storageBytes,omitempty"`
	SeenResources int       `json:"seenResources"`

	// SQLite-only retention/cleanup state. Zero values for memory store.
	RetentionAge           time.Duration `json:"retentionAge,omitempty"`
	MaxStorageBytes        int64         `json:"maxStorageBytes,omitempty"`
	LastCleanupAt          time.Time     `json:"lastCleanupAt,omitempty"`
	LastCleanupDeletedRows int64         `json:"lastCleanupDeletedRows,omitempty"`
	LastCleanupError       string        `json:"lastCleanupError,omitempty"`
}

// CompiledFilter is a pre-compiled filter for efficient event filtering
type CompiledFilter struct {
	preset            *FilterPreset
	excludeKindsMap   map[string]bool
	includeKindsMap   map[string]bool
	excludePatterns   []*regexp.Regexp
	includeEventTypes map[EventType]bool
	excludeOperations map[EventType]bool
}

// CompileFilter compiles a FilterPreset for efficient matching
func CompileFilter(preset *FilterPreset) (*CompiledFilter, error) {
	if preset == nil {
		return nil, nil
	}

	cf := &CompiledFilter{
		preset:            preset,
		excludeKindsMap:   make(map[string]bool),
		includeKindsMap:   make(map[string]bool),
		includeEventTypes: make(map[EventType]bool),
		excludeOperations: make(map[EventType]bool),
	}

	for _, k := range preset.ExcludeKinds {
		cf.excludeKindsMap[k] = true
	}
	for _, k := range preset.IncludeKinds {
		cf.includeKindsMap[k] = true
	}

	for _, pattern := range preset.ExcludeNamePatterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, err
		}
		cf.excludePatterns = append(cf.excludePatterns, re)
	}

	for _, t := range preset.IncludeEventTypes {
		cf.includeEventTypes[t] = true
	}
	for _, t := range preset.ExcludeOperations {
		cf.excludeOperations[t] = true
	}

	return cf, nil
}

// IncludesManaged reports whether the compiled preset allows managed resources.
func (cf *CompiledFilter) IncludesManaged() bool {
	return cf != nil && cf.preset != nil && cf.preset.IncludeManaged
}

// Matches returns true if the event passes the filter
func (cf *CompiledFilter) Matches(event *TimelineEvent) bool {
	if cf == nil || cf.preset == nil {
		return true
	}

	// Check include kinds (whitelist)
	if len(cf.includeKindsMap) > 0 && !cf.includeKindsMap[event.Kind] {
		return false
	}

	// Check exclude kinds (blacklist)
	if cf.excludeKindsMap[event.Kind] {
		return false
	}

	// Check exclude name patterns
	for _, re := range cf.excludePatterns {
		if re.MatchString(event.Name) {
			return false
		}
	}

	// Check include event types (whitelist)
	if len(cf.includeEventTypes) > 0 && !cf.includeEventTypes[event.EventType] {
		return false
	}

	// Check exclude operations
	if cf.excludeOperations[event.EventType] {
		return false
	}

	// Note: IncludeManaged is handled in matchesFilters to allow query option override
	return true
}

// ResourceKey generates a unique key for a resource
func ResourceKey(kind, namespace, name string) string {
	return kind + "/" + namespace + "/" + name
}
