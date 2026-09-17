package memlimit

import (
	"os"
	"strconv"
	"strings"
)

// cgroupLimitBytes returns the container's memory limit, or 0 when there is no
// real per-container cap. It reads cgroup v2 (memory.max) then v1
// (memory.limit_in_bytes). A value above the machine's MemTotal is the host
// limit leaking through or an unlimited sentinel, not a cap; a value equal to
// MemTotal is a pod sized to the whole node and is kept.
func cgroupLimitBytes() int64 {
	const unlimitedV1 = int64(1) << 62

	var limit int64
	if b, err := os.ReadFile("/sys/fs/cgroup/memory.max"); err == nil {
		s := strings.TrimSpace(string(b))
		if s == "max" {
			return 0
		}
		if v, err := strconv.ParseInt(s, 10, 64); err == nil {
			limit = v
		}
	}
	if limit == 0 {
		if b, err := os.ReadFile("/sys/fs/cgroup/memory/memory.limit_in_bytes"); err == nil {
			if v, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); err == nil && v < unlimitedV1 {
				limit = v
			}
		}
	}
	if limit <= 0 {
		return 0
	}
	if total := memTotalBytes(); total > 0 && limit > total {
		return 0
	}
	return limit
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
