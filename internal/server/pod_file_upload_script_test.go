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
func TestUploadScriptDropsSpecialBitsOnReplace(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("only root can replace a file owned by someone else")
	}
	dir := realTempDir(t)
	dest := filepath.Join(dir, "tool")
	_ = os.WriteFile(dest, []byte("old"), 0o755)
	if err := os.Chown(dest, 1234, 1234); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dest, 0o755|os.ModeSetuid|os.ModeSetgid|os.ModeSticky); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runUploadScript(t, dir, "tool", true, 3, []byte("new"))
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	st, _ := os.Stat(dest)
	if st.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 || st.Mode().Perm() != 0o755 {
		t.Errorf("mode %v, want plain 0755 with no setuid, setgid or sticky", st.Mode())
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); !ok || sys.Uid != 1234 || sys.Gid != 1234 {
		t.Errorf("owner changed: %+v", st.Sys())
	}
}

// A setgid directory gives the temp file the directory's group, so a replace
// must restore the old file's group even when it matches the writer's.
func TestUploadScriptKeepsGroupInSetgidDirectory(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to set up a setgid directory with a foreign group")
	}
	dir := realTempDir(t)
	if err := os.Chown(dir, 0, 1234); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o775|os.ModeSetgid); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "a")
	_ = os.WriteFile(dest, []byte("old"), 0o644)
	if err := os.Chown(dest, 0, 0); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runUploadScript(t, dir, "a", true, 3, []byte("new"))
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if got, _ := os.ReadFile(dest); string(got) != "new" {
		t.Errorf("content %q", got)
	}
	st, _ := os.Stat(dest)
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("no owner in %T", st.Sys())
	}
	if sys.Uid != 0 || sys.Gid != 0 {
		t.Errorf("owner %d:%d, want 0:0", sys.Uid, sys.Gid)
	}
}

// runUploadScriptWithout runs the script with a PATH holding every command it
// uses except the named ones, to stand in for a minimal image.
func runUploadScriptWithout(t *testing.T, missing []string, dir, name string, overwrite bool, body []byte) (int, string) {
	t.Helper()
	bin := t.TempDir()
	skip := map[string]bool{}
	for _, m := range missing {
		skip[m] = true
	}
	for _, tool := range []string{"mktemp", "cat", "wc", "ln", "mv", "chmod", "ls", "rm", "chown"} {
		if skip[tool] {
			continue
		}
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s not on this host", tool)
		}
		if err := os.Symlink(p, filepath.Join(bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	cmd := podFileUploadCommand(dir, name, overwrite, int64(len(body)))
	c := exec.Command(cmd[0], cmd[1:]...)
	c.Env = []string{"PATH=" + bin}
	c.Stdin = bytes.NewReader(body)
	var stderr bytes.Buffer
	c.Stderr = &stderr
	err := c.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), stderr.String()
	}
	if err != nil {
		t.Fatalf("run script: %v", err)
	}
	return 0, stderr.String()
}

// Without ls the owner check reads nothing, so a replace must be refused
// rather than leave the file owned by the container's user.
func TestUploadScriptWithoutLsRefusesReplace(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("only root can replace a file owned by someone else")
	}
	dir := realTempDir(t)
	dest := filepath.Join(dir, "app.conf")
	_ = os.WriteFile(dest, []byte("old"), 0o640)
	if err := os.Chown(dest, 1000, 1000); err != nil {
		t.Fatal(err)
	}
	code, stderr := runUploadScriptWithout(t, []string{"ls"}, dir, "app.conf", true, []byte("new"))
	if code != 127 {
		t.Fatalf("exit %d, want 127 (%s)", code, stderr)
	}
	if got, _ := os.ReadFile(dest); string(got) != "old" {
		t.Errorf("destination changed to %q", got)
	}
	st, _ := os.Stat(dest)
	if sys, ok := st.Sys().(*syscall.Stat_t); !ok || sys.Uid != 1000 || sys.Gid != 1000 {
		t.Errorf("owner changed: %+v", st.Sys())
	}
	assertNoTempFiles(t, dir)
	if got := missingCommandName(stderr); got != "ls" {
		t.Errorf("missing command = %q from %q, want ls", got, stderr)
	}
}

// A missing chmod is reported as a missing command, on a new file and on a
// replace, not as a raw failure or as unreadable permissions.
func TestUploadScriptWithoutChmodNamesIt(t *testing.T) {
	for _, overwrite := range []bool{false, true} {
		dir := realTempDir(t)
		if overwrite {
			_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("old"), 0o640)
		}
		code, stderr := runUploadScriptWithout(t, []string{"chmod"}, dir, "a.txt", overwrite, []byte("new"))
		if code != 127 {
			t.Fatalf("overwrite=%v: exit %d, want 127 (%s)", overwrite, code, stderr)
		}
		if got := missingCommandName(stderr); got != "chmod" {
			t.Errorf("overwrite=%v: missing command = %q from %q, want chmod", overwrite, got, stderr)
		}
		assertNoTempFiles(t, dir)
	}
}

