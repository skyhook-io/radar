package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	k8sexec "k8s.io/client-go/util/exec"

	"github.com/skyhook-io/radar/internal/k8s"
)

// uploadExec is one exec the handler asked for.
type uploadExec struct {
	command   []string
	container string
}

func (u uploadExec) isUpload() bool {
	return len(u.command) >= 3 && u.command[2] == podFileUploadScript
}

func (u uploadExec) isCleanup() bool {
	return len(u.command) >= 3 && u.command[2] == podFileUploadCleanupScript
}

type uploadFakeExec struct {
	ctx     context.Context
	respond func(ctx context.Context, req uploadExec, opts remotecommand.StreamOptions) error
	req     uploadExec
}

func (f *uploadFakeExec) Stream(opts remotecommand.StreamOptions) error {
	return f.StreamWithContext(context.Background(), opts)
}

func (f *uploadFakeExec) StreamWithContext(ctx context.Context, opts remotecommand.StreamOptions) error {
	return f.respond(ctx, f.req, opts)
}

type uploadHarness struct {
	ts    *httptest.Server
	srv   *Server
	mu    sync.Mutex
	execs []uploadExec
}

func (h *uploadHarness) recorded() []uploadExec {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]uploadExec(nil), h.execs...)
}

func uploadServer(t *testing.T, enabled bool, respond func(ctx context.Context, req uploadExec, opts remotecommand.StreamOptions) error) *uploadHarness {
	t.Helper()

	prevConn := k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	t.Cleanup(func() { k8s.SetConnectionStatus(prevConn) })

	apiserver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(apiserver.Close)
	config := &rest.Config{Host: apiserver.URL}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatalf("build clientset: %v", err)
	}
	prevClient := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(prevClient) })
	prevConfig := k8s.SetTestConfig(config)
	t.Cleanup(func() { k8s.SetTestConfig(prevConfig) })

	prevDisable := k8s.ForceDisableExec
	k8s.ForceDisableExec = !enabled
	t.Cleanup(func() { k8s.ForceDisableExec = prevDisable })
	prevMax := podFileUploadMaxBytes
	podFileUploadMaxBytes = 1 << 20
	t.Cleanup(func() { podFileUploadMaxBytes = prevMax })

	h := &uploadHarness{srv: &Server{}}
	h.srv.newExecutor = func(_ *rest.Config, u *url.URL) (remotecommand.Executor, error) {
		req := uploadExec{command: u.Query()["command"], container: u.Query().Get("container")}
		h.mu.Lock()
		h.execs = append(h.execs, req)
		h.mu.Unlock()
		return &uploadFakeExec{respond: respond, req: req}, nil
	}

	router := chi.NewRouter()
	router.Post("/api/pods/{namespace}/{name}/files/upload", h.srv.handlePodFileUpload)
	h.ts = httptest.NewServer(router)
	t.Cleanup(h.ts.Close)
	return h
}

func uploadURL(ts *httptest.Server, dir, name string, overwrite bool) string {
	q := url.Values{"container": {"app"}, "dir": {dir}, "name": {name}}
	if overwrite {
		q.Set("overwrite", "true")
	}
	return ts.URL + "/api/pods/ns/pod/files/upload?" + q.Encode()
}

// uploadError is the writeErrorCode shape the handler answers failures with.
type uploadError struct {
	Error string `json:"error"`
	Code  string `json:"error_code"`
}

func postUpload(t *testing.T, rawurl string, body []byte) (int, uploadError, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawurl, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload request: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var e uploadError
	_ = json.Unmarshal(raw, &e)
	return resp.StatusCode, e, raw
}

// acceptAll reads the whole body the way the real script does and succeeds.
func acceptAll(stdin *[]byte, extra string) func(context.Context, uploadExec, remotecommand.StreamOptions) error {
	return func(_ context.Context, req uploadExec, opts remotecommand.StreamOptions) error {
		if !req.isUpload() {
			return fmt.Errorf("unexpected exec %q", req.command)
		}
		fmt.Fprint(opts.Stdout, "tmp=.radar-upload.Ab12Cd\n"+extra)
		data, err := io.ReadAll(opts.Stdin)
		if err != nil {
			return err
		}
		*stdin = data
		return nil
	}
}

