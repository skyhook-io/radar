package server

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	k8sexec "k8s.io/client-go/util/exec"
)

func TestPodFileTarCommand(t *testing.T) {
	cmd := podFileTarCommand("/var/log", "app.log")
	if len(cmd) != 3 || cmd[0] != "/bin/sh" || cmd[1] != "-c" {
		t.Fatalf("expected an sh -c invocation, got %q", cmd)
	}

	script := cmd[2]
	for _, want := range []string{
		"tar cfh -",       // -h so a symlink yields the bytes it points at
		"-C '/var/log'",   // the directory is quoted for the shell
		"'./app.log'",     // "./" keeps a name starting with "-" from reading as an option
		podFileDrainGuard, // the guard is what keeps the file whole over a slow link
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script %q is missing %q", script, want)
		}
	}
}

func TestPodFileTarCommandQuotesHostileNames(t *testing.T) {
	script := podFileTarCommand("/tmp", "'; rm -rf / #")[2]
	if strings.Contains(script, "rm -rf /;") || !strings.Contains(script, `'./'\''; rm -rf / #'`) {
		t.Errorf("single quotes in the file name are not neutralised: %q", script)
	}

	script = podFileTarCommand("/tmp", "-C")[2]
	if !strings.Contains(script, "'./-C'") {
		t.Errorf("a file named -C must not reach tar as an option: %q", script)
	}
}

func TestIsDownloadablePath(t *testing.T) {
	for path, want := range map[string]bool{
		"/var/log/app.log": true,
		"/app.log":         true,
		"/":                false,
		".":                false,
		"..":               false,
	} {
		if got := isDownloadablePath(path); got != want {
			t.Errorf("isDownloadablePath(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestIsShellMissing(t *testing.T) {
	for msg, want := range map[string]bool{
		`exec: "/bin/sh": stat /bin/sh: no such file or directory`: true,
		`executable file not found in $PATH: "/bin/sh"`:            true,
		"tar: /output/app.log: no such file or directory":          false,
		"tar: /output: Permission denied":                          false,
	} {
		if got := isShellMissing(msg); got != want {
			t.Errorf("isShellMissing(%q) = %v, want %v", msg, got, want)
		}
	}
}

func TestIsCommandMissing(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		stderr string
		want   bool
	}{
		// Behind /bin/sh the runtime never reports the missing binary itself;
		// the shell does, and only the exit code is worded consistently.
		{"shell reports exit 127", k8sexec.CodeExitError{Err: errors.New("command terminated with exit code 127"), Code: 127}, "sh: tar: not found", true},
		{"wrapped exit 127", errors.Join(errors.New("exec failed"), k8sexec.CodeExitError{Err: errors.New("x"), Code: 127}), "", true},
		{"runtime cannot find the shell", errors.New(`executable file not found in $PATH: "/bin/sh"`), "", true},
		// containerd words a missing interpreter its own way, and a distroless
		// container has to reach the cat fallback to get an honest answer.
		{"containerd cannot stat the shell", errors.New(`OCI runtime exec failed: exec: "/bin/sh": stat /bin/sh: no such file or directory`), "", true},
		{"tar failed for another reason", k8sexec.CodeExitError{Err: errors.New("command terminated with exit code 1"), Code: 1}, "tar: /x: Permission denied", false},
		{"no error at all", nil, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isCommandMissing(tt.err, tt.stderr); got != tt.want {
				t.Errorf("isCommandMissing() = %v, want %v", got, tt.want)
			}
		})
	}
}

// tarArchive returns a single-file tar archive, optionally cut short to
// reproduce the stream Kubernetes hands back when it drops the tail of a
// large file.
func tarArchive(t *testing.T, name string, payload []byte, keepBytes int) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(payload)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	out := buf.Bytes()
	if keepBytes >= 0 && keepBytes < len(out) {
		out = out[:keepBytes]
	}
	return out
}

// streamOver builds a podFileStream reading a ready-made archive, which is
// enough to exercise everything the transfer does with the bytes.
func streamOver(t *testing.T, archive []byte) *podFileStream {
	t.Helper()
	tr := tar.NewReader(bytes.NewReader(archive))
	header, err := tr.Next()
	if err != nil {
		t.Fatalf("archive has no entry: %v", err)
	}
	return &podFileStream{size: header.Size, payload: tr}
}

func TestPodFileStreamDeliversWholeFile(t *testing.T) {
	payload := bytes.Repeat([]byte("radar"), 5000)
	stream := streamOver(t, tarArchive(t, "app.log", payload, -1))

	var got bytes.Buffer
	n, err := io.Copy(&got, stream)
	if err != nil {
		t.Fatalf("copy failed: %v", err)
	}
	if n != int64(len(payload)) || !bytes.Equal(got.Bytes(), payload) {
		t.Errorf("copied %d bytes, want %d identical bytes", n, len(payload))
	}
	if !stream.complete {
		t.Error("a fully read stream must be marked complete so Close can release the guard")
	}
}

