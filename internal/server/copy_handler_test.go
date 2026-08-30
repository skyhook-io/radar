package server

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
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

// These tests drive the real routes and the real handlers. Only the exec client
// is stood in for, because everything this change fixes lives between the tar
// header and the response body: the size the archive declares, the bytes that
// follow it, when stdin is released, and what the client is told when those
// disagree. None of that is reachable from a test of the pieces.
//
// What they do NOT cover: the shell scripts, which no Go test can execute, and
// the Kubernetes teardown behaviour that made the file arrive short in the first
// place. Both are verified against a live cluster by hand. Green here means the
// wiring is right, not that a download works.

// fakeExecutor stands in for the cluster. respond receives the same streams
// client-go would hand the real exec, so a test can write an archive, read the
// stdin the drain guard depends on, and then report whatever status it likes.
type fakeExecutor struct {
	respond func(t *testing.T, opts remotecommand.StreamOptions) error
	t       *testing.T
}

func (f *fakeExecutor) Stream(opts remotecommand.StreamOptions) error {
	return f.StreamWithContext(context.Background(), opts)
}

func (f *fakeExecutor) StreamWithContext(ctx context.Context, opts remotecommand.StreamOptions) error {
	return f.respond(f.t, opts)
}

// execRequest is what a test matches on to tell the transfer attempts apart:
// the guarded tar first, then the framed cat, then the bare cat.
type execRequest struct {
	command []string
}

func (e execRequest) script() string {
	if len(e.command) == 3 && e.command[0] == "/bin/sh" {
		return e.command[2]
	}
	return strings.Join(e.command, " ")
}

func (e execRequest) isTar() bool  { return strings.Contains(e.script(), "tar cfh") }
func (e execRequest) isCat() bool  { return strings.Contains(e.script(), "wc -c <") }
func (e execRequest) isBare() bool { return len(e.command) > 0 && e.command[0] == "cat" }

// downloadServer mounts the real routes with a fake exec client behind them.
func downloadServer(t *testing.T, srv *Server, respond func(t *testing.T, req execRequest, opts remotecommand.StreamOptions) error) *httptest.Server {
	t.Helper()

	// The handlers refuse to start unless the cluster looks connected and both a
	// client and a config resolve, so all three seams have to be published.
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

	srv.newExecutor = func(_ *rest.Config, u *url.URL) (remotecommand.Executor, error) {
		return &fakeExecutor{t: t, respond: func(t *testing.T, opts remotecommand.StreamOptions) error {
			return respond(t, execRequest{command: u.Query()["command"]}, opts)
		}}, nil
	}

	router := chi.NewRouter()
	router.Get("/api/pods/{namespace}/{name}/files/download", srv.handlePodFileDownload)
	router.Post("/api/pods/{namespace}/{name}/files/save", srv.handlePodFileSave)
	ts := httptest.NewServer(router)
	t.Cleanup(ts.Close)
	return ts
}

func downloadURL(ts *httptest.Server, path string) string {
	return ts.URL + "/api/pods/ns/pod/files/download?container=app&path=" + url.QueryEscape(path)
}

// writeArchive writes a one-file tar to w, keeping only the first keepBytes when
// keepBytes is non-negative, which is how Kubernetes hands back a transfer whose
// tail was dropped.
func writeArchive(t *testing.T, w io.Writer, name string, payload []byte, keepBytes int) {
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
	if _, err := w.Write(out); err != nil && err != io.ErrClosedPipe {
		t.Logf("archive write ended: %v", err)
	}
}

// get runs the request under a deadline, so a transfer that parks fails the test
// instead of hanging the suite - which is exactly how the shrinking-file bug
// behaved before it was fixed.
func fetchRaw(t *testing.T, rawurl string, within time.Duration) (*http.Response, []byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawurl, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request did not complete within %s: %v", within, err)
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(resp.Body)
	return resp, body, readErr
}

func fetchWithin(t *testing.T, rawurl string, within time.Duration) (*http.Response, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), within)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawurl, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request did not complete within %s: %v", within, err)
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return resp, body // a torn response is a result, not a failure
	}
	return resp, body
}

