//go:build windows

package main

import (
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/skyhook-io/radar/internal/updater"
)

// One Radar per Windows session. A second launch brings the running window to
// the front and exits before it starts anything; otherwise it would load
// kubeconfig, serve on another port (where the webview's storage is empty) and
// point MCP clients at itself. Wails' own lock is taken inside wails.Run,
// after all of that.
//
// The second launch focuses the window itself rather than asking the running
// Radar to: Windows lets the foreground process, which it is, hand the
// foreground to another window.
const instanceMutexName = `Local\RadarDesktop`

// instanceMutex is held for the life of the process; Windows releases it when
// the process exits.
var instanceMutex windows.Handle

const predecessorWait = 30 * time.Second

var (
	user32                  = windows.NewLazySystemDLL("user32.dll")
	procIsIconic            = user32.NewProc("IsIconic")
	procShowWindow          = user32.NewProc("ShowWindow")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procGetWindow           = user32.NewProc("GetWindow")
)

const (
	swRestore = 9
	gwOwner   = 4
)

// handOffToRunningInstance reports whether another Radar is running, after
// bringing its window to the front.
func handOffToRunningInstance() bool {
	waitForUpdatedPredecessor()
	h, err := windows.CreateMutex(nil, false, windows.StringToUTF16Ptr(instanceMutexName))
	if err == nil {
		instanceMutex = h
		writeInstancePID()
		return false
	}
	if err != windows.ERROR_ALREADY_EXISTS {
		log.Printf("Single-instance check failed, starting anyway: %v", err)
		return false
	}
	windows.CloseHandle(h)
	log.Printf("Radar is already running; bringing its window to the front")
	if pid := readInstancePID(); pid != 0 {
		focusProcessWindow(pid)
	}
	return true
}

// waitForUpdatedPredecessor lets the version being replaced by an update exit
// first, releasing the lock and the port.
func waitForUpdatedPredecessor() {
	v := os.Getenv(updater.RelaunchAfterPIDEnv)
	if v == "" {
		return
	}
	os.Unsetenv(updater.RelaunchAfterPIDEnv)
	pid, err := strconv.ParseUint(v, 10, 32)
	if err != nil {
		return
	}
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return // already gone
	}
	defer windows.CloseHandle(h)
	if ev, _ := windows.WaitForSingleObject(h, uint32(predecessorWait.Milliseconds())); ev == uint32(windows.WAIT_TIMEOUT) {
		log.Printf("Previous Radar (pid %d) still running after %s; starting anyway", pid, predecessorWait)
	}
}

func instancePIDPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".radar", "desktop.pid")
}

func writeInstancePID() {
	path := instancePIDPath()
	if path == "" {
		return
	}
	err := os.MkdirAll(filepath.Dir(path), 0o755)
	if err == nil {
		err = os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o644)
	}
	if err != nil {
		log.Printf("Failed to record the desktop PID: %v", err)
	}
}

// readInstancePID is 0 when the running Radar hasn't written its PID yet; its
// window is then still to appear, in front.
func readInstancePID() uint32 {
	path := instancePIDPath()
	if path == "" {
		return 0
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 32)
	if err != nil {
		return 0
	}
	return uint32(pid)
}

// focusProcessWindow restores and raises pid's main window: its visible
// top-level window that no other window owns.
func focusProcessWindow(pid uint32) {
	var found windows.HWND
	cb := windows.NewCallback(func(h, _ uintptr) uintptr {
		hwnd := windows.HWND(h)
		var owner uint32
		if _, err := windows.GetWindowThreadProcessId(hwnd, &owner); err != nil || owner != pid {
			return 1
		}
		if !windows.IsWindowVisible(hwnd) {
			return 1
		}
		if o, _, _ := procGetWindow.Call(h, gwOwner); o != 0 {
			return 1
		}
		found = hwnd
		return 0
	})
	_ = windows.EnumWindows(cb, unsafe.Pointer(nil))
	if found == 0 {
		return
	}
	if iconic, _, _ := procIsIconic.Call(uintptr(found)); iconic != 0 {
		procShowWindow.Call(uintptr(found), swRestore)
	}
	procSetForegroundWindow.Call(uintptr(found))
}