func TestPodFileUploadStreamsBodyIntoTheFixedScript(t *testing.T) {
	var got []byte
	h := uploadServer(t, true, acceptAll(&got, ""))
	payload := []byte("hello\nworld\n")

	status, _, raw := postUpload(t, uploadURL(h.ts, "/etc/app/", "config.yaml", false), payload)
	if status != http.StatusOK {
		t.Fatalf("status %d, want 200 (%s)", status, raw)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("stdin = %q, want %q", got, payload)
	}
	var res podFileUploadResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	if res.Path != "/etc/app/config.yaml" || res.Size != int64(len(payload)) || res.Replaced {
		t.Errorf("result = %+v", res)
	}

	execs := h.recorded()
	if len(execs) != 1 {
		t.Fatalf("got %d execs, want 1", len(execs))
	}
	want := []string{"/bin/sh", "-c", podFileUploadScript, "sh", "/etc/app", "config.yaml", "0", strconv.Itoa(len(payload))}
	if strings.Join(execs[0].command, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("command = %q, want %q", execs[0].command, want)
	}
	if execs[0].container != "app" {
		t.Errorf("container = %q, want app", execs[0].container)
	}
}

func TestPodFileUploadTargetsTheRequestedContainer(t *testing.T) {
	var got []byte
	h := uploadServer(t, true, acceptAll(&got, ""))
	u := strings.Replace(uploadURL(h.ts, "/data", "a.txt", false), "container=app", "container=sidecar", 1)
	status, _, raw := postUpload(t, u, []byte("x"))
	if status != http.StatusOK {
		t.Fatalf("status %d (%s)", status, raw)
	}
	if execs := h.recorded(); len(execs) != 1 || execs[0].container != "sidecar" {
		t.Errorf("execs = %+v, want one exec in container sidecar", execs)
	}
}

// User input must never become script text: a name full of shell syntax
// reaches the container as one argv entry.
func TestPodFileUploadPassesHostileNamesAsArguments(t *testing.T) {
	var got []byte
	h := uploadServer(t, true, acceptAll(&got, ""))
	name := `$(rm -rf ~);'"` + "`id`" + ` -x`

	status, _, raw := postUpload(t, uploadURL(h.ts, "/tmp/$(x)", name, false), []byte("x"))
	if status != http.StatusOK {
		t.Fatalf("status %d (%s)", status, raw)
	}
	cmd := h.recorded()[0].command
	if cmd[2] != podFileUploadScript {
		t.Fatal("script text changed with the request")
	}
	if cmd[4] != "/tmp/$(x)" || cmd[5] != name {
		t.Errorf("args = %q", cmd[4:])
	}
}

func TestPodFileUploadOverwriteFlagAndReplacedResult(t *testing.T) {
	var got []byte
	h := uploadServer(t, true, acceptAll(&got, "replaced\n"))

	status, _, raw := postUpload(t, uploadURL(h.ts, "/data", "a.txt", true), []byte("new"))
	if status != http.StatusOK {
		t.Fatalf("status %d (%s)", status, raw)
	}
	if h.recorded()[0].command[6] != "1" {
		t.Errorf("overwrite arg = %q, want 1", h.recorded()[0].command[6])
	}
	var res podFileUploadResult
	_ = json.Unmarshal(raw, &res)
	if !res.Replaced {
		t.Error("replaced = false, want true")
	}
}

func TestPodFileUploadRefusedWhenExecDisabled(t *testing.T) {
	h := uploadServer(t, false, func(context.Context, uploadExec, remotecommand.StreamOptions) error {
		t.Error("exec ran with writes disabled")
		return nil
	})
	status, e, _ := postUpload(t, uploadURL(h.ts, "/tmp", "a", false), []byte("x"))
	if status != http.StatusForbidden || e.Code != uploadCodeDisabled {
		t.Errorf("got %d %q, want 403 %s", status, e.Code, uploadCodeDisabled)
	}
}

