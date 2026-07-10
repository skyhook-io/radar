package timeline

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/skyhook-io/radar/pkg/timeline/storetest"
)

// The shared EventStore contract lives in pkg/timeline/storetest and runs here
// against SQLiteStore; MemoryStore runs the same suite from the pkg module.
// Only SQLite-specific behavior (migrations, quarantine, retention, timestamp
// encoding) is tested in this package's own files.
func TestStoreConformance_SQLite(t *testing.T) {
	storetest.RunConformance(t, func(t *testing.T) EventStore {
		t.Helper()
		dir, err := os.MkdirTemp("", "timeline-conformance-*")
		if err != nil {
			t.Fatalf("MkdirTemp: %v", err)
		}
		t.Cleanup(func() { os.RemoveAll(dir) })
		store, err := NewSQLiteStore(filepath.Join(dir, "test.db"))
		if err != nil {
			t.Fatalf("NewSQLiteStore: %v", err)
		}
		t.Cleanup(func() { store.Close() })
		return store
	})
}
