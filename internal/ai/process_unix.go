//go:build !windows

package ai

import (
	"errors"
	"os/exec"
	"syscall"
)

func configureProcessLifecycle(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
}

// pidAlive reports whether a process exists (signal 0; EPERM still means
// alive). PID reuse is theoretically possible but irrelevant at this scale: a
// false "alive" only delays crash repair until the next boot.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
