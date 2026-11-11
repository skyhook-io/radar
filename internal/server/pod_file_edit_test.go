package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/remotecommand"
	k8sexec "k8s.io/client-go/util/exec"

	"github.com/skyhook-io/radar/internal/k8s"
)

func (u uploadExec) isRead() bool {
	return len(u.command) == 3 && strings.Contains(u.command[2], "tar cfh")
}

func editURL(ts *httptest.Server, filePath, sha string) string {
	q := url.Values{"container": {"app"}, "path": {filePath}, "sha256": {sha}}
	return ts.URL + "/api/pods/ns/pod/files/content?" + q.Encode()
}

func putEdit(t *testing.T, rawurl string, body []byte) (int, uploadError, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, rawurl, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("edit request: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var e uploadError
	_ = json.Unmarshal(raw, &e)
	return resp.StatusCode, e, raw
}

// editCluster serves current as the file's contents to the re-read, and hands
// the write exec to write.
func editCluster(t *testing.T, current []byte, write func(ctx context.Context, req uploadExec, opts remotecommand.StreamOptions) error) func(context.Context, uploadExec, remotecommand.StreamOptions) error {
	return func(ctx context.Context, req uploadExec, opts remotecommand.StreamOptions) error {
		switch {
		case req.isRead():
			writeArchive(t, opts.Stdout, "file", current, -1)
			if opts.Stdin != nil {
				_, _ = io.Copy(io.Discard, opts.Stdin)
			}
			return nil
		case req.isUpload():
			if write == nil {
				t.Errorf("write exec ran: %q", req.command)
				return nil
			}
			return write(ctx, req, opts)
		case req.isCleanup():
			return nil
		}
		return fmt.Errorf("unexpected exec %q", req.command)
	}
}

func TestPodFileEditSavesWhenUnchanged(t *testing.T) {
	old := []byte("key: old\n")
	newer := []byte("\xef\xbb\xbfkey: new\r\nother: 1\r\n")
	var got []byte
	h := uploadServer(t, true, editCluster(t, old, func(_ context.Context, req uploadExec, opts remotecommand.StreamOptions) error {
		fmt.Fprint(opts.Stdout, "tmp=.radar-upload.Ab12Cd\nreplaced\n")
		data, err := io.ReadAll(opts.Stdin)
		got = data
		return err
	}))

	status, _, raw := putEdit(t, editURL(h.ts, "/etc/app/config.yaml", sha256Hex(old)), newer)
	if status != http.StatusOK {
		t.Fatalf("status %d (%s)", status, raw)
	}
	if !bytes.Equal(got, newer) {
		t.Errorf("stdin = %q, want the new contents byte for byte", got)
	}
	var res podFileEditResult
	_ = json.Unmarshal(raw, &res)
	if res.Path != "/etc/app/config.yaml" || res.Size != int64(len(newer)) || res.SHA256 != sha256Hex(newer) {
		t.Errorf("result = %+v", res)
	}

	execs := h.recorded()
	if len(execs) != 2 || !execs[0].isRead() || !execs[1].isUpload() {
		t.Fatalf("execs = %+v, want a re-read then the write", execs)
	}
	want := []string{"/etc/app", "config.yaml", "1", fmt.Sprint(len(newer)), sha256Hex(old)}
	if args := execs[1].command[4:]; strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("write args = %q, want %q (replace, with the expected hash for the in-container check)", args, want)
	}
}

func TestPodFileEditSavesAnEmptyFile(t *testing.T) {
	old := []byte("something\n")
	var got []byte
	sawWrite := false
	h := uploadServer(t, true, editCluster(t, old, func(_ context.Context, _ uploadExec, opts remotecommand.StreamOptions) error {
		sawWrite = true
		data, err := io.ReadAll(opts.Stdin)
		got = data
		return err
	}))
	status, _, raw := putEdit(t, editURL(h.ts, "/data/a.txt", sha256Hex(old)), nil)
	if status != http.StatusOK || !sawWrite || len(got) != 0 {
		t.Fatalf("status %d (%s), wrote=%v %q", status, raw, sawWrite, got)
	}
}

