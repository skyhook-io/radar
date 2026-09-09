package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	k8sexec "k8s.io/client-go/util/exec"

	"github.com/skyhook-io/radar/internal/k8s"
)

// The preview endpoint is the inline text-file viewer's backing route. It
// reuses the same tar/cat plumbing as download, so what these tests cover is
// specifically what preview adds on top: the size gate, the sniff/UTF-8
// classification, and the curated error codes the frontend renders as
// fallback UI. The download tests already prove the exec seam itself works.

// ---------------------------------------------------------------------------
// classifyPreviewBytes — the pure classifier, no exec involved
// ---------------------------------------------------------------------------

func TestClassifyPreviewAcceptsUTF8Text(t *testing.T) {
	got := classifyPreviewBytes([]byte("hello, 世界\nsecond line"))
	if got.code != "" {
		t.Errorf("code = %q, want empty (text is previewable)", got.code)
	}
	if !strings.HasPrefix(got.mimeType, "text/") {
		t.Errorf("mimeType = %q, want a text/ type", got.mimeType)
	}
}

func TestClassifyPreviewAcceptsJSON(t *testing.T) {
	got := classifyPreviewBytes([]byte(`{"a":1,"b":[true,null,"x"]}`))
	if got.code != "" {
		t.Errorf("JSON was refused: code=%q mime=%q", got.code, got.mimeType)
	}
}

// A YAML/nginx-conf-shaped input sniffs as application/octet-stream. The
// NUL-byte fallback is what keeps these from being turned away — and this
// is the case most likely to regress if the classifier is simplified later.
func TestClassifyPreviewAcceptsYAMLViaNULFallback(t *testing.T) {
	yaml := []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cfg\ndata:\n  foo: bar\n")
	got := classifyPreviewBytes(yaml)
	if got.code != "" {
		t.Errorf("YAML was refused: code=%q mime=%q", got.code, got.mimeType)
	}
	if !strings.Contains(got.mimeType, "text/plain") {
		t.Errorf("YAML fell to a non-text mime: %q", got.mimeType)
	}
}

func TestClassifyPreviewRejectsNULByteBinary(t *testing.T) {
	// A payload with a NUL byte in the sniff window is the shape http.DetectContentType
	// treats as octet-stream too — this is the "binary" branch's happy path.
	binary := append([]byte("ELF header-ish"), 0x00, 0x01, 0x02, 0x03)
	got := classifyPreviewBytes(binary)
	if got.code != previewCodeBinaryFile {
		t.Errorf("code = %q, want binary_file", got.code)
	}
	if got.message == "" {
		t.Error("binary rejection must carry a curated message for the frontend")
	}
}

func TestClassifyPreviewRejectsInvalidUTF8(t *testing.T) {
	// Latin-1 é as one byte 0xE9 (no NULs) — passes the NUL scan, fails utf8.Valid.
	// A byte the runtime is willing to name distinguishes this from binary in the
	// message so an operator sees which fallback fired.
	got := classifyPreviewBytes([]byte{0x68, 0x65, 0xE9, 0x6C, 0x6C, 0x6F})
	if got.code != previewCodeBinaryFile {
		t.Errorf("code = %q, want binary_file for non-UTF-8", got.code)
	}
	if !strings.Contains(strings.ToLower(got.message), "utf-8") {
		t.Errorf("message %q should mention UTF-8 so the operator knows why", got.message)
	}
}

func TestClassifyPreviewMarksEmptyFile(t *testing.T) {
	got := classifyPreviewBytes(nil)
	if got.code != previewCodeEmptyFile {
		t.Errorf("code = %q, want empty_file", got.code)
	}
}

// ---------------------------------------------------------------------------
// Handler-level tests — drive the real handler through the fake exec seam
// ---------------------------------------------------------------------------

