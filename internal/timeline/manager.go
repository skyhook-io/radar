package timeline

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
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

	// Tombstone cache types
	TombstoneCache = pkgtimeline.TombstoneCache
	TombstoneEntry = pkgtimeline.TombstoneEntry
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

	// Reason constants
	ReasonRecreated = pkgtimeline.ReasonRecreated

	// HealthState constants
	HealthHealthy   = pkgtimeline.HealthHealthy
	HealthDegraded  = pkgtimeline.HealthDegraded
	HealthUnhealthy = pkgtimeline.HealthUnhealthy
	HealthNeutral   = pkgtimeline.HealthNeutral
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
func SeenResourceKey(clusterContext, kind, namespace, name string) string {
	return pkgtimeline.SeenResourceKey(clusterContext, kind, namespace, name)
}

// Converter functions.
func NewInformerEvent(kind, apiVersion, namespace, name, uid, resourceVersion string, operation EventType, healthState HealthState, diff *DiffInfo, owner *OwnerInfo, labels map[string]string, createdAt *time.Time) TimelineEvent {
	return pkgtimeline.NewInformerEvent(kind, apiVersion, namespace, name, uid, resourceVersion, operation, healthState, diff, owner, labels, createdAt)
}
func NewK8sEventTimelineEvent(event *corev1.Event, owner *OwnerInfo) TimelineEvent {
	return pkgtimeline.NewK8sEventTimelineEvent(event, owner)
}
func NewHistoricalEvent(clusterContext, kind, apiVersion, namespace, name string, ts time.Time, reason, message string, healthState HealthState, owner *OwnerInfo, labels map[string]string) TimelineEvent {
	return pkgtimeline.NewHistoricalEvent(clusterContext, kind, apiVersion, namespace, name, ts, reason, message, healthState, owner, labels)
}
func ExtractOwner(obj any) *OwnerInfo         { return pkgtimeline.ExtractOwner(obj) }
func ExtractLabels(obj any) map[string]string { return pkgtimeline.ExtractLabels(obj) }
func ExtractTombstoneEntry(obj any) (TombstoneEntry, bool) {
	return pkgtimeline.ExtractTombstoneEntry(obj)
}
func NewTombstoneCache(ttl time.Duration, capacity int) *TombstoneCache {
	return pkgtimeline.NewTombstoneCache(ttl, capacity)
}
func OperationToEventType(op string) EventType  { return pkgtimeline.OperationToEventType(op) }
func EventTypeToOperation(et EventType) string  { return pkgtimeline.EventTypeToOperation(et) }
func HealthStateToString(hs HealthState) string { return pkgtimeline.HealthStateToString(hs) }
func StringToHealthState(s string) HealthState  { return pkgtimeline.StringToHealthState(s) }
func ToLegacyDiffInfo(d *DiffInfo) *DiffInfo    { return pkgtimeline.ToLegacyDiffInfo(d) }

// Store constructors.
func NewMemoryStore(maxSize int) *pkgtimeline.MemoryStore { return pkgtimeline.NewMemoryStore(maxSize) }
func NewDegradedMemoryStore(maxSize int, reason string) *pkgtimeline.MemoryStore {
	return pkgtimeline.NewDegradedMemoryStore(maxSize, reason)
}

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
			store, err := openSQLiteWithRecovery(cfg.Path)
			if err != nil {
				// A transient open failure, or a corrupt file whose
				// quarantine-and-recreate also failed. Degrade to an in-memory
				// store so the timeline stays alive (without persistence) for
				// this session instead of the whole subsystem going dark.
				// initErr stays nil: the caller must not treat this as a fatal
				// init failure.
				maxSize := cfg.MaxSize
				if maxSize <= 0 {
					maxSize = 1000
				}
				reason := fmt.Sprintf("SQLite store at %s unusable: %v", cfg.Path, err)
				log.Printf("[timeline] %s; degrading to in-memory for this session", reason)
				setGlobalStore(NewDegradedMemoryStore(maxSize, reason))
				break
			}
			setGlobalStore(store)
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
			setGlobalStore(NewMemoryStore(maxSize))
			log.Printf("Initialized in-memory event store (max %d events)", maxSize)
		}
		observationStartNanos.Store(time.Now().UnixNano())
	})
	return initErr
}