// The re-read finds different contents: the user's edit must not land on
// top of a change they never saw.
func TestPodFileEditRefusesWhenTheFileChanged(t *testing.T) {
	h := uploadServer(t, true, editCluster(t, []byte("changed by someone else\n"), nil))
	status, e, _ := putEdit(t, editURL(h.ts, "/data/a.txt", sha256Hex([]byte("what I opened\n"))), []byte("mine\n"))
	if status != http.StatusConflict || e.Code != editCodeChanged {
		t.Errorf("got %d %q, want 409 %s", status, e.Code, editCodeChanged)
	}
}

func TestPodFileEditRefusesWhenTheFileWasRemoved(t *testing.T) {
	h := uploadServer(t, true, func(_ context.Context, req uploadExec, opts remotecommand.StreamOptions) error {
		if !req.isRead() {
			t.Errorf("unexpected exec %q", req.command)
			return nil
		}
		fmt.Fprint(opts.Stderr, "tar: ./a.txt: Cannot stat: No such file or directory\n")
		return k8sexec.CodeExitError{Code: 2, Err: errors.New("exit")}
	})
	status, e, raw := putEdit(t, editURL(h.ts, "/data/a.txt", sha256Hex([]byte("x"))), []byte("y"))
	if status != http.StatusConflict || e.Code != editCodeChanged {
		t.Errorf("got %d %s, want 409 %s", status, raw, editCodeChanged)
	}
}

// The container's own check, just before the rename, caught a change that
// happened after the server's re-read.
func TestPodFileEditRefusesWhenTheContainerSeesAChange(t *testing.T) {
	old := []byte("old\n")
	h := uploadServer(t, true, editCluster(t, old, func(_ context.Context, _ uploadExec, opts remotecommand.StreamOptions) error {
		_, _ = io.Copy(io.Discard, opts.Stdin)
		fmt.Fprint(opts.Stdout, "tmp=.radar-upload.Ab12Cd\n")
		fmt.Fprint(opts.Stderr, "radar: file changed\n")
		return k8sexec.CodeExitError{Code: uploadExitChanged, Err: errors.New("exit")}
	}))
	status, e, _ := putEdit(t, editURL(h.ts, "/data/a.txt", sha256Hex(old)), []byte("new\n"))
	if status != http.StatusConflict || e.Code != editCodeChanged {
		t.Errorf("got %d %q, want 409 %s", status, e.Code, editCodeChanged)
	}
}

func TestPodFileEditRefusesNonTextAndOversizedContents(t *testing.T) {
	h := uploadServer(t, true, func(context.Context, uploadExec, remotecommand.StreamOptions) error {
		t.Error("exec ran for contents that cannot be saved")
		return nil
	})
	sha := sha256Hex([]byte("x"))
	if status, e, _ := putEdit(t, editURL(h.ts, "/data/a", sha), []byte{'a', 0, 'b'}); status != http.StatusUnsupportedMediaType || e.Code != editCodeNotText {
		t.Errorf("binary: got %d %q, want 415 %s", status, e.Code, editCodeNotText)
	}
	if status, e, _ := putEdit(t, editURL(h.ts, "/data/a", sha), []byte{0xe9, 'a'}); status != http.StatusUnsupportedMediaType || e.Code != editCodeNotText {
		t.Errorf("invalid UTF-8: got %d %q, want 415 %s", status, e.Code, editCodeNotText)
	}
	big := bytes.Repeat([]byte("x"), podFilePreviewByteCap+1)
	if status, e, _ := putEdit(t, editURL(h.ts, "/data/a", sha), big); status != http.StatusRequestEntityTooLarge || e.Code != editCodeTooLarge {
		t.Errorf("oversized: got %d %q, want 413 %s", status, e.Code, editCodeTooLarge)
	}
}

