package mcp

import (
	"os"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
)

// TestMain dates the test caches' typed watches two days back, before any
// window a test reads, as for a Radar that has been running. A test about a
// watch that started inside the window sets its own clock.
func TestMain(m *testing.M) {
	k8s.SetTestWatchClock(func() time.Time { return time.Now().Add(-48 * time.Hour) })
	os.Exit(m.Run())
}
