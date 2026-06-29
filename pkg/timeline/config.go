package timeline

import "time"

// StoreType identifies the storage backend
type StoreType string

const (
	StoreTypeMemory StoreType = "memory"
	StoreTypeSQLite StoreType = "sqlite"
)

// StoreConfig holds configuration for the event store
type StoreConfig struct {
	Type            StoreType
	Path            string        // For SQLite: database file path
	MaxSize         int           // For Memory: ring buffer size
	RetentionAge    time.Duration // For SQLite: delete events older than this; 0 = never
	MaxStorageBytes int64         // For SQLite: prune oldest events to stay under this size; 0 = never
}

// DefaultStoreConfig returns sensible defaults
func DefaultStoreConfig() StoreConfig {
	return StoreConfig{
		Type:    StoreTypeMemory,
		MaxSize: 1000,
	}
}
