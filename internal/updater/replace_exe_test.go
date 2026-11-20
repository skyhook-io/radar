package updater

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupSwap(t *testing.T) (exe, newExe string) {
	t.Helper()
	dir := t.TempDir()
	exe = filepath.Join(dir, "Radar.exe")
	newExe = filepath.Join(t.TempDir(), "Radar.exe")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newExe, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	attempts, backoff := renameAttempts, renameBackoff
	renameAttempts, renameBackoff = 3, 0
	t.Cleanup(func() { renameAttempts, renameBackoff, rename = attempts, backoff, os.Rename })
	return exe, newExe
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func assertMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s should not exist (stat err %v)", path, err)
	}
}

func TestReplaceExecutableSwapsAndKeepsTheRunningOneAside(t *testing.T) {
	exe, newExe := setupSwap(t)
	if err := os.WriteFile(exe+".old", []byte("older"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := replaceExecutable(exe, newExe); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, exe); got != "new" {
		t.Fatalf("exe = %q, want the new version", got)
	}
	if got := readFile(t, exe+".old"); got != "old" {
		t.Fatalf(".old = %q, want the version that was running", got)
	}
	assertMissing(t, exe+".new")
}

func TestReplaceExecutableRestoresTheRunningVersionWhenTheNewOneCantMoveIn(t *testing.T) {
	exe, newExe := setupSwap(t)
	rename = func(from, to string) error {
		if strings.HasSuffix(from, ".new") {
			return errors.New("sharing violation")
		}
		return os.Rename(from, to)
	}
	err := replaceExecutable(exe, newExe)
	if err == nil || !strings.Contains(err.Error(), "current version kept") {
		t.Fatalf("err = %v, want a failure that says the current version was kept", err)
	}
	if got := readFile(t, exe); got != "old" {
		t.Fatalf("exe = %q, want the running version back", got)
	}
	assertMissing(t, exe+".old")
	assertMissing(t, exe+".new")
}

func TestReplaceExecutableLeavesEverythingWhenTheRunningOneCantMove(t *testing.T) {
	exe, newExe := setupSwap(t)
	rename = func(string, string) error { return errors.New("access denied") }
	if err := replaceExecutable(exe, newExe); err == nil {
		t.Fatal("want an error")
	}
	if got := readFile(t, exe); got != "old" {
		t.Fatalf("exe = %q, want it untouched", got)
	}
	assertMissing(t, exe+".new")
}

func TestReplaceExecutableRetriesABrieflyLockedFile(t *testing.T) {
	exe, newExe := setupSwap(t)
	failures := 2
	rename = func(from, to string) error {
		if strings.HasSuffix(from, ".new") && failures > 0 {
			failures--
			return errors.New("sharing violation")
		}
		return os.Rename(from, to)
	}
	if err := replaceExecutable(exe, newExe); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, exe); got != "new" {
		t.Fatalf("exe = %q, want the new version", got)
	}
}
