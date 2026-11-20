//go:build !linux && !windows

package updater

import "testing"

func TestMacOSKeepsTheInAppUpdater(t *testing.T) {
	if got := DetectMode(); got != ModeInPlace {
		t.Fatalf("DetectMode() = %q, want the in-app updater", got)
	}
}
