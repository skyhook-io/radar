//go:build !windows

package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/skyhook-io/radar/internal/k8s"
)

func TestLocalTerminalReportsExportedContextBeforeOutput(t *testing.T) {
	for _, isolated := range []bool{true, false} {
		t.Run(map[bool]string{true: "isolated", false: "fallback"}[isolated], func(t *testing.T) {
			t.Cleanup(k8s.SetTestLocalMode())
			previousDisabled := k8s.ForceDisableLocalTerminal
			k8s.ForceDisableLocalTerminal = false
			t.Cleanup(func() { k8s.ForceDisableLocalTerminal = previousDisabled })
			dir := t.TempDir()
			shell := filepath.Join(dir, "fixture-shell")
			if err := os.WriteFile(shell, []byte("#!/bin/sh\nprintf 'fixture output\\n'\ncat\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("SHELL", shell)
			if isolated {
				t.Cleanup(k8s.SetTestProfileSource(filepath.Join(dir, "absent.yaml"), "production", "demo-user"))
			} else {
				t.Cleanup(k8s.SetTestProfileSource("", "missing", ""))
				t.Cleanup(k8s.SetTestRegistryEntry("other", filepath.Join(dir, "absent.yaml"), "other"))
			}
			server := &Server{listenAddress: DefaultListenAddress}
			httpServer := httptest.NewServer(http.HandlerFunc(server.handleLocalTerminal))
			defer httpServer.Close()
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http")+"/api/local-terminal", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			var info localTermSessionInfo
			if err := conn.ReadJSON(&info); err != nil {
				t.Fatal(err)
			}
			want := localTermSessionInfo{Type: "session", KubeconfigIsolated: isolated}
			if isolated {
				want.Context = "production"
			}
			if info != want {
				t.Fatalf("session info = %+v, want %+v", info, want)
			}
			var output TerminalMessage
			if err := conn.ReadJSON(&output); err != nil {
				t.Fatal(err)
			}
			if output.Type != "output" || !strings.Contains(output.Data, "fixture output") {
				t.Fatalf("output after metadata = %+v", output)
			}
		})
	}
}
