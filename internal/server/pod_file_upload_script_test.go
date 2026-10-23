//go:build linux

package server

import (
	"bytes"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	k8sexec "k8s.io/client-go/util/exec"
)

// ---------------------------------------------------------------------------
// The script itself, run by the local POSIX shell against a scratch directory.
// Linux only: the script assumes a Linux userland (GNU or BusyBox) and /proc.
// ---------------------------------------------------------------------------

func runUploadScript(t *testing.T, dir, name string, overwrite bool, declared int64, body []byte) (int, string, string) {
	t.Helper()
	cmd := podFileUploadCommand(dir, name, overwrite, declared)
	c := exec.Command(cmd[0], cmd[1:]...)
	c.Stdin = bytes.NewReader(body)
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	err := c.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), stdout.String(), stderr.String()
	}
	if err != nil {
		t.Fatalf("run script: %v", err)
	}
	return 0, stdout.String(), stderr.String()
}

// realTempDir is t.TempDir with symlinks resolved, since the script refuses a
// directory reached through one.
func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".radar-upload.") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestUploadScriptCreatesNewFile(t *testing.T) {
	dir := realTempDir(t)
	body := []byte("fresh\n")
	code, stdout, stderr := runUploadScript(t, dir, "-n.txt", false, int64(len(body)), body)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "-n.txt"))
	if !bytes.Equal(got, body) {
		t.Errorf("content %q", got)
	}
	if strings.Contains(stdout, "replaced") {
		t.Error("reported a replace for a new file")
	}
	st, _ := os.Stat(filepath.Join(dir, "-n.txt"))
	umask := syscall.Umask(0)
	syscall.Umask(umask)
	if want := os.FileMode(0o666 &^ umask); st.Mode().Perm() != want {
		t.Errorf("mode %v, want %v", st.Mode().Perm(), want)
	}
	assertNoTempFiles(t, dir)
}

func TestUploadScriptRefusesExistingWithoutOverwrite(t *testing.T) {
	dir := realTempDir(t)
	dest := filepath.Join(dir, "a")
	_ = os.WriteFile(dest, []byte("old"), 0o640)
	code, _, _ := runUploadScript(t, dir, "a", false, 3, []byte("new"))
	if code != uploadExitExists {
		t.Errorf("exit %d, want %d", code, uploadExitExists)
	}
	if got, _ := os.ReadFile(dest); string(got) != "old" {
		t.Errorf("destination changed to %q", got)
	}
	assertNoTempFiles(t, dir)
}

func TestUploadScriptReplacesAndKeepsMode(t *testing.T) {
	dir := realTempDir(t)
	dest := filepath.Join(dir, "a")
	_ = os.WriteFile(dest, []byte("old"), 0o640)
	_ = os.Chmod(dest, 0o751)
	code, stdout, stderr := runUploadScript(t, dir, "a", true, 3, []byte("new"))
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if got, _ := os.ReadFile(dest); string(got) != "new" {
		t.Errorf("content %q", got)
	}
	if st, _ := os.Stat(dest); st.Mode().Perm() != 0o751 {
		t.Errorf("mode %v, want 0751", st.Mode().Perm())
	}
	if !strings.Contains(stdout, "\nreplaced\n") {
		t.Errorf("stdout %q lacks the replaced marker", stdout)
	}
	assertNoTempFiles(t, dir)
}

