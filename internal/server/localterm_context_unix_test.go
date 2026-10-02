//go:build !windows

package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/skyhook-io/radar/internal/k8s"
)

func TestLocalTerminalReportsExportedContextBeforeOutput(t *testing.T) {
	for _, tc := range []struct {
		name, context string
		isolated      bool
	}{
		{"isolated", "production", true},
		{"fallback", "missing", false},
		{"no active context", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolated := tc.isolated
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
				t.Cleanup(k8s.SetTestProfileSource("", tc.context, ""))
				t.Cleanup(k8s.SetTestRegistryEntry("other", filepath.Join(dir, "absent.yaml"), "other"))
			}
			server := &Server{listenAddress: DefaultListenAddress}
			httpServer := httptest.NewServer(http.HandlerFunc(server.handleLocalTerminal))
			defer httpServer.Close()
			expected := tc.context
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http")+"/api/local-terminal?expectedContext="+url.QueryEscape(expected), nil)
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

func TestLocalTerminalContextGuardBeforeShell(t *testing.T) {
	t.Cleanup(k8s.SetTestLocalMode())
	previousDisabled := k8s.ForceDisableLocalTerminal
	k8s.ForceDisableLocalTerminal = false
	t.Cleanup(func() { k8s.ForceDisableLocalTerminal = previousDisabled })
	dir := t.TempDir()
	t.Cleanup(k8s.SetTestProfileSource(filepath.Join(dir, "absent.yaml"), "production@secondary", "demo-user"))
	t.Setenv("TMPDIR", dir)
	marker := filepath.Join(dir, "shell-started")
	shell := filepath.Join(dir, "fixture-shell")
	if err := os.WriteFile(shell, []byte("#!/bin/sh\ntouch \"$GUARD_SHELL_MARKER\"\nprintf 'fixture output\\n'\ncat\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", shell)
	t.Setenv("GUARD_SHELL_MARKER", marker)
	server := &Server{listenAddress: DefaultListenAddress}
	httpServer := httptest.NewServer(http.HandlerFunc(server.handleLocalTerminal))
	defer httpServer.Close()

	for _, tc := range []struct {
		name, expected, origin string
		status                 int
	}{
		{"missing", "", "", http.StatusBadRequest},
		{"empty intent with active context", "", "", http.StatusConflict},
		{"different context", "staging", "", http.StatusConflict},
		{"same short name", "production@primary", "", http.StatusConflict},
		{"cross origin", "production@secondary", "http://attacker.example", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := http.Header{}
			if tc.origin != "" {
				headers.Set("Origin", tc.origin)
			}
			requestURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/api/local-terminal"
			if tc.name != "missing" {
				requestURL += "?expectedContext=" + url.QueryEscape(tc.expected)
			}
			conn, resp, err := websocket.DefaultDialer.Dial(requestURL, headers)
			if conn != nil {
				conn.Close()
				t.Fatal("rejected context opened a WebSocket")
			}
			if err == nil || resp == nil || resp.StatusCode != tc.status {
				t.Fatalf("handshake: response=%v error=%v, want %d", resp, err, tc.status)
			}
			resp.Body.Close()
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("rejected request started the shell: %v", err)
			}
		})
	}

	t.Run("matching plain request cleans exported kubeconfig", func(t *testing.T) {
		resp, err := http.Get(httpServer.URL + "/api/local-terminal?expectedContext=production%40secondary")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("plain GET status = %d", resp.StatusCode)
		}
		files, err := filepath.Glob(filepath.Join(dir, "radar-kubeconfig-*.yaml"))
		if err != nil || len(files) != 0 {
			t.Fatalf("kubeconfigs after failed upgrade = %v, error = %v", files, err)
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("failed upgrade started a shell: %v", err)
		}
	})

	oldStatus := k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateDisconnected, Context: "staging", ErrorType: "auth"})
	t.Cleanup(func() { k8s.SetConnectionStatus(oldStatus) })
	conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http")+"/api/local-terminal?expectedContext=staging", nil)
	if conn != nil {
		conn.Close()
		t.Fatal("failed switch silently opened the previous active context")
	}
	if err == nil || resp == nil || resp.StatusCode != http.StatusConflict {
		t.Fatalf("failed switch: response=%v error=%v", resp, err)
	}
	resp.Body.Close()

	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateDisconnected, Context: k8s.GetContextName(), ErrorType: "config"})
	conn, _, err = websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http")+"/api/local-terminal?expectedContext=production%40secondary", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var info localTermSessionInfo
	if err := conn.ReadJSON(&info); err != nil {
		t.Fatal(err)
	}
	if info.Context != "production@secondary" || !info.KubeconfigIsolated {
		t.Fatalf("matching disconnected context did not open with cached config: %+v", info)
	}
	var output TerminalMessage
	if err := conn.ReadJSON(&output); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("accepted request did not start the shell: %v", err)
	}
}
