package server

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestResolveCompressLevel(t *testing.T) {
	cases := []struct {
		raw  string
		want int
	}{
		{"", defaultCompressLevel},
		{"0", 0},
		{"1", 1},
		{"9", 9},
		{"5", 5},
		{"10", defaultCompressLevel},   // out of range
		{"-1", defaultCompressLevel},   // negative
		{"fast", defaultCompressLevel}, // non-numeric
	}
	for _, tc := range cases {
		if got := resolveCompressLevel(tc.raw); got != tc.want {
			t.Errorf("resolveCompressLevel(%q) = %d, want %d", tc.raw, got, tc.want)
		}
	}
}

func TestCompressMiddleware(t *testing.T) {
	t.Setenv("RADAR_COMPRESS_LEVEL", "1")
	cm := compressMiddleware()
	if cm == nil {
		t.Fatal("expected middleware at level 1, got nil")
	}

	r := chi.NewRouter()
	r.Use(cm)
	big := strings.Repeat(`{"k":"vvvvvvvvvv"},`, 500)
	r.Get("/json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "["+big+`{"k":"v"}]`)
	})
	r.Get("/sse", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+big+"\n\n")
	})

	// JSON path: compressed and round-trips.
	t.Run("json is gzipped", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/json", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
			t.Fatalf("Content-Encoding = %q, want gzip", got)
		}
		zr, err := gzip.NewReader(rec.Body)
		if err != nil {
			t.Fatalf("gzip.NewReader: %v", err)
		}
		out, err := io.ReadAll(zr)
		if err != nil {
			t.Fatalf("decompress: %v", err)
		}
		if !strings.HasPrefix(string(out), "[") {
			t.Fatalf("decompressed body unexpected: %.20q", out)
		}
	})

	// SSE path: never compressed (text/event-stream excluded), flush intact.
	t.Run("sse is not gzipped", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/sse", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if got := rec.Header().Get("Content-Encoding"); got != "" {
			t.Fatalf("SSE Content-Encoding = %q, want empty", got)
		}
		if !strings.HasPrefix(rec.Body.String(), "data: ") {
			t.Fatalf("SSE body should be plaintext, got %.20q", rec.Body.String())
		}
	})
}

func TestCompressMiddlewareDisabled(t *testing.T) {
	t.Setenv("RADAR_COMPRESS_LEVEL", "0")
	if cm := compressMiddleware(); cm != nil {
		t.Fatal("expected nil middleware when RADAR_COMPRESS_LEVEL=0")
	}
}

func TestIsStreamingRequest(t *testing.T) {
	mk := func(path string, h map[string]string) *http.Request {
		if path == "" {
			path = "/x"
		}
		r := httptest.NewRequest(http.MethodGet, path, nil)
		for k, v := range h {
			r.Header.Set(k, v)
		}
		return r
	}
	cases := []struct {
		name string
		path string
		hdr  map[string]string
		want bool
	}{
		{"plain json", "/api/resources", map[string]string{"Accept": "application/json"}, false},
		{"sse", "/api/events/stream", map[string]string{"Accept": "text/event-stream"}, true},
		{"stream route with */* accept", "/api/pods/n/p/logs/stream", map[string]string{"Accept": "*/*"}, true}, // path-based
		{"mcp dual accept", "/mcp", map[string]string{"Accept": "application/json, text/event-stream"}, true},
		{"websocket upgrade", "/api/pods/n/p/exec", map[string]string{"Upgrade": "websocket", "Connection": "Upgrade"}, true},
		{"connection upgrade only", "/api/local-terminal", map[string]string{"Connection": "keep-alive, Upgrade"}, true},
		{"no headers plain route", "/api/resources", map[string]string{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isStreamingRequest(mk(tc.path, tc.hdr)); got != tc.want {
				t.Errorf("isStreamingRequest(%s, %v) = %v, want %v", tc.path, tc.hdr, got, tc.want)
			}
		})
	}
}