// previewServer is the preview twin of downloadServer. The plumbing is
// identical; only the route it mounts differs.
func previewServer(t *testing.T, respond func(t *testing.T, req execRequest, opts remotecommand.StreamOptions) error) *httptest.Server {
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

	srv := &Server{}
	srv.newExecutor = func(_ *rest.Config, u *url.URL) (remotecommand.Executor, error) {
		return &fakeExecutor{t: t, respond: func(t *testing.T, opts remotecommand.StreamOptions) error {
			return respond(t, execRequest{command: u.Query()["command"]}, opts)
		}}, nil
	}

	router := chi.NewRouter()
	router.Get("/api/pods/{namespace}/{name}/file", srv.handlePodFilePreview)
	ts := httptest.NewServer(router)
	t.Cleanup(ts.Close)
	return ts
}

func previewURL(ts *httptest.Server, filePath string) string {
	return ts.URL + "/api/pods/ns/pod/file?container=app&path=" + url.QueryEscape(filePath)
}

// fetchPreview issues the request and decodes either the success or error
// shape. Preview is capped at 1 MiB, so a real request never legitimately
// takes more than seconds; a 5s deadline keeps a stuck fake from hanging CI.
func fetchPreview(t *testing.T, rawurl string) (*http.Response, []byte) {
	t.Helper()
	return fetchWithin(t, rawurl, 5*time.Second)
}

func decodeSuccess(t *testing.T, body []byte) podFilePreviewResponse {
	t.Helper()
	var got podFilePreviewResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode success: %v (body %q)", err, body)
	}
	return got
}

func decodeError(t *testing.T, body []byte) podFilePreviewErrorResponse {
	t.Helper()
	var got podFilePreviewErrorResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode error: %v (body %q)", err, body)
	}
	return got
}

func TestHandlePodFilePreviewReturnsTextFileContent(t *testing.T) {
	payload := []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cfg\n")

	ts := previewServer(t, func(t *testing.T, req execRequest, opts remotecommand.StreamOptions) error {
		if !req.isTar() {
			t.Fatalf("expected the tar attempt, got %q", req.script())
		}
		writeArchive(t, opts.Stdout, "config.yaml", payload, -1)
		if opts.Stdin != nil {
			_, _ = io.Copy(io.Discard, opts.Stdin) // release the guard
		}
		return nil
	})

	resp, body := fetchPreview(t, previewURL(ts, "/etc/cfg/config.yaml"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200 (body %q)", resp.StatusCode, body)
	}
	got := decodeSuccess(t, body)
	if got.Content != string(payload) {
		t.Errorf("content = %q, want %q", got.Content, payload)
	}
	if got.Code != "" {
		t.Errorf("code = %q, want empty on a normal text file", got.Code)
	}
	if got.Size != int64(len(payload)) {
		t.Errorf("size = %d, want %d", got.Size, len(payload))
	}
}

// The size gate must fire on the tar header alone — before any body is
// pulled. Writing a valid archive with the oversized header and then closing
// stdout without a body is exactly the shape that would hang a naive handler
// that reads `size` bytes; the size gate must fire first.
func TestHandlePodFilePreviewRejectsOversizedFile(t *testing.T) {
	oversized := int64(podFilePreviewByteCap + 42)
	payload := bytes.Repeat([]byte{'x'}, int(oversized))

	ts := previewServer(t, func(t *testing.T, req execRequest, opts remotecommand.StreamOptions) error {
		if !req.isTar() {
			t.Fatalf("expected the tar attempt, got %q", req.script())
		}
		writeArchive(t, opts.Stdout, "big.log", payload, -1)
		if opts.Stdin != nil {
			_, _ = io.Copy(io.Discard, opts.Stdin)
		}
		return nil
	})

	resp, body := fetchPreview(t, previewURL(ts, "/var/log/big.log"))
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 (body %q)", resp.StatusCode, body)
	}
	got := decodeError(t, body)
	if got.Code != previewCodeFileTooLarge {
		t.Errorf("code = %q, want file_too_large", got.Code)
	}
	if got.Size != oversized {
		t.Errorf("error size = %d, want %d — the UI needs the real size to say '12.3 MiB, too large'", got.Size, oversized)
	}
}

