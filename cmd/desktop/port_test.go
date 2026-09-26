package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	held := func(k portOwnerKind) func(int) portOwnerKind { return func(int) portOwnerKind { return k } }

	rememberDesktopPort(path, 0, 51000, held(ownerOther))
	if got := lastDesktopPort(path); got != 51000 {
		t.Fatalf("first launch: recorded %d, want 51000", got)
	}
	rememberDesktopPort(path, 51000, 52000, held(ownerRadar))
	if got := lastDesktopPort(path); got != 51000 {
		t.Fatalf("a second window fell back: recorded %d, want the remembered 51000 kept", got)
	}
	rememberDesktopPort(path, 51000, 52000, held(ownerUnknown))
	if got := lastDesktopPort(path); got != 51000 {
		t.Fatalf("no clear answer on the remembered port: recorded %d, want 51000 kept", got)
	}
	rememberDesktopPort(path, 51000, 53000, held(ownerOther))
	if got := lastDesktopPort(path); got != 53000 {
		t.Fatalf("remembered port held by something else: recorded %d, want 53000", got)
	}
}

func TestPortOwner(t *testing.T) {
	port := func(addr net.Addr) int { return addr.(*net.TCPAddr).Port }

	radar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/connection" {
			http.NotFound(w, r)
			return
		}
		// Radar lists every kubeconfig context unless asked not to; a large
		// kubeconfig would otherwise push the reply past the probe's read limit.
		if r.URL.Query().Get("contexts") != "0" {
			_, _ = w.Write([]byte(`{"state":"connected","contexts":[` + strings.Repeat(`{"name":"ctx"},`, 10000) + `{"name":"last"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"state":"connected"}`))
	}))
	defer radar.Close()
	if got := portOwner(port(radar.Listener.Addr())); got != ownerRadar {
		t.Errorf("Radar: got %v, want ownerRadar", got)
	}

	other := httptest.NewServer(http.NotFoundHandler())
	defer other.Close()
	if got := portOwner(port(other.Listener.Addr())); got != ownerOther {
		t.Errorf("unrelated HTTP server: got %v, want ownerOther", got)
	}

	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	go func() {
		for {
			c, err := raw.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte("SSH-2.0-not-http\r\n"))
			c.Close()
		}
	}()
	if got := portOwner(port(raw.Addr())); got != ownerOther {
		t.Errorf("non-HTTP service: got %v, want ownerOther", got)
	}

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second)
	}))
	defer slow.Close()
	if got := portOwner(port(slow.Listener.Addr())); got != ownerUnknown {
		t.Errorf("server too slow to answer: got %v, want ownerUnknown", got)
	}

	free, _ := net.Listen("tcp", "127.0.0.1:0")
	freePort := port(free.Addr())
	free.Close()
	if got := portOwner(freePort); got != ownerUnknown {
		t.Errorf("free port: got %v, want ownerUnknown", got)
	}
}