func TestPodFileStreamRejectsTruncatedTransfer(t *testing.T) {
	payload := bytes.Repeat([]byte("radar"), 5000)
	full := tarArchive(t, "app.log", payload, -1)
	stream := streamOver(t, tarArchive(t, "app.log", payload, len(full)-4096))

	n, err := io.Copy(io.Discard, stream)
	if err == nil {
		t.Fatal("a truncated transfer must not read as a complete file")
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("error = %v, want it to wrap io.ErrUnexpectedEOF", err)
	}
	if n >= int64(len(payload)) {
		t.Errorf("copied %d bytes, expected fewer than the declared %d", n, len(payload))
	}
	if stream.complete {
		t.Error("a truncated stream must not be marked complete")
	}
}

func TestPodFileStreamAcceptsEmptyFile(t *testing.T) {
	stream := streamOver(t, tarArchive(t, "empty.log", nil, -1))

	n, err := io.Copy(io.Discard, stream)
	if err != nil || n != 0 {
		t.Fatalf("copy of an empty file = (%d, %v), want (0, nil)", n, err)
	}
	if !stream.complete {
		t.Error("an empty file is still a complete transfer")
	}
}

func TestPodFileCatCommand(t *testing.T) {
	cmd := podFileCatCommand("/var/log/app.log")
	if len(cmd) != 3 || cmd[0] != "/bin/sh" || cmd[1] != "-c" {
		t.Fatalf("expected an sh -c invocation, got %q", cmd)
	}
	script := cmd[2]
	for _, want := range []string{
		"wc -c < '/var/log/app.log'", // the size is announced so a short read is detectable
		"cat '/var/log/app.log'",
		"read _", // the drain guard, without which a slow link loses the tail
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script %q is missing %q", script, want)
		}
	}
	if !strings.Contains(podFileCatCommand("/tmp/'; id #")[2], `'/tmp/'\''; id #'`) {
		t.Error("single quotes in the file name are not neutralised")
	}
}

// wc on a character device never reaches an end, so anything that exists but is
// not a regular file has to be turned away before wc opens it.
func TestPodFileCatCommandRefusesNonRegularFilesBeforeReading(t *testing.T) {
	script := podFileCatCommand("/dev/zero")[2]
	guard := strings.Index(script, "[ -e '/dev/zero' ] && [ ! -f '/dev/zero' ]")
	if guard < 0 {
		t.Fatalf("script has no type guard: %q", script)
	}
	if wc := strings.Index(script, "wc -c"); wc < guard {
		t.Error("the type guard must run before wc opens the path")
	}
	if !strings.Contains(script, "exit 3") {
		t.Errorf("the guard must exit with %d so the handler can name the reason: %q", podFileNotRegularExit, script)
	}
}

// The size and the bytes come from two separate reads of the file. If it shrank
// in between, the reader is waiting for bytes that will never arrive, so arming
// the guard would park the command and hang the request instead of failing it.
func TestPodFileCatCommandArmsTheGuardOnlyWhenTheFileStillHoldsWhatWasPromised(t *testing.T) {
	script := podFileCatCommand("/var/log/app.log")[2]

	recheck := strings.Index(script, "after=$(wc -c <")
	guard := strings.Index(script, "read _")
	if recheck < 0 {
		t.Fatalf("script never re-measures the file before arming the guard: %q", script)
	}
	if guard < recheck {
		t.Errorf("the guard is armed before the re-measure, so a shrinking file still parks it: %q", script)
	}
	gate := strings.Index(script, "-ge ")
	if gate < recheck || gate > guard {
		t.Errorf("the re-measure must gate the guard on still having the promised bytes: %q", script)
	}
}

func TestClassifyPodFileOpenErrorNamesANonRegularFile(t *testing.T) {
	err := classifyPodFileOpenError("/dev/zero",
		io.EOF,
		k8sexec.CodeExitError{Err: errors.New("command terminated with exit code 3"), Code: podFileNotRegularExit},
		"not a regular file")
	if !err.notAFile {
		t.Errorf("exit %d should read as \"not a regular file\", got %+v", podFileNotRegularExit, err)
	}
	if err.commandMissing || err.notFound {
		t.Errorf("exit %d must not be mistaken for a missing command or a missing file: %+v", podFileNotRegularExit, err)
	}
}