// A file that grew past the limit since it was opened is not the file the
// user edited.
func TestPodFileEditRefusesWhenTheFileGrewPastTheLimit(t *testing.T) {
	h := uploadServer(t, true, editCluster(t, bytes.Repeat([]byte("x"), podFilePreviewByteCap+1), nil))
	status, e, _ := putEdit(t, editURL(h.ts, "/data/a", sha256Hex([]byte("x"))), []byte("y"))
	if status != http.StatusConflict || e.Code != editCodeChanged {
		t.Errorf("got %d %q, want 409 %s", status, e.Code, editCodeChanged)
	}
}

func TestPodFileEditRejectsBadInput(t *testing.T) {
	h := uploadServer(t, true, func(context.Context, uploadExec, remotecommand.StreamOptions) error {
		t.Error("exec ran for invalid input")
		return nil
	})
	good := sha256Hex([]byte("x"))
	for _, c := range []struct{ path, sha string }{
		{"", good},
		{"relative/a", good},
		{"/", good},
		{"/data/..", good},
		{"/data/a", ""},
		{"/data/a", "abc"},
		{"/data/a", strings.Repeat("g", 64)},
	} {
		if status, e, raw := putEdit(t, editURL(h.ts, c.path, c.sha), []byte("y")); status != http.StatusBadRequest || e.Code != uploadCodeBadRequest {
			t.Errorf("path=%q sha=%q: got %d %s, want 400", c.path, c.sha, status, raw)
		}
	}
}

func TestPodFileEditRefusedWhenExecDisabled(t *testing.T) {
	h := uploadServer(t, false, func(context.Context, uploadExec, remotecommand.StreamOptions) error {
		t.Error("exec ran with exec disabled")
		return nil
	})
	status, e, _ := putEdit(t, editURL(h.ts, "/data/a", sha256Hex([]byte("x"))), []byte("y"))
	if status != http.StatusForbidden || e.Code != uploadCodeDisabled {
		t.Errorf("got %d %q, want 403 %s", status, e.Code, uploadCodeDisabled)
	}
}

func TestPodFileEditOffInCloudMode(t *testing.T) {
	t.Setenv("RADAR_CLOUD_MODE", "true")
	h := uploadServer(t, true, func(context.Context, uploadExec, remotecommand.StreamOptions) error {
		t.Error("exec ran in Cloud mode")
		return nil
	})
	status, e, _ := putEdit(t, editURL(h.ts, "/data/a", sha256Hex([]byte("x"))), []byte("y"))
	if status != http.StatusForbidden || e.Code != uploadCodeCloud {
		t.Errorf("got %d %q, want 403 %s", status, e.Code, uploadCodeCloud)
	}
	for mode, want := range map[string]bool{"true": false, "false": true} {
		t.Setenv("RADAR_CLOUD_MODE", mode)
		w := httptest.NewRecorder()
		h.srv.handleCapabilities(w, httptest.NewRequest(http.MethodGet, "/api/capabilities", nil))
		var caps k8s.Capabilities
		if err := json.Unmarshal(w.Body.Bytes(), &caps); err != nil {
			t.Fatal(err)
		}
		if caps.Features.PodFileEdit != want {
			t.Errorf("RADAR_CLOUD_MODE=%s: podFileEdit=%v, want %v", mode, caps.Features.PodFileEdit, want)
		}
	}
}

