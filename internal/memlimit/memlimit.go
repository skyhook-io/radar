// Package memlimit sizes the Go GC to the container's memory limit.
//
// The Go runtime honors GOMEMLIMIT only when told about it; it never reads the
// cgroup on its own, so a process under a Kubernetes memory limit otherwise
// runs at GOGC=100 with no idea a wall exists and can be OOMKilled while the
// collector still has plenty of garbage to reclaim.
package memlimit

import (
	"log"
	"math"
	"os"
	"runtime/debug"
)

// headroom is the fraction of the cgroup limit handed to GOMEMLIMIT. The limit
// governs Go-managed memory only (heap, stacks, runtime structures); the
// binary's mapped pages, SQLite's own allocator and page cache also count
// against the cgroup, and the GC death-spirals when pinned right at the OOM
// boundary.
const headroom = 0.85

// Apply sets GOMEMLIMIT once, from the cgroup memory limit in force at startup.
// An explicit GOMEMLIMIT env var wins — including GOMEMLIMIT=off, which the
// runtime reports the same as unset — and a process outside a cgroup limit (a
// laptop, a pod with no limit set) is left alone: there is no sensible default
// that is right for both a 16Gi workstation and a 256Mi sidecar. A limit
// changed in place after startup is not followed; a resources change rolls new
// pods anyway.
func Apply() {
	if os.Getenv("GOMEMLIMIT") != "" || debug.SetMemoryLimit(-1) != math.MaxInt64 {
		return
	}
	limit := cgroupLimitBytes()
	if limit <= 0 {
		return
	}
	target := int64(float64(limit) * headroom)
	debug.SetMemoryLimit(target)
	log.Printf("[memlimit] GOMEMLIMIT set to %dMiB from cgroup memory limit", target>>20)
}