func TestHandlePodFilePreviewRejectsBinaryFile(t *testing.T) {
	payload := []byte{0x7F, 'E', 'L', 'F', 0x00, 0x01, 0x02, 0x03, 0x04, 0x05}

	ts := previewServer(t, func(t *testing.T, req execRequest, opts remotecommand.StreamOptions) error {
		writeArchive(t, opts.Stdout, "bin", payload, -1)
		if opts.Stdin != nil {
			_, _ = io.Copy(io.Discard, opts.Stdin)
		}
		return nil
	})

	resp, body := fetchPreview(t, previewURL(ts, "/usr/bin/bin"))
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415 (body %q)", resp.StatusCode, body)
	}
	got := decodeError(t, body)
	if got.Code != previewCodeBinaryFile {
		t.Errorf("code = %q, want binary_file", got.Code)
	}
	if got.MimeType == "" {
		t.Error("binary error must carry the detected mimeType so the UI can name it")
	}
	if got.Size != int64(len(payload)) {
		t.Errorf("error size = %d, want %d", got.Size, len(payload))
	}
}

func TestHandlePodFilePreviewReturnsEmptyFileAsSuccess(t *testing.T) {
	ts := previewServer(t, func(t *testing.T, req execRequest, opts remotecommand.StreamOptions) error {
		writeArchive(t, opts.Stdout, "empty", nil, -1)
		if opts.Stdin != nil {
			_, _ = io.Copy(io.Discard, opts.Stdin)
		}
		return nil
	})

	resp, body := fetchPreview(t, previewURL(ts, "/tmp/empty"))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", resp.StatusCode, body)
	}
	got := decodeSuccess(t, body)
	if got.Code != previewCodeEmptyFile {
		t.Errorf("code = %q, want empty_file", got.Code)
	}
	if got.Content != "" {
		t.Errorf("content = %q, want empty", got.Content)
	}
	if got.Size != 0 {
		t.Errorf("size = %d, want 0", got.Size)
	}
}

func TestHandlePodFilePreviewReportsPermissionDenied(t *testing.T) {
	ts := previewServer(t, func(t *testing.T, req execRequest, opts remotecommand.StreamOptions) error {
		_, _ = io.WriteString(opts.Stderr, "tar: /etc/shadow: Permission denied")
		return k8sexec.CodeExitError{Err: fmt.Errorf("command terminated with exit code 2"), Code: 2}
	})

	resp, body := fetchPreview(t, previewURL(ts, "/etc/shadow"))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (body %q)", resp.StatusCode, body)
	}
	got := decodeError(t, body)
	if got.Code != previewCodePermissionDenied {
		t.Errorf("code = %q, want permission_denied", got.Code)
	}
}

func TestHandlePodFilePreviewReportsMissingFile(t *testing.T) {
	ts := previewServer(t, func(t *testing.T, req execRequest, opts remotecommand.StreamOptions) error {
		_, _ = io.WriteString(opts.Stderr, "tar: /nope: No such file or directory")
		return k8sexec.CodeExitError{Err: fmt.Errorf("command terminated with exit code 2"), Code: 2}
	})

	resp, body := fetchPreview(t, previewURL(ts, "/nope"))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %q)", resp.StatusCode, body)
	}
	got := decodeError(t, body)
	if got.Code != previewCodeNotFound {
		t.Errorf("code = %q, want not_found", got.Code)
	}
}

func TestHandlePodFilePreviewRejectsBadPath(t *testing.T) {
	// The size gate would have caught this too, but the path validator fires
	// first and returns a 400 without any exec. previewServer is unused here.
	ts := previewServer(t, func(t *testing.T, req execRequest, opts remotecommand.StreamOptions) error {
		t.Error("no exec should run for an empty path")
		return nil
	})

	resp, body := fetchPreview(t, ts.URL+"/api/pods/ns/pod/file?container=app&path=")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %q)", resp.StatusCode, body)
	}
}
