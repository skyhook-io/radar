package timeline

import (
	"context"
	"fmt"
	"log"
	"sync"
)

// StoreType identifies the storage backend
type StoreType string

const (
	StoreTypeMemory StoreType = "memory"
	StoreTypeSQLite StoreType = "sqlite"
)

// StoreConfig holds configuration for the event store
type StoreConfig struct {
	Type    StoreType
	Path    string // For SQLite: database file path
	MaxSize int    // For Memory: ring buffer size
}

// DefaultStoreConfig returns sensible defaults
func DefaultStoreConfig() StoreConfig {
	return StoreConfig{
		Type:    StoreTypeMemory,
		MaxSize: 1000,
	}
}

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
			log.Printf("Initialized SQLite event store at %s", cfg.Path)

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

// ResetStore stops and clears the event store
// This must be called before reinitializing when switching contexts
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

// ReinitStore reinitializes the event store after a context switch
// Must call ResetStore first
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
