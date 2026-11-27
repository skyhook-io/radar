package vulnerabilities

import (
	"sync/atomic"
	"time"

	"github.com/skyhook-io/radar/pkg/vulnintel"
)

// intelStore holds CISA KEV and FIRST EPSS for the process. Unconfigured
// (tests, Radar Hub importing the package) the rows carry neither.
var intelStore atomic.Pointer[vulnintel.Store]

// ConfigureIntel sets where the feeds come from. Nothing is fetched until a
// read asks for a snapshot.
func ConfigureIntel(cfg vulnintel.Config) {
	if old := intelStore.Swap(vulnintel.NewStore(cfg)); old != nil {
		old.Close()
	}
}

// RefreshIntel asks for an immediate reload of both feeds, at most once per
// vulnintel.ManualRefreshInterval for the process (vulnintel.ErrRefreshTooSoon
// with how long to wait). Unconfigured, or with both feeds off, it is
// vulnintel.ErrFeedsOff.
func RefreshIntel() (time.Duration, error) {
	s := intelStore.Load()
	if s == nil {
		return 0, vulnintel.ErrFeedsOff
	}
	return s.RefreshNow()
}

// IntelSnapshot is the feeds as loaded so far, nil when unconfigured. The
// first call starts loading them; until a load finishes their state is
// loading.
func IntelSnapshot() *vulnintel.Snapshot {
	s := intelStore.Load()
	if s == nil {
		return nil
	}
	snap := s.Snapshot()
	return &snap
}

// IntelStatus is the feeds' state for the wire.
func IntelStatus() *Intel {
	return intelOf(IntelSnapshot())
}
