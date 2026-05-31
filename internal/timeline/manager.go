package timeline

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"

	pkgtimeline "github.com/skyhook-io/radar/pkg/timeline"
)

// Re-export types from pkg/timeline so callers don't need to change imports.
type (
	// Core types
	EventSource      = pkgtimeline.EventSource
	EventType        = pkgtimeline.EventType
	HealthState      = pkgtimeline.HealthState
	GroupingMode     = pkgtimeline.GroupingMode
	TimelineEvent    = pkgtimeline.TimelineEvent
	EventGroup       = pkgtimeline.EventGroup
	TimelineResponse = pkgtimeline.TimelineResponse
	TimelineMeta     = pkgtimeline.TimelineMeta
	FilterPreset     = pkgtimeline.FilterPreset

	// Store types
	EventStore     = pkgtimeline.EventStore
	QueryOptions   = pkgtimeline.QueryOptions
	StoreStats     = pkgtimeline.StoreStats
	CompiledFilter = pkgtimeline.CompiledFilter

	// Config types
	StoreType   = pkgtimeline.StoreType
	StoreConfig = pkgtimeline.StoreConfig

	// k8score alias chain
	OwnerInfo   = pkgtimeline.OwnerInfo
	DiffInfo    = pkgtimeline.DiffInfo
	FieldChange = pkgtimeline.FieldChange
)

// Re-export constants from pkg/timeline.
const (
	// EventSource constants
	SourceInformer   = pkgtimeline.SourceInformer
	SourceK8sEvent   = pkgtimeline.SourceK8sEvent
	SourceHistorical = pkgtimeline.SourceHistorical

	// EventType constants
	EventTypeAdd     = pkgtimeline.EventTypeAdd
	EventTypeUpdate  = pkgtimeline.EventTypeUpdate
	EventTypeDelete  = pkgtimeline.EventTypeDelete
	EventTypeNormal  = pkgtimeline.EventTypeNormal
	EventTypeWarning = pkgtimeline.EventTypeWarning

	// HealthState constants
	HealthHealthy   = pkgtimeline.HealthHealthy
	HealthDegraded  = pkgtimeline.HealthDegraded
	HealthUnhealthy = pkgtimeline.HealthUnhealthy
	HealthUnknown   = pkgtimeline.HealthUnknown

	// GroupingMode constants
	GroupByNone      = pkgtimeline.GroupByNone
	GroupByOwner     = pkgtimeline.GroupByOwner
	GroupByApp       = pkgtimeline.GroupByApp
	GroupByNamespace = pkgtimeline.GroupByNamespace

	// StoreType constants
	StoreTypeMemory = pkgtimeline.StoreTypeMemory
	StoreTypeSQLite = pkgtimeline.StoreTypeSQLite
)

// Re-export functions from pkg/timeline.

func DefaultFilterPresets() map[string]FilterPreset { return pkgtimeline.DefaultFilterPresets() }
func DefaultQueryOptions() QueryOptions             { return pkgtimeline.DefaultQueryOptions() }
func DefaultStoreConfig() StoreConfig               { return pkgtimeline.DefaultStoreConfig() }
func CompileFilter(preset *FilterPreset) (*CompiledFilter, error) {
	return pkgtimeline.CompileFilter(preset)
}
func ResourceKey(kind, namespace, name string) string {
	return pkgtimeline.ResourceKey(kind, namespace, name)
}

// Converter functions.
func NewInformerEvent(kind, apiVersion, namespace, name, uid string, operation EventType, healthState HealthState, diff *DiffInfo, owner *OwnerInfo, labels map[string]string, createdAt *time.Time) TimelineEvent {
	return pkgtimeline.NewInformerEvent(kind, apiVersion, namespace, name, uid, operation, healthState, diff, owner, labels, createdAt)
}
func NewK8sEventTimelineEvent(event *corev1.Event, owner *OwnerInfo) TimelineEvent {
	return pkgtimeline.NewK8sEventTimelineEvent(event, owner)
}
func NewHistoricalEvent(kind, apiVersion, namespace, name string, ts time.Time, reason, message string, healthState HealthState, owner *OwnerInfo, labels map[string]string) TimelineEvent {
	return pkgtimeline.NewHistoricalEvent(kind, apiVersion, namespace, name, ts, reason, message, healthState, owner, labels)
}
func ExtractOwner(obj any) *OwnerInfo         { return pkgtimeline.ExtractOwner(obj) }
func ExtractLabels(obj any) map[string]string { return pkgtimeline.ExtractLabels(obj) }
func DetermineHealthState(kind string, obj any) HealthState {
	return pkgtimeline.DetermineHealthState(kind, obj)
}
func OperationToEventType(op string) EventType  { return pkgtimeline.OperationToEventType(op) }
func EventTypeToOperation(et EventType) string  { return pkgtimeline.EventTypeToOperation(et) }
func HealthStateToString(hs HealthState) string { return pkgtimeline.HealthStateToString(hs) }
func StringToHealthState(s string) HealthState  { return pkgtimeline.StringToHealthState(s) }
func ToLegacyDiffInfo(d *DiffInfo) *DiffInfo    { return pkgtimeline.ToLegacyDiffInfo(d) }

// Store constructors.
func NewMemoryStore(maxSize int) *pkgtimeline.MemoryStore { return pkgtimeline.NewMemoryStore(maxSize) }

// ---------------------------------------------------------------------------
// Global store singleton
// ---------------------------------------------------------------------------