func TestPodFileUploadRejectsBadInput(t *testing.T) {
	h := uploadServer(t, true, func(context.Context, uploadExec, remotecommand.StreamOptions) error {
		t.Error("exec ran for invalid input")
		return nil
	})
	cases := []struct{ dir, name string }{
		{"/tmp", ""},
		{"/tmp", "."},
		{"/tmp", ".."},
		{"/tmp", "a/b"},
		{"/tmp", "../etc/passwd"},
		{"/tmp", "a\x00b"},
		{"/tmp", strings.Repeat("a", 256)},
		{"", "a"},
		{"tmp", "a"},
	}
	for _, c := range cases {
		status, e, raw := postUpload(t, uploadURL(h.ts, c.dir, c.name, false), []byte("x"))
		if status != http.StatusBadRequest || e.Code != uploadCodeBadRequest {
			t.Errorf("dir=%q name=%q: got %d %s, want 400", c.dir, c.name, status, raw)
		}
	}
}

func TestPodFileUploadOverCapIs413BeforeExec(t *testing.T) {
	h := uploadServer(t, true, func(context.Context, uploadExec, remotecommand.StreamOptions) error {
		t.Error("exec ran for an oversized body")
		return nil
	})
	status, e, _ := postUpload(t, uploadURL(h.ts, "/tmp", "big", false), bytes.Repeat([]byte("x"), (1<<20)+1))
	if status != http.StatusRequestEntityTooLarge || e.Code != uploadCodeTooLarge {
		t.Errorf("got %d %q, want 413", status, e.Code)
	}
}

func TestPodFileUploadRequiresContentLength(t *testing.T) {
	h := uploadServer(t, true, func(context.Context, uploadExec, remotecommand.StreamOptions) error {
		t.Error("exec ran without a declared length")
		return nil
	})
	pr, pw := io.Pipe()
	go func() { _, _ = pw.Write([]byte("chunked")); _ = pw.Close() }()
	req, _ := http.NewRequest(http.MethodPost, uploadURL(h.ts, "/tmp", "a", false), pr)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusLengthRequired {
		t.Errorf("status %d, want 411", resp.StatusCode)
	}
}

func TestPodFileUploadBoundsConcurrentWrites(t *testing.T) {
	h := uploadServer(t, true, func(context.Context, uploadExec, remotecommand.StreamOptions) error {
		t.Error("exec ran with every slot taken")
		return nil
	})
	slots := h.srv.podFileWriteSlotsChan()
	for i := 0; i < cap(slots); i++ {
		slots <- struct{}{}
	}
	status, e, _ := postUpload(t, uploadURL(h.ts, "/tmp", "a", false), []byte("x"))
	if status != http.StatusTooManyRequests || e.Code != uploadCodeBusy {
		t.Errorf("got %d %q, want 429", status, e.Code)
	}
}

