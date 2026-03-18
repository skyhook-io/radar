//go:build darwin

package version

import "syscall"

func dirBirthtime(path string) int64 {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return 0
	}
	return st.Birthtimespec.Sec
}
