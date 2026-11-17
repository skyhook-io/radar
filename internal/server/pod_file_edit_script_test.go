//go:build linux

package server

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func runEditScript(t *testing.T, dir, name, expect string, body []byte) int {
	t.Helper()
	cmd := podFileWriteCommand(dir, name, true, int64(len(body)), expect)
	c := exec.Command(cmd[0], cmd[1:]...)
	c.Stdin = bytes.NewReader(body)
	err := c.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	if err != nil {
		t.Fatalf("run script: %v", err)
	}
	return 0
}

func TestEditScriptCommitsWhenTheHashStillMatches(t *testing.T) {
	dir := realTempDir(t)
	dest := filepath.Join(dir, "a")
	_ = os.WriteFile(dest, []byte("old\n"), 0o640)
	if code := runEditScript(t, dir, "a", sha256Hex([]byte("old\n")), []byte("new\n")); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got, _ := os.ReadFile(dest); string(got) != "new\n" {
		t.Errorf("content %q", got)
	}
	if st, _ := os.Stat(dest); st.Mode().Perm() != 0o640 {
		t.Errorf("mode %v, want 0640", st.Mode().Perm())
	}
	assertNoTempFiles(t, dir)
}

// The container's own check: the file changed after the server's re-read.
func TestEditScriptRefusesWhenTheFileChanged(t *testing.T) {
	dir := realTempDir(t)
	dest := filepath.Join(dir, "a")
	_ = os.WriteFile(dest, []byte("someone else's\n"), 0o644)
	if code := runEditScript(t, dir, "a", sha256Hex([]byte("old\n")), []byte("new\n")); code != uploadExitChanged {
		t.Errorf("exit %d, want %d", code, uploadExitChanged)
	}
	if got, _ := os.ReadFile(dest); string(got) != "someone else's\n" {
		t.Errorf("destination changed to %q", got)
	}
	assertNoTempFiles(t, dir)
}

// A file removed since it was opened is not recreated by a save.
func TestEditScriptRefusesWhenTheFileWasRemoved(t *testing.T) {
	dir := realTempDir(t)
	if code := runEditScript(t, dir, "gone", sha256Hex([]byte("old\n")), []byte("new\n")); code != uploadExitChanged {
		t.Errorf("exit %d, want %d", code, uploadExitChanged)
	}
	if _, err := os.Lstat(filepath.Join(dir, "gone")); !os.IsNotExist(err) {
		t.Error("the save created the file")
	}
	assertNoTempFiles(t, dir)
}