func TestPodFileUploadClassifiesFailures(t *testing.T) {
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "pods/exec"}, "pod", errors.New("no"))
	cases := []struct {
		name       string
		err        error
		stderr     string
		wantStatus int
		wantCode   string
	}{
		{"rbac status error", forbidden, "", http.StatusForbidden, uploadCodeForbidden},
		{"rbac spdy text", errors.New(`unable to upgrade connection: pods "pod" is forbidden: User "u" cannot create resource "pods/exec"`), "", http.StatusForbidden, uploadCodeForbidden},
		{"dir missing", k8sexec.CodeExitError{Code: uploadExitDirNotFound, Err: errors.New("exit")}, "", http.StatusNotFound, uploadCodeDirNotFound},
		{"not a dir", k8sexec.CodeExitError{Code: uploadExitNotADirectory, Err: errors.New("exit")}, "", http.StatusBadRequest, uploadCodeNotADirectory},
		{"exists", k8sexec.CodeExitError{Code: uploadExitExists, Err: errors.New("exit")}, "", http.StatusConflict, uploadCodeExists},
		{"symlink or special", k8sexec.CodeExitError{Code: uploadExitNotRegular, Err: errors.New("exit")}, "", http.StatusConflict, uploadCodeNotARegularFile},
		{"mount point", k8sexec.CodeExitError{Code: uploadExitMountPoint, Err: errors.New("exit")}, "", http.StatusConflict, uploadCodeMountPoint},
		{"mode", k8sexec.CodeExitError{Code: uploadExitModeUnknown, Err: errors.New("exit")}, "", http.StatusConflict, uploadCodeModeUnknown},
		{"owner", k8sexec.CodeExitError{Code: uploadExitOwnerChange, Err: errors.New("exit")}, "", http.StatusConflict, uploadCodeOwnerChange},
		{"short", k8sexec.CodeExitError{Code: uploadExitIncomplete, Err: errors.New("exit")}, "", http.StatusBadRequest, uploadCodeIncomplete},
		{"erofs", k8sexec.CodeExitError{Code: 1, Err: errors.New("exit")}, "mktemp: failed to create file via template './.radar-upload.XXXXXX': Read-only file system", http.StatusConflict, uploadCodeReadOnly},
		{"non-root eacces", k8sexec.CodeExitError{Code: 1, Err: errors.New("exit")}, "mktemp: (null): Permission denied", http.StatusForbidden, uploadCodePermissionDenied},
		{"enospc", k8sexec.CodeExitError{Code: 1, Err: errors.New("exit")}, "cat: write error: No space left on device", http.StatusInsufficientStorage, uploadCodeNoSpace},
		{"ebusy", k8sexec.CodeExitError{Code: 1, Err: errors.New("exit")}, "mv: cannot move './.radar-upload.x' to './app.conf': Device or resource busy", http.StatusConflict, uploadCodeMountPoint},
		{"no mktemp", k8sexec.CodeExitError{Code: 127, Err: errors.New("exit")}, "sh: mktemp: not found", http.StatusNotImplemented, uploadCodeMissingTools},
		{"no shell", errors.New(`OCI runtime exec failed: exec failed: unable to start container process: exec: "/bin/sh": stat /bin/sh: no such file or directory`), "", http.StatusNotImplemented, uploadCodeNoShell},
		{"transport", errors.New("stream error: connection reset"), "", http.StatusBadGateway, uploadCodeUncertain},
		{"no such pod", errors.New(`unable to upgrade connection: pods "x" not found`), "", http.StatusNotFound, uploadCodePodNotFound},
		{"no such pod status", apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, "x"), "", http.StatusNotFound, uploadCodePodNotFound},
		{"no such container", errors.New("unable to upgrade connection: container nope is not valid for pod pod"), "", http.StatusBadRequest, uploadCodeNoSuchContainer},
		{"other exit", k8sexec.CodeExitError{Code: 1, Err: errors.New("exit")}, "mv: something odd", http.StatusInternalServerError, uploadCodeFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := uploadServer(t, true, func(_ context.Context, req uploadExec, opts remotecommand.StreamOptions) error {
				if req.isCleanup() {
					return nil
				}
				_, _ = io.Copy(io.Discard, opts.Stdin)
				fmt.Fprint(opts.Stderr, c.stderr)
				return c.err
			})
			status, e, raw := postUpload(t, uploadURL(h.ts, "/etc", "app.conf", false), []byte("x"))
			if status != c.wantStatus || e.Code != c.wantCode {
				t.Errorf("got %d %s, want %d %s", status, raw, c.wantStatus, c.wantCode)
			}
			if e.Error == "" {
				t.Error("no message for the user")
			}
		})
	}
}

// A refusal that arrives before the body was read is still classified and
// answered. Whether a browser receives it rather than a reset depends on the
// drain, which Go's own client cannot tell apart; that part is checked in a
// browser.
func TestPodFileUploadEarlyRefusalReachesClientWithLargeBody(t *testing.T) {
	h := uploadServer(t, true, func(_ context.Context, req uploadExec, opts remotecommand.StreamOptions) error {
		fmt.Fprint(opts.Stderr, "radar: mount point")
		return k8sexec.CodeExitError{Code: uploadExitMountPoint, Err: errors.New("exit")}
	})
	status, e, raw := postUpload(t, uploadURL(h.ts, "/etc", "app.conf", true), bytes.Repeat([]byte("x"), 900<<10))
	if status != http.StatusConflict || e.Code != uploadCodeMountPoint {
		t.Errorf("got %d %s, want 409 %s", status, raw, uploadCodeMountPoint)
	}
}