func TestPodFileEditClassifiesWriteFailuresInItsOwnWords(t *testing.T) {
	old := []byte("old\n")
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "pods/exec"}, "pod", errors.New("no"))
	for name, tc := range map[string]struct {
		err        error
		stderr     string
		wantStatus int
		wantCode   string
	}{
		"read-only":  {k8sexec.CodeExitError{Code: 1, Err: errors.New("exit")}, "sh: can't create ./.radar-upload.x: Read-only file system", http.StatusConflict, uploadCodeReadOnly},
		"owner":      {k8sexec.CodeExitError{Code: uploadExitOwnerChange, Err: errors.New("exit")}, "", http.StatusConflict, uploadCodeOwnerChange},
		"mount":      {k8sexec.CodeExitError{Code: uploadExitMountPoint, Err: errors.New("exit")}, "", http.StatusConflict, uploadCodeMountPoint},
		"permission": {k8sexec.CodeExitError{Code: 1, Err: errors.New("exit")}, "sh: can't create ./.radar-upload.x: Permission denied", http.StatusForbidden, uploadCodePermissionDenied},
		"forbidden":  {forbidden, "", http.StatusForbidden, uploadCodeForbidden},
	} {
		t.Run(name, func(t *testing.T) {
			h := uploadServer(t, true, editCluster(t, old, func(_ context.Context, _ uploadExec, opts remotecommand.StreamOptions) error {
				_, _ = io.Copy(io.Discard, opts.Stdin)
				fmt.Fprint(opts.Stderr, tc.stderr)
				return tc.err
			}))
			status, e, raw := putEdit(t, editURL(h.ts, "/data/a.txt", sha256Hex(old)), []byte("new\n"))
			if status != tc.wantStatus || e.Code != tc.wantCode {
				t.Errorf("got %d %s, want %d %s", status, raw, tc.wantStatus, tc.wantCode)
			}
			if strings.Contains(strings.ToLower(e.Error), "upload") {
				t.Errorf("message %q talks about uploading", e.Error)
			}
		})
	}
}

// Upload's messages must read exactly as they did before the classifier
// learned a second set of words.
func TestPodFileUploadWordingUnchanged(t *testing.T) {
	for _, tc := range []struct {
		err    error
		stderr string
		want   string
	}{
		{k8sexec.CodeExitError{Code: uploadExitIncomplete, Err: errors.New("exit")}, "", "The upload stopped before the whole file arrived; nothing was written"},
		{errors.New(`OCI runtime exec failed: exec failed: unable to start container process: exec: "/bin/sh": stat /bin/sh: no such file or directory`), "",
			"Container has no shell (/bin/sh), which uploading needs. Distroless and scratch images cannot take uploads."},
		{errors.New("stream error: connection reset"), "",
			"The connection to the container dropped before it confirmed the upload; refresh the listing to see whether /data/a.txt changed"},
		{k8sexec.CodeExitError{Code: 1, Err: errors.New("exit")}, "mv: something odd", "Upload failed: exit mv: something odd"},
	} {
		if _, _, got := classifyPodFileUploadError("/data/a.txt", tc.err, tc.stderr); got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
}

func TestPodFileEditCrossOriginRefusedOnRealRouter(t *testing.T) {
	srv := New(Config{DevMode: true})
	t.Cleanup(srv.Stop)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	req, _ := http.NewRequest(http.MethodPut, editURL(ts, "/data/a", sha256Hex([]byte("x"))), strings.NewReader("y"))
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

func TestPodFilePreviewReturnsTheHashOfTheBytesServed(t *testing.T) {
	payload := []byte("\xef\xbb\xbfa: 1\r\nb: 2\n")
	ts := previewServer(t, func(t *testing.T, req execRequest, opts remotecommand.StreamOptions) error {
		writeArchive(t, opts.Stdout, "f.yaml", payload, -1)
		if opts.Stdin != nil {
			_, _ = io.Copy(io.Discard, opts.Stdin)
		}
		return nil
	})
	resp, body := fetchPreview(t, previewURL(ts, "/etc/f.yaml"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d (%s)", resp.StatusCode, body)
	}
	got := decodeSuccess(t, body)
	if got.SHA256 != sha256Hex(payload) {
		t.Errorf("sha256 = %q, want %q", got.SHA256, sha256Hex(payload))
	}
	if got.Content != string(payload) {
		t.Errorf("content %q is not the bytes served, BOM and CRLF included", got.Content)
	}
}
