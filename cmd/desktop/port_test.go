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
	bindable := func(ok bool) func(int) bool { return func(int) bool { return ok } }
	remember := func(remembered, actual int, owner portOwnerKind, canBind bool) int {
		rememberDesktopPort(path, remembered, actual, held(owner), bindable(canBind))
		return lastDesktopPort(path)
	}

	if got := remember(0, 51000, ownerOther, false); got != 51000 {
		t.Fatalf("first launch: recorded %d, want 51000", got)
	}
	cases := []struct {
		name    string
		owner   portOwnerKind
		canBind bool
		want    int
	}{
		{"a second window holds it", ownerRadar, false, 51000},
		{"no answer in time", ownerUnknown, false, 51000},
		{"nothing there, and it binds again", ownerSilent, true, 51000},
		{"nothing there, and it still can't be bound", ownerSilent, false, 52000},
		{"something else answers", ownerOther, false, 52000},
	}
	for _, c := range cases {
		recordDesktopPort(path, 51000)
		if got := remember(51000, 52000, c.owner, c.canBind); got != c.want {
			t.Errorf("%s: recorded %d, want %d", c.name, got, c.want)
		}
	}
}

func TestLoopbackPortBindable(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if loopbackPortBindable(held.Addr().(*net.TCPAddr).Port) {
		t.Error("a held port reported bindable")
	}
	free, _ := net.Listen("tcp", "127.0.0.1:0")
	port := free.Addr().(*net.TCPAddr).Port
	free.Close()
	if !loopbackPortBindable(port) {
		t.Error("a free port reported unbindable")
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
	if got := portOwner(freePort); got != ownerSilent {
		t.Errorf("free port: got %v, want ownerSilent", got)
	}
}