// A connection that drops before the command reports leaves the trap's fate
// unknown, so the handler removes the temp file the script announced.
func TestPodFileUploadCleansUpAfterTransportFailure(t *testing.T) {
	h := uploadServer(t, true, func(_ context.Context, req uploadExec, opts remotecommand.StreamOptions) error {
		if req.isCleanup() {
			return nil
		}
		fmt.Fprint(opts.Stdout, "tmp=.radar-upload.Zz9Yy8\n")
		_, _ = io.Copy(io.Discard, opts.Stdin)
		return errors.New("stream error: connection reset")
	})
	status, e, _ := postUpload(t, uploadURL(h.ts, "/data", "a", false), []byte("x"))
	if status != http.StatusBadGateway || e.Code != uploadCodeUncertain {
		t.Errorf("got %d %q", status, e.Code)
	}
	execs := h.recorded()
	if len(execs) != 2 || !execs[1].isCleanup() {
		t.Fatalf("execs = %+v, want upload then cleanup", execs)
	}
	if got := execs[1].command[4:]; got[0] != "/data" || got[1] != ".radar-upload.Zz9Yy8" {
		t.Errorf("cleanup args = %q", got)
	}
}

// A command that exited ran its own trap; and a temp name that does not look
// like one the script makes is never handed to rm.
func TestPodFileUploadSkipsCleanupWhenUnneededOrUnsafe(t *testing.T) {
	for name, tc := range map[string]struct {
		stdout string
		err    error
	}{
		"exited":    {"tmp=.radar-upload.Ab12Cd\n", k8sexec.CodeExitError{Code: 1, Err: errors.New("exit")}},
		"odd name":  {"tmp=../../etc/passwd\n", errors.New("stream error")},
		"no output": {"", errors.New("stream error")},
	} {
		t.Run(name, func(t *testing.T) {
			h := uploadServer(t, true, func(_ context.Context, req uploadExec, opts remotecommand.StreamOptions) error {
				if req.isCleanup() {
					t.Error("cleanup ran")
					return nil
				}
				fmt.Fprint(opts.Stdout, tc.stdout)
				_, _ = io.Copy(io.Discard, opts.Stdin)
				return tc.err
			})
			postUpload(t, uploadURL(h.ts, "/data", "a", false), []byte("x"))
			if n := len(h.recorded()); n != 1 {
				t.Errorf("got %d execs, want 1", n)
			}
		})
	}
}

// A container that took the whole body but never closes the session must not
// pin the request: the watchdog cancels it and the temp file is removed.
func TestPodFileUploadWatchdogEndsAStuckSession(t *testing.T) {
	prev := podFileUploadCommitTimeout
	podFileUploadCommitTimeout = 100 * time.Millisecond
	t.Cleanup(func() { podFileUploadCommitTimeout = prev })

	h := uploadServer(t, true, func(ctx context.Context, req uploadExec, opts remotecommand.StreamOptions) error {
		if req.isCleanup() {
			return nil
		}
		fmt.Fprint(opts.Stdout, "tmp=.radar-upload.Ww3Ee4\n")
		_, _ = io.Copy(io.Discard, opts.Stdin)
		<-ctx.Done()
		return ctx.Err()
	})
	status, e, raw := postUpload(t, uploadURL(h.ts, "/data", "a", false), []byte("whole body"))
	if status != http.StatusBadGateway || e.Code != uploadCodeUncertain {
		t.Errorf("got %d %s, want 502 %s", status, raw, uploadCodeUncertain)
	}
	if execs := h.recorded(); len(execs) != 2 || !execs[1].isCleanup() {
		t.Errorf("execs = %+v, want a cleanup", execs)
	}
}

