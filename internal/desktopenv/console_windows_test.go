//go:build windows

package desktopenv

import (
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

func TestHideConsoleWindowKeepsExistingFlags(t *testing.T) {
	cmd := exec.Command("cmd", "/C", "exit")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NEW_PROCESS_GROUP}
	HideConsoleWindow(cmd)
	if !cmd.SysProcAttr.HideWindow {
		t.Fatal("HideWindow not set")
	}
	want := uint32(windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW)
	if cmd.SysProcAttr.CreationFlags != want {
		t.Fatalf("CreationFlags = %#x, want %#x", cmd.SysProcAttr.CreationFlags, want)
	}
}
