//go:build windows

package ai

import (
	"os/exec"
	"strconv"

	"golang.org/x/sys/windows"
)

const windowsStillActive = 259

func configureProcessLifecycle(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}

		// taskkill /T is the Windows equivalent of killing a Unix process group:
		// it terminates the agent and every child process it spawned. Fall back to
		// killing the direct process if taskkill is unavailable or loses a race
		// with normal process exit.
		killTree := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
		if err := killTree.Run(); err != nil {
			_ = cmd.Process.Kill()
		}
		return nil
	}
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}

	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		// Access denied still proves that the process exists.
		return err == windows.ERROR_ACCESS_DENIED
	}
	defer windows.CloseHandle(handle)

	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return false
	}
	return exitCode == windowsStillActive
}