func TestPodFileDownloadServesTheWholeFile(t *testing.T) {
	payload := bytes.Repeat([]byte("radar"), 4096)
	ts := downloadServer(t, &Server{}, func(t *testing.T, req execRequest, opts remotecommand.StreamOptions) error {
		if !req.isTar() {
			t.Errorf("expected the tar attempt, got %q", req.script())
		}
		writeArchive(t, opts.Stdout, "app.log", payload, -1)
		if opts.Stdin != nil {
			_, _ = io.Copy(io.Discard, opts.Stdin) // the guard: wait to be released
		}
		return nil
	})

	resp, body := fetchWithin(t, downloadURL(ts, "/var/log/app.log"), 10*time.Second)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Content-Length"); got != strconv.Itoa(len(payload)) {
		t.Errorf("Content-Length = %q, want %d", got, len(payload))
	}
	if !strings.Contains(resp.Header.Get("Content-Disposition"), `filename="app.log"`) {
		t.Errorf("Content-Disposition = %q", resp.Header.Get("Content-Disposition"))
	}
	if !bytes.Equal(body, payload) {
		t.Errorf("served %d bytes, want %d identical bytes", len(body), len(payload))
	}
}

// A short transfer must never reach the client as a complete response. The
// declared Content-Length is what makes that true: without it the handler would
// answer a cleanly terminated body that happens to be short, and the client
// would store it believing the download succeeded.
func TestPodFileDownloadTruncatedArchiveDoesNotLookComplete(t *testing.T) {
	payload := bytes.Repeat([]byte("radar"), 4096)
	var full bytes.Buffer
	writeArchive(t, &full, "app.log", payload, -1)

	ts := downloadServer(t, &Server{}, func(t *testing.T, req execRequest, opts remotecommand.StreamOptions) error {
		writeArchive(t, opts.Stdout, "app.log", payload, full.Len()-2048)
		return nil // success, exactly as the real teardown reports it
	})

	resp, body, readErr := fetchRaw(t, downloadURL(ts, "/var/log/app.log"), 10*time.Second)
	if int64(len(body)) == int64(len(payload)) {
		t.Fatal("a truncated transfer was served as a complete file")
	}
	if resp.Header.Get("Content-Length") != strconv.Itoa(len(payload)) {
		t.Errorf("Content-Length = %q, want the size the archive declared (%d)", resp.Header.Get("Content-Length"), len(payload))
	}
	// Reading the body must fail. A clean read of a short body is the corrupt
	// download this endpoint must never produce.
	if readErr == nil {
		t.Errorf("client read %d bytes of a declared %d with no error, so it would save a corrupt file", len(body), len(payload))
	}
}