func TestUploadScriptRefusesNonRegularDestinations(t *testing.T) {
	dir := realTempDir(t)
	target := filepath.Join(dir, "target")
	_ = os.WriteFile(target, []byte("keep"), 0o644)
	_ = os.Symlink(target, filepath.Join(dir, "link"))
	_ = os.Mkdir(filepath.Join(dir, "sub"), 0o755)
	if err := syscall.Mkfifo(filepath.Join(dir, "fifo"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Symlink(filepath.Join(dir, "nowhere"), filepath.Join(dir, "dangling"))

	for _, name := range []string{"link", "sub", "fifo", "dangling"} {
		for _, overwrite := range []bool{false, true} {
			code, _, _ := runUploadScript(t, dir, name, overwrite, 3, []byte("new"))
			if code != uploadExitNotRegular {
				t.Errorf("%s overwrite=%v: exit %d, want %d", name, overwrite, code, uploadExitNotRegular)
			}
		}
	}
	if got, _ := os.ReadFile(target); string(got) != "keep" {
		t.Errorf("symlink target changed to %q", got)
	}
	if _, err := os.Lstat(filepath.Join(dir, "nowhere")); !os.IsNotExist(err) {
		t.Error("dangling symlink was followed")
	}
	assertNoTempFiles(t, dir)
}

func TestUploadScriptShortBodyLeavesDestinationUntouched(t *testing.T) {
	dir := realTempDir(t)
	dest := filepath.Join(dir, "a")
	_ = os.WriteFile(dest, []byte("original"), 0o644)
	code, _, _ := runUploadScript(t, dir, "a", true, 100, []byte("partial"))
	if code != uploadExitIncomplete {
		t.Errorf("exit %d, want %d", code, uploadExitIncomplete)
	}
	if got, _ := os.ReadFile(dest); string(got) != "original" {
		t.Errorf("destination changed to %q", got)
	}
	code, _, _ = runUploadScript(t, dir, "b", false, 100, []byte("partial"))
	if code != uploadExitIncomplete {
		t.Errorf("new file: exit %d, want %d", code, uploadExitIncomplete)
	}
	if _, err := os.Stat(filepath.Join(dir, "b")); !os.IsNotExist(err) {
		t.Error("a short upload created the file")
	}
	assertNoTempFiles(t, dir)
}

// A refusal must still read stdin to its end; the kubelet never closes an
// exec whose process left input unread.
func TestUploadScriptRefusalConsumesTheBody(t *testing.T) {
	dir := realTempDir(t)
	_ = os.WriteFile(filepath.Join(dir, "a"), []byte("old"), 0o644)
	body := bytes.Repeat([]byte("x"), 1<<20)
	cmd := podFileUploadCommand(dir, "a", false, int64(len(body)))
	c := exec.Command(cmd[0], cmd[1:]...)
	in := bytes.NewReader(body)
	c.Stdin = in
	err := c.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != uploadExitExists {
		t.Fatalf("run: %v, want exit %d", err, uploadExitExists)
	}
	if in.Len() != 0 {
		t.Errorf("%d bytes of stdin left unread", in.Len())
	}
}

func TestUploadScriptDirectoryErrors(t *testing.T) {
	dir := realTempDir(t)
	file := filepath.Join(dir, "f")
	_ = os.WriteFile(file, nil, 0o644)
	if code, _, _ := runUploadScript(t, filepath.Join(dir, "missing"), "a", false, 1, []byte("x")); code != uploadExitDirNotFound {
		t.Errorf("missing dir: exit %d", code)
	}
	if code, _, _ := runUploadScript(t, file, "a", false, 1, []byte("x")); code != uploadExitNotADirectory {
		t.Errorf("file as dir: exit %d", code)
	}
}

func TestUploadScriptReadOnlyDirectoryFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := realTempDir(t)
	_ = os.Chmod(dir, 0o555)
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	code, _, stderr := runUploadScript(t, dir, "a", false, 1, []byte("x"))
	if code == 0 {
		t.Fatal("wrote into a read-only directory")
	}
	status, c, _ := classifyPodFileUploadError(dir+"/a", k8sexec.CodeExitError{Code: code, Err: errors.New("exit")}, stderr)
	if status != http.StatusForbidden || c != uploadCodePermissionDenied {
		t.Errorf("classified as %d %s (stderr %q)", status, c, stderr)
	}
}

func TestUploadScriptRefusesSymlinkedDirectories(t *testing.T) {
	root := realTempDir(t)
	real := filepath.Join(root, "real")
	_ = os.MkdirAll(filepath.Join(real, "sub"), 0o755)
	_ = os.Symlink(real, filepath.Join(root, "link"))

	for _, dir := range []string{filepath.Join(root, "link"), filepath.Join(root, "link", "sub")} {
		code, _, stderr := runUploadScript(t, dir, "a", false, 1, []byte("x"))
		if code != uploadExitSymlinkedDir {
			t.Errorf("%s: exit %d, want %d (stderr %q)", dir, code, uploadExitSymlinkedDir, stderr)
		}
		if !strings.Contains(stderr, real) {
			t.Errorf("%s: stderr %q does not name the real directory", dir, stderr)
		}
	}
	for _, p := range []string{filepath.Join(real, "a"), filepath.Join(real, "sub", "a")} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("%s was written through the symlink", p)
		}
	}
	assertNoTempFiles(t, real)
	assertNoTempFiles(t, filepath.Join(real, "sub"))
}

