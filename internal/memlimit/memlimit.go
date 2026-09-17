// Package memlimit sizes the Go GC to the container's memory limit.
//
// The Go runtime honors GOMEMLIMIT only when told about it; it never reads the
// cgroup on its own, so a process under a Kubernetes memory limit otherwise
// runs at GOGC=100 with no idea a wall exists and can be OOMKilled while the
// collector still has plenty of garbage to reclaim.
package memlimit

import (
	"context"
	"log"
	"math"
	"runtime/debug"
	"time"
)

const (
	// headroom is the fraction of the cgroup limit handed to GOMEMLIMIT. The
	// limit governs Go-managed memory only (heap, stacks, runtime structures),
	// not total RSS, and the GC death-spirals when pinned right at the OOM
	// boundary.
	headroom = 0.75
	// refreshInterval bounds how long an in-place pod resize goes unnoticed.
	refreshInterval = 30 * time.Second
)

// Apply sets GOMEMLIMIT from the cgroup memory limit when one is readable and
// keeps it in sync with in-place changes for the life of ctx. An explicit
// GOMEMLIMIT env var wins, and a process outside a cgroup limit (a laptop, a
// pod with no limit set) is left alone: there is no sensible default that is
// right for both a 16Gi workstation and a 256Mi sidecar.
func Apply(ctx context.Context) {
	if debug.SetMemoryLimit(-1) != math.MaxInt64 {
		return
	}
	target, ok := fromCgroup()
	if !ok {
		return
	}
	debug.SetMemoryLimit(target)
	log.Printf("[memlimit] GOMEMLIMIT set to %dMiB from cgroup memory limit", target>>20)
	go refresh(ctx, target)
}

func fromCgroup() (int64, bool) {
	limit := cgroupLimitBytes()
	if limit <= 0 {
		return 0, false
	}
	return int64(float64(limit) * headroom), true
}

// refresh only acts on a successful cgroup read, so a transient read failure
// keeps the current value rather than flapping.
func refresh(ctx context.Context, current int64) {
	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			desired, ok := fromCgroup()
			if !ok || desired == current {
				continue
			}
			debug.SetMemoryLimit(desired)
			log.Printf("[memlimit] GOMEMLIMIT updated %dMiB -> %dMiB after cgroup limit change", current>>20, desired>>20)
			current = desired
		}
	}
}
