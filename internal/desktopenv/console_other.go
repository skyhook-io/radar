//go:build !windows

package desktopenv

import "os/exec"

// HideConsoleWindow is a no-op off Windows, where child processes never open
// windows of their own.
func HideConsoleWindow(*exec.Cmd) {}