var (
	globalStore     EventStore
	globalStoreOnce sync.Once
	globalStoreMu   sync.Mutex
	globalConfig    StoreConfig

	// Event broadcast for SSE
	subscribers   []chan TimelineEvent
	subscribersMu sync.RWMutex
)

// InitStore initializes the global event store
func InitStore(cfg StoreConfig) error {
	var initErr error
	globalStoreOnce.Do(func() {
		globalConfig = cfg

		switch cfg.Type {
		case StoreTypeSQLite:
			if cfg.Path == "" {
				initErr = fmt.Errorf("SQLite store requires a path")
				return
			}
			store, err := NewSQLiteStore(cfg.Path)
			if err != nil {
				initErr = fmt.Errorf("failed to create SQLite store: %w", err)
				return
			}
			globalStore = store
			if cfg.RetentionAge > 0 || cfg.MaxStorageBytes > 0 {
				store.StartCleanupLoop(cfg.RetentionAge, time.Hour, cfg.MaxStorageBytes)
				log.Printf("Initialized SQLite event store at %s (retention: %s, max size: %d bytes)", cfg.Path, cfg.RetentionAge, cfg.MaxStorageBytes)
			} else {
				log.Printf("Initialized SQLite event store at %s (retention: disabled — events table will grow unbounded)", cfg.Path)
			}

		case StoreTypeMemory:
			fallthrough
		default:
			maxSize := cfg.MaxSize
			if maxSize <= 0 {
				maxSize = 1000
			}
			globalStore = NewMemoryStore(maxSize)
			log.Printf("Initialized in-memory event store (max %d events)", maxSize)
		}
	})
	return initErr
}

// GetStore returns the global event store instance
func GetStore() EventStore {
	return globalStore
}

// ResetStore stops and clears the event store.
// This must be called before reinitializing when switching contexts.
func ResetStore() {
	globalStoreMu.Lock()
	defer globalStoreMu.Unlock()

	if globalStore != nil {
		if err := globalStore.Close(); err != nil {
			log.Printf("Warning: error closing event store: %v", err)
		}
		globalStore = nil
	}
	globalStoreOnce = sync.Once{}
}

// ReinitStore reinitializes the event store after a context switch.
// Must call ResetStore first.
func ReinitStore(cfg StoreConfig) error {
	return InitStore(cfg)
}

// RecordEvent is a convenience function to record an event to the global store
func RecordEvent(ctx context.Context, event TimelineEvent) error {
	store := GetStore()
	if store == nil {
		return fmt.Errorf("event store not initialized")
	}
	return store.Append(ctx, event)
}

// RecordEvents is a convenience function to record multiple events to the global store
func RecordEvents(ctx context.Context, events []TimelineEvent) error {
	store := GetStore()
	if store == nil {
		return fmt.Errorf("event store not initialized")
	}
	return store.AppendBatch(ctx, events)
}

// QueryEvents is a convenience function to query events from the global store
func QueryEvents(ctx context.Context, opts QueryOptions) ([]TimelineEvent, error) {
	store := GetStore()
	if store == nil {
		return nil, fmt.Errorf("event store not initialized")
	}
	return store.Query(ctx, opts)
}

// QueryGrouped is a convenience function to query grouped events from the global store
func QueryGrouped(ctx context.Context, opts QueryOptions) (*TimelineResponse, error) {
	store := GetStore()
	if store == nil {
		return nil, fmt.Errorf("event store not initialized")
	}
	return store.QueryGrouped(ctx, opts)
}

// Subscribe registers a channel to receive new timeline events.
// The caller is responsible for reading from the channel to avoid blocking.
// Returns a function to unsubscribe.
func Subscribe() (chan TimelineEvent, func()) {
	ch := make(chan TimelineEvent, 100)
	subscribersMu.Lock()
	subscribers = append(subscribers, ch)
	subscribersMu.Unlock()

	unsubscribe := func() {
		subscribersMu.Lock()
		defer subscribersMu.Unlock()
		for i, sub := range subscribers {
			if sub == ch {
				subscribers = append(subscribers[:i], subscribers[i+1:]...)
				close(ch)
				break
			}
		}
	}

	return ch, unsubscribe
}

// broadcastEvent sends an event to all subscribers (non-blocking)
func broadcastEvent(event TimelineEvent) {
	subscribersMu.RLock()
	defer subscribersMu.RUnlock()

	for _, ch := range subscribers {
		select {
		case ch <- event:
		default:
			// Channel full, skip (subscriber not keeping up)
			RecordDrop(event.Kind, event.Namespace, event.Name,
				DropReasonSubscriberFull, string(event.EventType))
		}
	}
}

// RecordEventWithBroadcast records an event and broadcasts it to subscribers
func RecordEventWithBroadcast(ctx context.Context, event TimelineEvent) error {
	store := GetStore()
	if store == nil {
		return fmt.Errorf("event store not initialized")
	}
	if err := store.Append(ctx, event); err != nil {
		return err
	}
	broadcastEvent(event)
	return nil
}

// RecordEventsWithBroadcast records multiple events and broadcasts them to subscribers
func RecordEventsWithBroadcast(ctx context.Context, events []TimelineEvent) error {
	store := GetStore()
	if store == nil {
		return fmt.Errorf("event store not initialized")
	}
	if err := store.AppendBatch(ctx, events); err != nil {
		return err
	}
	for _, event := range events {
		broadcastEvent(event)
	}
	return nil
}