// The desktop save is where a missed short read does real damage: the bytes are
// already on the user's disk, and without the size check they are renamed into
// place and reported as a finished download.
func TestPodFileSaveRefusesToFinishATruncatedTransfer(t *testing.T) {
	payload := bytes.Repeat([]byte("radar"), 4096)
	var full bytes.Buffer
	writeArchive(t, &full, "app.log", payload, -1)

	var saved []byte
	var savedName string
	srv := &Server{saveFileStreamFunc: func(name string, r io.Reader) (string, error) {
		savedName = name
		var err error
		saved, err = io.ReadAll(r) // the desktop app copies to a partial file exactly like this
		if err != nil {
			return "", err // and removes it, reporting the failure
		}
		return "/tmp/" + name, nil
	}}

	ts := downloadServer(t, srv, func(t *testing.T, req execRequest, opts remotecommand.StreamOptions) error {
		writeArchive(t, opts.Stdout, "app.log", payload, full.Len()-2048)
		return nil
	})

	saveURL := ts.URL + "/api/pods/ns/pod/files/save?container=app&path=" + url.QueryEscape("/var/log/app.log")
	req, err := http.NewRequest(http.MethodPost, saveURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("save request failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusOK {
		t.Fatalf("a truncated transfer was reported as saved (%q); the user has a corrupt file and no way to know", body)
	}
	if len(saved) == len(payload) {
		t.Errorf("the callback received a complete file from a truncated transfer")
	}
	_ = savedName
}

// On the cat path there is no archive to notice the loss, only the byte count
// the container announced, so the explicit size check is the whole defence. A
// short read here must never be finished and reported as a saved file.
func TestPodFileSaveRefusesToFinishAShortCatTransfer(t *testing.T) {
	payload := []byte("the container announced more of this file than it sent")

	var saved []byte
	srv := &Server{saveFileStreamFunc: func(name string, r io.Reader) (string, error) {
		var err error
		saved, err = io.ReadAll(r)
		if err != nil {
			return "", err
		}
		return "/tmp/" + name, nil
	}}

	ts := downloadServer(t, srv, func(t *testing.T, req execRequest, opts remotecommand.StreamOptions) error {
		switch {
		case req.isTar():
			_, _ = io.WriteString(opts.Stderr, "sh: tar: not found")
			return k8sexec.CodeExitError{Err: fmt.Errorf("command terminated with exit code 127"), Code: 127}
		case req.isCat():
			// Announce the whole file, then send part of it and exit clean - the
			// shape a file that shrank mid-read produces. Deliberately does NOT
			// wait on stdin: the script re-measures the file and declines to arm
			// the drain guard when it came up short, which is what keeps a short
			// transfer from parking instead of failing.
			fmt.Fprintf(opts.Stdout, "%d\n", len(payload))
			_, _ = opts.Stdout.Write(payload[:len(payload)-20])
			return nil
		default:
			t.Errorf("unexpected attempt %q", req.script())
			return fmt.Errorf("unexpected command")
		}
	})

	saveURL := ts.URL + "/api/pods/ns/pod/files/save?container=app&path=" + url.QueryEscape("/var/log/app.log")
	req, err := http.NewRequest(http.MethodPost, saveURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("save request failed: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusOK {
		t.Fatalf("a short transfer was reported as saved (%q); nothing else would have caught it", body)
	}
	if len(saved) == len(payload) {
		t.Errorf("the callback was handed a complete file from a short transfer")
	}
}

// The transfer must not release the remote process until it holds every byte.
// Releasing early is what let Kubernetes drop the tail.
func TestPodFileDownloadReleasesTheDrainGuardOnlyAfterTheFullRead(t *testing.T) {
	payload := bytes.Repeat([]byte("radar"), 8192)
	var full bytes.Buffer
	writeArchive(t, &full, "app.log", payload, -1)
	archive := full.Bytes()
	held := archive[len(archive)-1024:]

	ts := downloadServer(t, &Server{}, func(t *testing.T, req execRequest, opts remotecommand.StreamOptions) error {
		if opts.Stdin == nil {
			t.Error("the transfer opened no stdin, so nothing can hold the process open")
			return fmt.Errorf("no stdin stream")
		}
		if _, err := opts.Stdout.Write(archive[:len(archive)-1024]); err != nil {
			return err
		}
		// Withhold the tail until stdin closes. If the server released the guard
		// before reading everything, this never returns and the deadline fires.
		_, _ = io.Copy(io.Discard, opts.Stdin)
		_, _ = opts.Stdout.Write(held)
		return nil
	})

	resp, body := fetchWithin(t, downloadURL(ts, "/var/log/app.log"), 10*time.Second)
	if resp.StatusCode != http.StatusOK || !bytes.Equal(body, payload) {
		t.Fatalf("status %d, %d bytes: the guard was released before the file was whole", resp.StatusCode, len(body))
	}
}

// A container without tar still has to deliver the file, through the framed cat
// path, and that path has to be reached by the exit status rather than by
// matching the wording of a message.
func TestPodFileDownloadFallsBackToCatWhenTarIsMissing(t *testing.T) {
	payload := []byte("no tar in this container, but the file still arrives")
	var sawTar, sawCat bool

	ts := downloadServer(t, &Server{}, func(t *testing.T, req execRequest, opts remotecommand.StreamOptions) error {
		switch {
		case req.isTar():
			sawTar = true
			_, _ = io.WriteString(opts.Stderr, "sh: tar: not found")
			return k8sexec.CodeExitError{Err: fmt.Errorf("command terminated with exit code 127"), Code: 127}
		case req.isCat():
			sawCat = true
			fmt.Fprintf(opts.Stdout, "%d\n", len(payload))
			_, _ = opts.Stdout.Write(payload)
			if opts.Stdin != nil {
				_, _ = io.Copy(io.Discard, opts.Stdin)
			}
			return nil
		default:
			t.Errorf("unexpected attempt %q", req.script())
			return fmt.Errorf("unexpected command")
		}
	})

	resp, body := fetchWithin(t, downloadURL(ts, "/var/log/app.log"), 10*time.Second)
	if !sawTar || !sawCat {
		t.Fatalf("expected tar then cat; sawTar=%v sawCat=%v", sawTar, sawCat)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", resp.StatusCode, body)
	}
	if !bytes.Equal(body, payload) {
		t.Errorf("served %q, want %q", body, payload)
	}
}
