//go:build linux

package updater

import (
	"os"
	"path/filepath"
	"testing"
)

// install lays out <root>/<prefix>/bin/radar-desktop, plus the package marker
// under <root>/<prefix>/share when packaged, and returns the binary's path.
func install(t *testing.T, root, prefix string, packaged bool) string {
	t.Helper()
	bin := filepath.Join(root, prefix, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(bin, "radar-desktop")
	if err := os.WriteFile(exe, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if packaged {
		share := filepath.Join(root, prefix, "share", "radar-desktop")
		if err := os.MkdirAll(share, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(share, packagedMarker), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return exe
}

func TestModeForWritableFolderUpdatesInPlace(t *testing.T) {
	exe := install(t, t.TempDir(), "apps", false)
	if mode, reason := modeFor(exe); mode != ModeInPlace {
		t.Fatalf("modeFor = %q (%s), want in place", mode, reason)
	}
	entries, err := os.ReadDir(filepath.Dir(exe))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("the writability check left files behind: %v", entries)
	}
}

// The swap renames the binary and creates a new file; it never writes to the
// old one, so a read-only binary in a writable folder still updates.
func TestModeForReadOnlyBinaryInWritableFolderUpdatesInPlace(t *testing.T) {
	exe := install(t, t.TempDir(), "apps", false)
	if err := os.Chmod(exe, 0o555); err != nil {
		t.Fatal(err)
	}
	if mode, reason := modeFor(exe); mode != ModeInPlace {
		t.Fatalf("modeFor = %q (%s), want in place", mode, reason)
	}
}

func TestModeForPackageInstallIsLeftToThePackageManager(t *testing.T) {
	// Writable here, as it is for root: the marker still wins.
	exe := install(t, t.TempDir(), "usr", true)
	if mode, _ := modeFor(exe); mode != ModePackage {
		t.Fatalf("modeFor = %q, want package", mode)
	}
}

func TestModeForBinaryOutsideThePackagePrefixIsNotPackaged(t *testing.T) {
	root := t.TempDir()
	install(t, root, "usr", true)
	exe := install(t, root, filepath.Join("usr", "local"), false)
	if mode, reason := modeFor(exe); mode != ModeInPlace {
		t.Fatalf("modeFor = %q (%s), want in place: the package marker belongs to another prefix", mode, reason)
	}
}

func TestModeForFolderRadarCannotWriteNeedsADownload(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write to any folder")
	}
	exe := install(t, t.TempDir(), "apps", false)
	dir := filepath.Dir(exe)
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	if mode, reason := modeFor(exe); mode != ModeDownload || reason == "" {
		t.Fatalf("modeFor = %q (%q), want download with a reason", mode, reason)
	}
}
