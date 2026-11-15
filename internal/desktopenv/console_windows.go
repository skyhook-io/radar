//go:build windows

package desktopenv

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// HideConsoleWindow starts a console program without a visible window. Radar
// Desktop is a GUI-subsystem process with no console to share, so Windows
// otherwise opens a new console window (or Windows Terminal tab) for every
// console child. CREATE_NO_WINDOW gives the child a hidden console rather than
// none, so its own console children inherit that instead of opening windows.
func HideConsoleWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= windows.CREATE_NO_WINDOW
}