// isCorruptedSQLiteError reports whether an open/schema failure signals actual
// on-disk corruption — the only case where quarantining and recreating the file
// is right. A transient or environmental failure (locked, permission denied,
// disk full) must NOT quarantine, or a healthy database gets moved aside and its
// history is lost. modernc.org/sqlite surfaces the SQLite result text, so match
// the corruption markers.
func isCorruptedSQLiteError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{
		"malformed",         // SQLITE_CORRUPT: "database disk image is malformed"
		"not a database",    // SQLITE_NOTADB: "file is not a database"
		"file is encrypted", // SQLITE_NOTADB variant
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// openSQLiteWithRecovery opens the SQLite store. Only an open failure that
// isCorruptedSQLiteError classifies as on-disk corruption quarantines the file
// (to a fixed ".corrupt" name, once) and recreates it; every other failure —
// and a failed quarantine rename — surfaces the original open error unchanged
// so the caller degrades to memory and the untouched file is retried next
// start.
func openSQLiteWithRecovery(path string) (*SQLiteStore, error) {
	store, err := NewSQLiteStore(path)
	if err == nil {
		return store, nil
	}
	// Only a corrupt file earns a quarantine. A transient/environmental failure
	// (locked, permission, full disk) surfaces as-is so the caller degrades to
	// memory for this session; the file stays put and is retried untouched next
	// start rather than being moved aside and its history lost.
	if !isCorruptedSQLiteError(err) {
		return nil, err
	}

	quarantine := path + ".corrupt"
	if renameErr := os.Rename(path, quarantine); renameErr != nil {
		// Nothing to move aside (or the move failed) — can't recreate cleanly.
		return nil, err
	}
	// Move the WAL/SHM sidecars aside too; a stale WAL left next to a fresh main
	// file would be replayed into it and could re-corrupt it. Best-effort — they
	// may not exist.
	_ = os.Rename(path+"-wal", quarantine+"-wal")
	_ = os.Rename(path+"-shm", quarantine+"-shm")
	log.Printf("[timeline] quarantined unreadable SQLite store %s -> %s (open error: %v)", path, quarantine, err)

	return NewSQLiteStore(path)
}

// observationStartNanos (unix nanos; 0 = no store) is when THIS process began
// recording events. Claims of the form "no changes in the last N seconds"
// must clamp to it: after a restart the store (in-memory always; SQLite
// during the downtime gap) has not been watching for the full window, and
// asserting a longer one would be a false statement. Atomic because it is
// written on context switch (ResetStore) while concurrent MCP request
// goroutines read it.
var observationStartNanos atomic.Int64

// ObservationStart returns when this process's store began observing, or the
// zero time when no store is initialized.
func ObservationStart() time.Time {
	nanos := observationStartNanos.Load()
	if nanos == 0 {
		return time.Time{}
	}
	return time.Unix(0, nanos)
}

// SetObservationStartForTest backdates the observation window so tests can
// exercise claims that require a longer watch period.
func SetObservationStartForTest(t time.Time) {
	if t.IsZero() {
		observationStartNanos.Store(0)
		return
	}
	observationStartNanos.Store(t.UnixNano())
}

// GetStore returns the global event store instance.
func GetStore() EventStore {
	globalStoreMu.Lock()
	defer globalStoreMu.Unlock()
	return globalStore
}

// setGlobalStore assigns the global store under the same lock GetStore/ResetStore
// use. The InitStore write and concurrent GetStore reads (informer callbacks on
// other goroutines) would otherwise race on the bare global.
func setGlobalStore(s EventStore) {
	globalStoreMu.Lock()
	globalStore = s
	globalStoreMu.Unlock()
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
	observationStartNanos.Store(0)
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
