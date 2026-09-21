package memlimit

import "os"

const cgroupMount = "/sys/fs/cgroup"

func cgroupLimitBytes() int64 {
	self, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return 0
	}
	return limitFromFS(cgroupMount, string(self), memTotalBytes())
}