// A client that stops sending must not hold an upload slot: the stall
// deadline ends the read, the exec is cancelled and the client is told.
func TestPodFileUploadStalledBodyIsCutOff(t *testing.T) {
	prev := podFileUploadStallTimeout
	podFileUploadStallTimeout = 200 * time.Millisecond
	t.Cleanup(func() { podFileUploadStallTimeout = prev })

	h := uploadServer(t, true, func(ctx context.Context, req uploadExec, opts remotecommand.StreamOptions) error {
		if req.isCleanup() {
			return nil
		}
		fmt.Fprint(opts.Stdout, "tmp=.radar-upload.Ss5Tt6\n")
		_, _ = io.Copy(io.Discard, opts.Stdin)
		<-ctx.Done()
		return ctx.Err()
	})
	u, _ := url.Parse(uploadURL(h.ts, "/data", "a", false))
	conn, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: %s\r\nContent-Length: 1000\r\n\r\nabcd", u.RequestURI(), u.Host)
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("no response to a stalled upload: %v", err)
	}
	raw, _ := io.ReadAll(resp.Body)
	var e uploadError
	_ = json.Unmarshal(raw, &e)
	if resp.StatusCode != http.StatusRequestTimeout || e.Code != uploadCodeStalled {
		t.Errorf("got %d %s, want 408 %s", resp.StatusCode, raw, uploadCodeStalled)
	}
	if slots := h.srv.podFileWriteSlotsChan(); len(slots) != 0 {
		t.Errorf("%d upload slots still held", len(slots))
	}
}

// A browser that goes away mid-body must stop the exec rather than leave it
// waiting, and the partial temp file must be removed.
func TestPodFileUploadClientDisconnectCancelsExecAndCleansUp(t *testing.T) {
	gotPartial := make(chan struct{})
	execDone := make(chan error, 1)
	h := uploadServer(t, true, func(ctx context.Context, req uploadExec, opts remotecommand.StreamOptions) error {
		if req.isCleanup() {
			return nil
		}
		fmt.Fprint(opts.Stdout, "tmp=.radar-upload.Qq1Ww2\n")
		buf := make([]byte, 4)
		_, _ = io.ReadFull(opts.Stdin, buf)
		close(gotPartial)
		// Keep reading as client-go's stdin copier would; the failed body read
		// is what cancels ctx.
		_, err := io.Copy(io.Discard, opts.Stdin)
		select {
		case <-ctx.Done():
			execDone <- ctx.Err()
			return ctx.Err()
		case <-time.After(3 * time.Second):
			execDone <- fmt.Errorf("exec not cancelled (stdin err %v)", err)
			return errors.New("not cancelled")
		}
	})

	u, _ := url.Parse(uploadURL(h.ts, "/data", "a", false))
	conn, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: %s\r\nContent-Length: 1000\r\n\r\nabcd", u.RequestURI(), u.Host)
	select {
	case <-gotPartial:
	case <-time.After(3 * time.Second):
		t.Fatal("exec never saw the partial body")
	}
	conn.Close()

	if err := <-execDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("exec: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if execs := h.recorded(); len(execs) == 2 && execs[1].isCleanup() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("execs = %+v, want a cleanup after the disconnect", h.recorded())
}

func TestPodFileUploadFailsClosedWithoutClusterClient(t *testing.T) {
	h := uploadServer(t, true, func(context.Context, uploadExec, remotecommand.StreamOptions) error {
		t.Error("exec ran without a client")
		return nil
	})
	prev := k8s.SetTestConfig(nil)
	t.Cleanup(func() { k8s.SetTestConfig(prev) })
	status, _, _ := postUpload(t, uploadURL(h.ts, "/tmp", "a", false), []byte("x"))
	if status != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503", status)
	}
}

// The handler has no origin check of its own; the /api middleware supplies
// it. Prove the route is behind it on the real router.
func TestPodFileUploadCrossOriginRefusedOnRealRouter(t *testing.T) {
	srv := New(Config{DevMode: true})
	t.Cleanup(srv.Stop)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	req, _ := http.NewRequest(http.MethodPost, uploadURL(ts, "/tmp", "a", false), strings.NewReader("x"))
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "cross_origin_refused") {
		t.Errorf("got %d %s, want the cross-origin refusal", resp.StatusCode, body)
	}
}
