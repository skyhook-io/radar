package memlimit

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// limitFromFS returns the tightest memory limit enforced on this process, or 0
// when no per-container cap is readable.
//
// Under a private cgroup namespace (the Kubernetes default on cgroup v2) the
// process sits at "/" and the mount root carries its limit. Under a host
// namespace or a systemd scope it sits in a nested path whose root has no
// limit at all, so the walk starts at the process's own cgroup and takes the
// minimum of every finite limit up to the root — an ancestor's cap is enforced
// just the same. A value above the machine's MemTotal is the host limit
// leaking through, not a cap; equal to MemTotal is a pod sized to the whole
// node and is kept.
func limitFromFS(mount, selfCgroup string, memTotal int64) int64 {
	v2Path, v1Path := parseSelfCgroup(selfCgroup)

	limit := int64(math.MaxInt64)
	if v2Path != "" {
		limit = minAlongPath(filepath.Join(mount, v2Path), mount, "memory.max", parseV2Limit, limit)
	}
	if v1Path != "" {
		limit = minAlongPath(filepath.Join(mount, "memory", v1Path), filepath.Join(mount, "memory"), "memory.limit_in_bytes", parseV1Limit, limit)
	}
	if limit == math.MaxInt64 {
		return 0
	}
	if memTotal > 0 && limit > memTotal {
		return 0
	}
	return limit
}

// parseSelfCgroup returns the process's cgroup v2 path (the "0::" entry) and its
// v1 memory-controller path, either of which may be empty.
func parseSelfCgroup(content string) (v2, v1 string) {
	for _, line := range strings.Split(content, "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		switch {
		case parts[0] == "0" && parts[1] == "":
			v2 = parts[2]
		case containsController(parts[1], "memory"):
			v1 = parts[2]
		}
	}
	return v2, v1
}

func containsController(list, name string) bool {
	for _, c := range strings.Split(list, ",") {
		if c == name {
			return true
		}
	}
	return false
}

// minAlongPath reads file at every directory from start up to and including
// stop, folding each finite value into limit.
func minAlongPath(start, stop, file string, parse func(string) (int64, bool), limit int64) int64 {
	start, stop = filepath.Clean(start), filepath.Clean(stop)
	for dir := start; ; dir = filepath.Dir(dir) {
		if b, err := os.ReadFile(filepath.Join(dir, file)); err == nil {
			if v, ok := parse(strings.TrimSpace(string(b))); ok && v < limit {
				limit = v
			}
		}
		if dir == stop || !strings.HasPrefix(dir, stop) || dir == filepath.Dir(dir) {
			return limit
		}
	}
}

func parseV2Limit(s string) (int64, bool) {
	if s == "max" {
		return 0, false
	}
	v, err := strconv.ParseInt(s, 10, 64)
	return v, err == nil && v > 0
}

// cgroup v1 reports "unlimited" as a near-MaxInt64 page-rounded sentinel.
func parseV1Limit(s string) (int64, bool) {
	v, err := strconv.ParseInt(s, 10, 64)
	return v, err == nil && v > 0 && v < int64(1)<<62
}

func memTotalBytes() int64 {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			if kb, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
				return kb * 1024
			}
		}
	}
	return 0
}