// A directory that appears at the destination while the body is arriving
// must refuse the commit, not swallow the file into it.
func TestUploadScriptDestinationBecomesDirectoryMidUpload(t *testing.T) {
	for _, overwrite := range []bool{false, true} {
		dir := realTempDir(t)
		dest := filepath.Join(dir, "a")
		if overwrite {
			_ = os.WriteFile(dest, []byte("old"), 0o644)
		}
		body := []byte("new body")
		cmd := podFileUploadCommand(dir, "a", overwrite, int64(len(body)))
		c := exec.Command(cmd[0], cmd[1:]...)
		stdin, err := c.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Start(); err != nil {
			t.Fatal(err)
		}
		_, _ = stdin.Write(body)
		// The temp file exists once the script is reading the body.
		deadline := time.Now().Add(5 * time.Second)
		for !hasTempFile(dir) && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		_ = os.Remove(dest)
		if err := os.Mkdir(dest, 0o755); err != nil {
			t.Fatal(err)
		}
		_ = stdin.Close()
		err = c.Wait()
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != uploadExitNotRegular {
			t.Errorf("overwrite=%v: %v, want exit %d", overwrite, err, uploadExitNotRegular)
		}
		entries, _ := os.ReadDir(dest)
		if len(entries) != 0 {
			t.Errorf("overwrite=%v: the upload landed inside the new directory: %v", overwrite, entries)
		}
		assertNoTempFiles(t, dir)
	}
}

func hasTempFile(dir string) bool {
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".radar-upload.") {
			return true
		}
	}
	return false
}

// The mode goes on after the body is written, so a read-only file can be
// replaced by a user who owns it.
func TestUploadScriptReplacesReadOnlyFile(t *testing.T) {
	dir := realTempDir(t)
	dest := filepath.Join(dir, "ro")
	_ = os.WriteFile(dest, []byte("old"), 0o444)
	code, _, stderr := runUploadScript(t, dir, "ro", true, 3, []byte("new"))
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if got, _ := os.ReadFile(dest); string(got) != "new" {
		t.Errorf("content %q", got)
	}
	if st, _ := os.Stat(dest); st.Mode().Perm() != 0o444 {
		t.Errorf("mode %v, want 0444", st.Mode().Perm())
	}
	assertNoTempFiles(t, dir)
}

// chown clears setuid and setgid, so the mode must be applied after it.
func TestUploadScriptKeepsSpecialBitsAcrossOwnerChange(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("only root can replace a file owned by someone else")
	}
	dir := realTempDir(t)
	dest := filepath.Join(dir, "tool")
	_ = os.WriteFile(dest, []byte("old"), 0o755)
	if err := os.Chown(dest, 1234, 1234); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dest, 0o755|os.ModeSetuid|os.ModeSetgid); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runUploadScript(t, dir, "tool", true, 3, []byte("new"))
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	st, _ := os.Stat(dest)
	if st.Mode()&(os.ModeSetuid|os.ModeSetgid) != os.ModeSetuid|os.ModeSetgid || st.Mode().Perm() != 0o755 {
		t.Errorf("mode %v, want setuid+setgid 0755", st.Mode())
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); !ok || sys.Uid != 1234 || sys.Gid != 1234 {
		t.Errorf("owner changed: %+v", st.Sys())
	}
}
