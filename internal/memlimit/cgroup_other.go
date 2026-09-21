//go:build !linux

package memlimit

func cgroupLimitBytes() int64 { return 0 }
