package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newDeliverRequest builds a request for deliverPodFile with the given query.
func newDeliverRequest(query string) *http.Request {
	return httptest.NewRequest(http.MethodGet, "/api/pods/ns/pod/files/download?"+query, nil)
}

func TestDeliverPodFileStreamsToClientByDefault(t *testing.T) {
	s := &Server{}
	w := httptest.NewRecorder()

	s.deliverPodFile(w, newDeliverRequest("path=/tmp/a.txt"), strings.NewReader("hello world"), "a.txt", 11, "ns", "pod", "/tmp/a.txt")

	res := w.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if got := res.Header.Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream", got)
	}
	if got := res.Header.Get("Content-Length"); got != "11" {
		t.Errorf("Content-Length = %q, want 11", got)
	}
	body, _ := io.ReadAll(res.Body)
	if string(body) != "hello world" {
		t.Errorf("body = %q, want %q", body, "hello world")
	}
}

// save=native must be inert unless the desktop app installed a stream func —
// a server or in-cluster deployment must never write downloads to local disk.
func TestDeliverPodFileIgnoresNativeSaveWithoutStreamFunc(t *testing.T) {
	s := &Server{}
	w := httptest.NewRecorder()

	s.deliverPodFile(w, newDeliverRequest("path=/tmp/a.txt&save=native"), strings.NewReader("hello world"), "a.txt", 11, "ns", "pod", "/tmp/a.txt")

	res := w.Result()
	if got := res.Header.Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("Content-Type = %q, want the file to be streamed to the client", got)
	}
	body, _ := io.ReadAll(res.Body)
	if string(body) != "hello world" {
		t.Errorf("body = %q, want %q", body, "hello world")
	}
}

func TestDeliverPodFileNativeSaveWritesToDisk(t *testing.T) {
	var gotName string
	var gotData []byte
	s := &Server{saveStreamFunc: func(name string, src io.Reader) (string, error) {
		gotName = name
		var err error
		gotData, err = io.ReadAll(src)
		return "/home/u/Downloads/" + name, err
	}}
	w := httptest.NewRecorder()

	// Trailing bytes stand in for the tar block padding that follows the file.
	s.deliverPodFile(w, newDeliverRequest("path=/tmp/a.txt&save=native"), strings.NewReader("hello world\x00\x00\x00"), "a.txt", 11, "ns", "pod", "/tmp/a.txt")

	res := w.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if got := res.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Path != "/home/u/Downloads/a.txt" {
		t.Errorf("path = %q, want /home/u/Downloads/a.txt", body.Path)
	}
	if gotName != "a.txt" {
		t.Errorf("saved name = %q, want a.txt", gotName)
	}
	if string(gotData) != "hello world" {
		t.Errorf("saved data = %q, want %q (padding must not be written)", gotData, "hello world")
	}
}

func TestDeliverPodFileNativeSaveRejectsTraversalFilename(t *testing.T) {
	called := false
	s := &Server{saveStreamFunc: func(string, io.Reader) (string, error) {
		called = true
		return "", nil
	}}
	w := httptest.NewRecorder()

	s.deliverPodFile(w, newDeliverRequest("path=/tmp/..&save=native"), strings.NewReader("x"), "..", 1, "ns", "pod", "/tmp/..")

	if called {
		t.Fatal("saveStreamFunc was called with an unusable filename")
	}
	if res := w.Result(); res.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", res.StatusCode)
	}
}

func TestDeliverPodFileNativeSaveReportsFailure(t *testing.T) {
	s := &Server{saveStreamFunc: func(string, io.Reader) (string, error) {
		return "", errors.New("disk full")
	}}
	w := httptest.NewRecorder()

	s.deliverPodFile(w, newDeliverRequest("path=/tmp/a.txt&save=native"), strings.NewReader("hello world"), "a.txt", 11, "ns", "pod", "/tmp/a.txt")

	res := w.Result()
	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), "disk full") {
		t.Errorf("body = %q, want it to name the underlying error", body)
	}
}

// save=native writes to the user's disk from a GET, which a page on another
// origin can fire without a preflight. Only the native branch is gated; the
// streaming path is an ordinary read.
func TestDeliverPodFileNativeSaveRejectsCrossOrigin(t *testing.T) {
	called := false
	s := &Server{saveStreamFunc: func(string, io.Reader) (string, error) {
		called = true
		return "/home/u/Downloads/a.txt", nil
	}}
	w := httptest.NewRecorder()

	req := newDeliverRequest("path=/tmp/a.txt&save=native")
	req.Header.Set("Origin", "https://evil.example.com")
	s.deliverPodFile(w, req, strings.NewReader("hello world"), "a.txt", 11, "ns", "pod", "/tmp/a.txt")

	if called {
		t.Fatal("saveStreamFunc was called for a cross-origin request")
	}
	if res := w.Result(); res.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403", res.StatusCode)
	}
}

// The desktop webview runs on the loopback server itself, and the Vite dev
// proxy forwards its own loopback origin.
func TestDeliverPodFileNativeSaveAllowsLoopbackOrigin(t *testing.T) {
	called := false
	s := &Server{saveStreamFunc: func(name string, src io.Reader) (string, error) {
		called = true
		_, err := io.ReadAll(src)
		return "/home/u/Downloads/" + name, err
	}}
	w := httptest.NewRecorder()

	req := newDeliverRequest("path=/tmp/a.txt&save=native")
	req.Header.Set("Origin", "http://localhost:9273")
	s.deliverPodFile(w, req, strings.NewReader("hello world"), "a.txt", 11, "ns", "pod", "/tmp/a.txt")

	if !called {
		t.Fatal("a loopback origin must still be able to save")
	}
	if res := w.Result(); res.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", res.StatusCode)
	}
}

// Reading a file cross-origin is not a side effect — the origin check must not
// leak onto the streaming path.
func TestDeliverPodFileCrossOriginStreamsWithoutNativeSave(t *testing.T) {
	s := &Server{saveStreamFunc: func(string, io.Reader) (string, error) {
		t.Fatal("saveStreamFunc must not be reached without save=native")
		return "", nil
	}}
	w := httptest.NewRecorder()

	req := newDeliverRequest("path=/tmp/a.txt")
	req.Header.Set("Origin", "https://evil.example.com")
	s.deliverPodFile(w, req, strings.NewReader("hello world"), "a.txt", 11, "ns", "pod", "/tmp/a.txt")

	res := w.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
	if got := res.Header.Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("Content-Type = %q, want application/octet-stream", got)
	}
	body, _ := io.ReadAll(res.Body)
	if string(body) != "hello world" {
		t.Errorf("body = %q, want %q", body, "hello world")
	}
}

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"a.txt", "a.txt"},
		{"/etc/passwd", "passwd"},
		{"../../etc/passwd", "passwd"},
		{"dir/sub/file.log", "file.log"},
		{"ev\x00il.txt", "evil.txt"},
		{"..", ""},
		{".", ""},
		{"", ""},
		{"/", ""},
	}
	for _, tt := range tests {
		if got := sanitizeFilename(tt.in); got != tt.want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