// Something else in the container swaps the temp file for a symlink while
// the body is arriving. The write, owner and mode must still land on the
// temp file Radar created, never on the symlink's target, and the upload
// must report the swap instead of claiming success.
func TestUploadScriptTempSwappedForSymlinkTouchesNothingElse(t *testing.T) {
	dir := realTempDir(t)
	outside := filepath.Join(realTempDir(t), "target")
	if err := os.WriteFile(outside, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "app.conf")
	_ = os.WriteFile(dest, []byte("old"), 0o644)

	body := []byte("first-half|second-half")
	cmd := podFileUploadCommand(dir, "app.conf", true, int64(len(body)))
	c := exec.Command(cmd[0], cmd[1:]...)
	stdin, err := c.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	c.Stderr = &stderr
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	_, _ = stdin.Write(body[:11])
	var tmp string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		matches, _ := filepath.Glob(filepath.Join(dir, ".radar-upload.*"))
		if len(matches) == 1 {
			if st, err := os.Stat(matches[0]); err == nil && st.Size() == 11 {
				tmp = matches[0]
				break
			}
		}
	}
	if tmp == "" {
		t.Fatal("temp file never received the first half")
	}
	if err := os.Rename(tmp, filepath.Join(dir, "stolen")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, tmp); err != nil {
		t.Fatal(err)
	}
	_, _ = stdin.Write(body[11:])
	_ = stdin.Close()
	err = c.Wait()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 29 {
		t.Fatalf("err %v, want exit 29 (%s)", err, stderr.String())
	}
	if got, _ := os.ReadFile(outside); string(got) != "original" {
		t.Errorf("symlink target written: %q", got)
	}
	if st, _ := os.Stat(outside); st.Mode().Perm() != 0o600 {
		t.Errorf("symlink target mode changed to %v", st.Mode())
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "stolen")); string(got) != string(body) {
		t.Errorf("upload did not land in Radar's own temp file: %q", got)
	}
}

// Another process plants a hard link to one of its own files at the temp name
// before Radar opens it. The upload must not write into that file.
func TestUploadScriptRefusesPlantedHardLinkAtTempName(t *testing.T) {
	dir := realTempDir(t)
	outside := filepath.Join(realTempDir(t), "theirs")
	if err := os.WriteFile(outside, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if filepath.VolumeName(outside) != filepath.VolumeName(dir) {
		t.Skip("needs one filesystem for the hard link")
	}
	bin := t.TempDir()
	for _, tool := range []string{"cat", "wc", "ln", "mv", "chmod", "ls", "rm", "chown"} {
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s not on this host", tool)
		}
		if err := os.Symlink(p, filepath.Join(bin, tool)); err != nil {
			t.Fatal(err)
		}
	}
	// A mktemp that hands out a name it has already filled with a hard link.
	fake := "#!/bin/sh\nln " + outside + " ./.radar-upload.planted || exit 1\necho ./.radar-upload.planted\n"
	if err := os.WriteFile(filepath.Join(bin, "mktemp"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := podFileUploadCommand(dir, "new.txt", false, 6)
	c := exec.Command(cmd[0], cmd[1:]...)
	c.Env = []string{"PATH=" + bin}
	c.Stdin = bytes.NewReader([]byte("secret"))
	if err := c.Run(); err == nil {
		t.Error("upload succeeded through a planted hard link")
	}
	if got, _ := os.ReadFile(outside); string(got) != "original" {
		t.Errorf("planted file was written: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); err == nil {
		t.Error("destination created from a planted temp file")
	}
	if _, err := os.Stat(filepath.Join(dir, ".radar-upload.planted")); err != nil {
		t.Errorf("the planted file must be left alone, not removed: %v", err)
	}
}

// Without rm a temp file could never be removed, so the upload stops before
// creating one and names rm.
func TestUploadScriptWithoutRmStopsBeforeTheTempFile(t *testing.T) {
	dir := realTempDir(t)
	code, stderr := runUploadScriptWithout(t, []string{"rm"}, dir, "a.txt", false, []byte("new"))
	if code != 127 {
		t.Fatalf("exit %d, want 127 (%s)", code, stderr)
	}
	if got := missingCommandName(stderr); got != "rm" {
		t.Errorf("missing command = %q from %q, want rm", got, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.txt")); err == nil {
		t.Error("destination created without rm")
	}
	assertNoTempFiles(t, dir)
}