// A shell says "can't open" for a file that is absent and for one it may not
// read. Calling the second case not-found sends the reader looking for a file
// that is sitting right there.
func TestClassifyPodFileOpenErrorSeparatesPermissionFromMissing(t *testing.T) {
	err := classifyPodFileOpenError("/output/secret.txt",
		io.EOF,
		k8sexec.CodeExitError{Err: errors.New("command terminated with exit code 1"), Code: 1},
		"sh: can't open /output/secret.txt: Permission denied")
	if err.notFound {
		t.Error("a file the container cannot read must not be reported as missing")
	}
	if !strings.Contains(err.message, "Permission denied") {
		t.Errorf("message = %q, want it to name the permission problem", err.message)
	}
}

// tar words the same problem differently, and it has to land the same way.
func TestClassifyPodFileOpenErrorNamesAPermissionProblemFromTar(t *testing.T) {
	err := classifyPodFileOpenError("/output/locked/secret.txt",
		io.EOF,
		k8sexec.CodeExitError{Err: errors.New("command terminated with exit code 1"), Code: 1},
		"tar: can't change directory to '/output/locked': Permission denied")
	if err.notFound || err.notAFile || err.commandMissing {
		t.Errorf("a permission problem was misrouted: %+v", err)
	}
	if !strings.Contains(err.message, "Permission denied") {
		t.Errorf("message = %q, want it to name the permission problem", err.message)
	}
}

func TestClassifyPodFileOpenErrorNamesAMissingFile(t *testing.T) {
	// busybox ash words it without "or directory" and in lower case.
	err := classifyPodFileOpenError("/output/nope",
		io.EOF,
		k8sexec.CodeExitError{Err: errors.New("command terminated with exit code 1"), Code: 1},
		"/bin/sh: can't open /output/nope: no such file")
	if !err.notFound {
		t.Errorf("a shell that cannot open the path should read as not-found, got %+v", err)
	}
}

// The cat fallback frames the file by the size the container announced, so a
// stream cut short is caught the same way the tar path catches it.
func TestPodFileStreamCatFramingRejectsShortRead(t *testing.T) {
	full := "0123456789"
	stream := &podFileStream{size: int64(len(full)), payload: io.LimitReader(strings.NewReader(full[:6]), int64(len(full)))}

	n, err := io.Copy(io.Discard, stream)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("copy of a short cat stream = (%d, %v), want an unexpected-EOF error", n, err)
	}
	if stream.complete {
		t.Error("a short cat stream must not be marked complete")
	}
}

func TestPodFileStreamCatFramingAcceptsExactRead(t *testing.T) {
	full := "0123456789"
	stream := &podFileStream{size: int64(len(full)), payload: io.LimitReader(strings.NewReader(full), int64(len(full)))}

	var got bytes.Buffer
	if _, err := io.Copy(&got, stream); err != nil {
		t.Fatalf("copy failed: %v", err)
	}
	if got.String() != full || !stream.complete {
		t.Errorf("read %q (complete=%v), want %q complete", got.String(), stream.complete, full)
	}
}

// fakeExec stands in for the exec goroutine: it writes an archive to stdout at
// the pace a real transfer would, waits for the caller to release the drain
// guard by closing stdin, and then reports how the remote command finished.
func fakeExec(t *testing.T, archive []byte, commandErr error) *podFileStream {
	t.Helper()
	stdoutR, stdoutW := io.Pipe()
	stdinR, stdinW := io.Pipe()
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		_, writeErr := stdoutW.Write(archive)
		if writeErr == nil {
			// The guard: the command stays alive until stdin closes.
			_, _ = io.Copy(io.Discard, stdinR)
		}
		select {
		case <-ctx.Done():
			_ = stdoutW.CloseWithError(ctx.Err())
			done <- ctx.Err()
		default:
			_ = stdoutW.CloseWithError(commandErr)
			done <- commandErr
		}
	}()

	tr := tar.NewReader(stdoutR)
	header, err := tr.Next()
	if err != nil {
		t.Fatalf("archive has no entry: %v", err)
	}
	return &podFileStream{
		size: header.Size, payload: tr, stdout: stdoutR, stdin: stdinW,
		stderr: &bytes.Buffer{}, done: done, cancel: cancel,
	}
}

func TestPodFileStreamCloseReleasesTheGuardAndReportsSuccess(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 4096)
	stream := fakeExec(t, tarArchive(t, "app.log", payload, -1), nil)

	if _, err := io.Copy(io.Discard, stream); err != nil {
		t.Fatalf("copy failed: %v", err)
	}
	if err := closeWithin(t, stream, 5*time.Second); err != nil {
		t.Errorf("Close() = %v, want nil", err)
	}
}

func TestPodFileStreamCloseSurfacesACommandThatFailedLate(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 4096)
	commandErr := errors.New("command terminated with exit code 1")
	stream := fakeExec(t, tarArchive(t, "app.log", payload, -1), commandErr)

	if _, err := io.Copy(io.Discard, stream); err != nil {
		t.Fatalf("copy failed: %v", err)
	}
	if err := closeWithin(t, stream, 5*time.Second); !errors.Is(err, commandErr) {
		t.Errorf("Close() = %v, want the command's own error", err)
	}
}

