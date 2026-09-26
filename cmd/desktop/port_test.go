package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestDesktopPortRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".radar", desktopPortFile)
	if got := lastDesktopPort(path); got != 0 {
		t.Fatalf("lastDesktopPort before any launch = %d, want 0", got)
	}
	recordDesktopPort(path, 54321)
	if got := lastDesktopPort(path); got != 54321 {
		t.Fatalf("lastDesktopPort = %d, want 54321", got)
	}
	recordDesktopPort(path, 60000)
	if got := lastDesktopPort(path); got != 60000 {
		t.Fatalf("lastDesktopPort after a new port = %d, want 60000", got)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "*.tmp")); len(leftovers) != 0 {
		t.Fatalf("temp files left behind: %v", leftovers)
	}
}

func TestLastDesktopPortIgnoresGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), desktopPortFile)
	for _, content := range []string{"", "abc", "0", "-1", "70000"} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := lastDesktopPort(path); got != 0 {
			t.Errorf("lastDesktopPort(%q) = %d, want 0", content, got)
		}
	}
}

func TestRememberDesktopPort(t *testing.T) {
	path := filepath.Join(t.TempDir(), desktopPortFile)
	radar := func(int) bool { return true }
	other := func(int) bool { return false }

	rememberDesktopPort(path, 0, 51000, other)
	if got := lastDesktopPort(path); got != 51000 {
		t.Fatalf("first launch: recorded %d, want 51000", got)
	}
	rememberDesktopPort(path, 51000, 52000, radar)
	if got := lastDesktopPort(path); got != 51000 {
		t.Fatalf("a second window fell back: recorded %d, want the remembered 51000 kept", got)
	}
	rememberDesktopPort(path, 51000, 53000, other)
	if got := lastDesktopPort(path); got != 53000 {
		t.Fatalf("remembered port held by something else: recorded %d, want 53000", got)
	}
}

func TestRadarServingOn(t *testing.T) {
	radar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/capabilities" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"deployment":{"mode":"local"}}`))
	}))
	defer radar.Close()
	other := httptest.NewServer(http.NotFoundHandler())
	defer other.Close()
	port := func(s *httptest.Server) int { return s.Listener.Addr().(*net.TCPAddr).Port }

	if !radarServingOn(port(radar)) {
		t.Error("Radar's capabilities endpoint not recognized")
	}
	if radarServingOn(port(other)) {
		t.Error("an unrelated server was taken for Radar")
	}
	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	freePort := closed.Addr().(*net.TCPAddr).Port
	closed.Close()
	if radarServingOn(freePort) {
		t.Error("a free port was taken for Radar")
	}
}
