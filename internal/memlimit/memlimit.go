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
	"os"
	"runtime/debug"
	"time"
)

const (
	// headroom is the fraction of the cgroup limit handed to GOMEMLIMIT. The
	// limit governs Go-managed memory only (heap, stacks, runtime structures);
	// the binary's mapped pages, SQLite's own allocator and page cache also
	// count against the cgroup, and the GC death-spirals when pinned right at
	// the OOM boundary.
	headroom = 0.85
	// refreshInterval bounds how long an in-place pod resize goes unnoticed.
	refreshInterval = 30 * time.Second
)

// Apply sets GOMEMLIMIT from the cgroup memory limit when one is readable and
// keeps it in sync with in-place changes for the life of ctx — a limit that
// appears, changes or disappears after startup is followed. An explicit
// GOMEMLIMIT env var wins — including GOMEMLIMIT=off, which the runtime reports
// the same as unset — and a process outside a cgroup limit (a laptop, a pod
// with no limit set) is left alone: there is no sensible default that is right
// for both a 16Gi workstation and a 256Mi sidecar.
func Apply(ctx context.Context) {
	if os.Getenv("GOMEMLIMIT") != "" || debug.SetMemoryLimit(-1) != math.MaxInt64 {
		return
	}
	current := int64(math.MaxInt64)
	if target, ok := fromCgroup(); ok {
		debug.SetMemoryLimit(target)
		log.Printf("[memlimit] GOMEMLIMIT set to %dMiB from cgroup memory limit", target>>20)
		current = target
	}
	go refresh(ctx, current)
}

func fromCgroup() (int64, bool) {
	limit := cgroupLimitBytes()
	if limit <= 0 {
		return 0, false
	}
	return int64(float64(limit) * headroom), true
}

// refresh follows the cgroup limit, including its removal: a limit that has
// gone away hands the runtime back to its default rather than pinning the GC
// to a cap nobody enforces any more.
func refresh(ctx context.Context, current int64) {
	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			desired, ok := fromCgroup()
			if !ok {
				desired = math.MaxInt64
			}
			if desired == current {
				continue
			}
			debug.SetMemoryLimit(desired)
			switch {
			case !ok:
				log.Printf("[memlimit] GOMEMLIMIT cleared: cgroup memory limit removed")
			case current == math.MaxInt64:
				log.Printf("[memlimit] GOMEMLIMIT set to %dMiB: cgroup memory limit added", desired>>20)
			default:
				log.Printf("[memlimit] GOMEMLIMIT updated %dMiB -> %dMiB after cgroup limit change", current>>20, desired>>20)
			}
			current = desired
		}
	}
}
