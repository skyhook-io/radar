//go:build windows

package version

import (
	"os"
	"syscall"
)

func dirBirthtime(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	d, ok := fi.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		return 0
	}
	return d.CreationTime.Nanoseconds() / 1_000_000_000
}
