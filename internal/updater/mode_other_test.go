//go:build !linux

package updater

import "testing"

func TestMacOSAndWindowsKeepTheInAppUpdater(t *testing.T) {
	if got := DetectMode(); got != ModeInPlace {
		t.Fatalf("DetectMode() = %q, want the in-app updater", got)
	}
}
