//go:build linux

package version

import "golang.org/x/sys/unix"

func dirBirthtime(path string) int64 {
	var sx unix.Statx_t
	if err := unix.Statx(unix.AT_FDCWD, path, 0, unix.STATX_BTIME, &sx); err != nil {
		return 0
	}
	if sx.Mask&unix.STATX_BTIME == 0 {
		return 0
	}
	return int64(sx.Btime.Sec)
}