// Abandoning the download must return at once rather than pulling the rest of
// the file across first, and must not claim the file arrived.
func TestPodFileStreamCloseWithoutReadingDoesNotHang(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 1<<20)
	stream := fakeExec(t, tarArchive(t, "app.log", payload, -1), nil)

	closeWithin(t, stream, 5*time.Second)
	if stream.complete {
		t.Error("an abandoned transfer must not be marked complete")
	}
	if stream.written >= int64(len(payload)) {
		t.Errorf("Close drained %d bytes of an abandoned transfer, want it to stop early", stream.written)
	}
}

// When no archive arrived, the command's own status is the only thing that can
// tell a missing file from an unreadable one. Tearing the exec down before it
// reports replaces that with "context canceled" and the reason is gone.
func TestPodFileStreamClosePreservesWhyTheCommandFailed(t *testing.T) {
	commandErr := errors.New("command terminated with exit code 2")
	stream := failedExec(t, commandErr)

	err := closeWithin(t, stream, 5*time.Second)
	if errors.Is(err, context.Canceled) {
		t.Fatal("cancellation raced away the command's status")
	}
	if !errors.Is(err, commandErr) {
		t.Errorf("Close() = %v, want the command's own error", err)
	}
}

// failedExec stands in for a command that wrote no archive and exited with a
// status the handler needs in order to classify the failure.
func failedExec(t *testing.T, commandErr error) *podFileStream {
	t.Helper()
	stdoutR, stdoutW := io.Pipe()
	stdinR, stdinW := io.Pipe()
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		// A real exec only reports once its streams are released, so the status
		// lands after the reader has given up on the archive.
		<-ctx.Done()
		_ = stdoutW.CloseWithError(commandErr)
		done <- commandErr
	}()
	// Releasing on pipe close is what the fixed Close relies on; the context
	// here only models "the exec goroutine is still holding the streams".
	go func() {
		_, _ = io.Copy(io.Discard, stdinR)
		cancel()
	}()

	return &podFileStream{
		size: -1, payload: stdoutR, stdout: stdoutR, stdin: stdinW,
		stderr: &bytes.Buffer{}, done: done, cancel: cancel,
	}
}

func TestPodFileStreamCloseIsIdempotent(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 4096)
	stream := fakeExec(t, tarArchive(t, "app.log", payload, -1), nil)

	if _, err := io.Copy(io.Discard, stream); err != nil {
		t.Fatalf("copy failed: %v", err)
	}
	first := closeWithin(t, stream, 5*time.Second)
	second := closeWithin(t, stream, 5*time.Second)
	if first != second {
		t.Errorf("second Close() = %v, want the first result %v", second, first)
	}
}

func closeWithin(t *testing.T, stream *podFileStream, limit time.Duration) error {
	t.Helper()
	result := make(chan error, 1)
	go func() { result <- stream.Close() }()
	select {
	case err := <-result:
		return err
	case <-time.After(limit):
		t.Fatal("Close() did not return")
		return nil
	}
}

// Running a command in a pod and writing a file to the user's disk is exactly
// what a page on another site must not be able to trigger.
func TestPodFileSaveRejectsCrossOriginRequests(t *testing.T) {
	s := &Server{saveFileStreamFunc: func(string, io.Reader) (string, error) {
		t.Error("a cross-origin request reached the save callback")
		return "", nil
	}}

	for origin, wantStatus := range map[string]int{
		"https://evil.example.com":  http.StatusForbidden,
		"http://localhost.evil.com": http.StatusForbidden,
		"http://localhost:9280":     0, // allowed through to the handler's own checks
		"":                          0, // same-origin or a non-browser caller
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/pods/ns/pod/files/save?container=c&path=/f", nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		rec := httptest.NewRecorder()
		s.handlePodFileSave(rec, req)

		if wantStatus == http.StatusForbidden && rec.Code != http.StatusForbidden {
			t.Errorf("Origin %q got %d, want %d", origin, rec.Code, http.StatusForbidden)
		}
		if wantStatus == 0 && rec.Code == http.StatusForbidden {
			t.Errorf("Origin %q was rejected as cross-origin", origin)
		}
	}
}

func TestPodFileSourceServesCatFallbackBytes(t *testing.T) {
	content := []byte("no tar in this container")
	src := &podFileSource{name: "app.conf", size: int64(len(content)), body: bytes.NewReader(content)}

	got, err := io.ReadAll(src)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("read %q, want %q", got, content)
	}
	if err := src.Close(); err != nil {
		t.Errorf("closing a buffered source must not fail: %v", err)
	}
}
