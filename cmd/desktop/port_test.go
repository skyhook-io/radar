package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDesktopPortRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".radar", desktopPortFile)
	if got := lastDesktopPort(path); got != 0 {
		t.Fatalf("lastDesktopPort before any launch = %d, want 0", got)
	}
	recordDesktopPort(path, 54321)
	if got := lastDesktopPort(path); got != 54321 {
		t.Fatalf("lastDesktopPort = %d, want 54321", got)
	}
	recordDesktopPort(path, 60000)
	if got := lastDesktopPort(path); got != 60000 {
		t.Fatalf("lastDesktopPort after a new port = %d, want 60000", got)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "*.tmp")); len(leftovers) != 0 {
		t.Fatalf("temp files left behind: %v", leftovers)
	}
}

func TestLastDesktopPortIgnoresGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), desktopPortFile)
	for _, content := range []string{"", "abc", "0", "-1", "70000"} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := lastDesktopPort(path); got != 0 {
			t.Errorf("lastDesktopPort(%q) = %d, want 0", content, got)
		}
	}
}
