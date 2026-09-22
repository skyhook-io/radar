package server

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/cloud"
)

func TestNormalizeListenAddress(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "zero value", want: DefaultListenAddress},
		{name: "ipv4 loopback", input: DefaultListenAddress, want: DefaultListenAddress},
		{name: "localhost", input: "localhost", want: DefaultListenAddress},
		{name: "all interfaces", input: AllInterfacesAddress, want: AllInterfacesAddress},
		{name: "explicit ipv4", input: "192.0.2.10", want: "192.0.2.10"},
		{name: "alternate loopback", input: "127.0.0.2", want: "127.0.0.2"},
		{name: "ipv6 loopback", input: "::1", want: "::1"},
		{name: "explicit ipv6", input: "2001:db8::10", want: "2001:db8::10"},
		{name: "ipv6 wildcard", input: "::", want: "::"},
		{name: "expanded ipv6", input: "0:0:0:0:0:0:0:1", want: "::1"},
		{name: "mapped loopback", input: "::ffff:127.0.0.1", want: DefaultListenAddress},
		{name: "mapped wildcard", input: "::ffff:0.0.0.0", want: AllInterfacesAddress},
		{name: "arbitrary hostname", input: "radar.internal", wantErr: true},
		{name: "uppercase localhost", input: "LOCALHOST", wantErr: true},
		{name: "bracketed ipv6", input: "[::1]", wantErr: true},
		{name: "host and port", input: "127.0.0.1:9280", wantErr: true},
		{name: "url", input: "http://127.0.0.1", wantErr: true},
		{name: "cidr", input: "192.0.2.0/24", wantErr: true},
		{name: "interface", input: "eth0", wantErr: true},
		{name: "ipv6 zone", input: "fe80::1%eth0", wantErr: true},
		{name: "whitespace", input: " 127.0.0.1", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeListenAddress(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NormalizeListenAddress(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("NormalizeListenAddress(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestSocketAddress(t *testing.T) {
	tests := []struct {
		name          string
		listenAddress string
		want          string
	}{
		{name: "loopback", listenAddress: DefaultListenAddress, want: "127.0.0.1:9280"},
		{name: "all interfaces uses dual-stack wildcard", listenAddress: AllInterfacesAddress, want: ":9280"},
		{name: "explicit ipv4", listenAddress: "192.0.2.10", want: "192.0.2.10:9280"},
		{name: "explicit ipv6", listenAddress: "2001:db8::10", want: "[2001:db8::10]:9280"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := socketAddress(tt.listenAddress, 9280); got != tt.want {
				t.Fatalf("socketAddress(%q, 9280) = %q, want %q", tt.listenAddress, got, tt.want)
			}
		})
	}
}

func TestNormalizePortForwardAddress(t *testing.T) {
	for input, want := range map[string]string{"": DefaultListenAddress, "localhost": DefaultListenAddress, DefaultListenAddress: DefaultListenAddress, AllInterfacesAddress: AllInterfacesAddress} {
		if got, err := normalizePortForwardAddress(input); err != nil || got != want {
			t.Errorf("normalizePortForwardAddress(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{"192.0.2.10", "127.0.0.2", "::1", "::", "radar.internal"} {
		if _, err := normalizePortForwardAddress(input); err == nil {
			t.Errorf("port-forward accepted unsupported address %q", input)
		}
	}
}

func TestServerListenAddress(t *testing.T) {
	tests := []struct {
		name          string
		listenAddress string
		wantLoopback  bool
		wantAny       bool
		wantHost      string
	}{
		{name: "zero value is loopback", wantLoopback: true, wantHost: "localhost"},
		{name: "explicit loopback", listenAddress: DefaultListenAddress, wantLoopback: true, wantHost: "localhost"},
		{name: "explicit all interfaces", listenAddress: AllInterfacesAddress, wantAny: true, wantHost: "localhost"},
		{name: "alternate loopback", listenAddress: "127.0.0.2", wantLoopback: true, wantHost: "127.0.0.2"},
		{name: "ipv6 loopback", listenAddress: "::1", wantLoopback: true, wantHost: "::1"},
		{name: "ipv6 wildcard", listenAddress: "::", wantAny: true, wantHost: "::1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := New(Config{Port: 0, ListenAddress: tt.listenAddress})
			ready := make(chan struct{})
			errCh := make(chan error, 1)
			go func() {
				errCh <- srv.StartWithReady(ready)
			}()

			select {
			case <-ready:
			case err := <-errCh:
				if tt.wantHost != "localhost" && (errors.Is(err, syscall.EADDRNOTAVAIL) || errors.Is(err, syscall.EAFNOSUPPORT) || errors.Is(err, syscall.EPROTONOSUPPORT)) {
					t.Skipf("address unavailable on this host: %v", err)
				}
				t.Fatalf("server failed to start: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("server did not become ready")
			}
			t.Cleanup(func() {
				srv.Stop()
				select {
				case err := <-errCh:
					if err != nil && !errors.Is(err, net.ErrClosed) {
						t.Errorf("server shutdown error: %v", err)
					}
				case <-time.After(5 * time.Second):
					t.Error("server did not stop")
				}
			})

			gotIP := srv.listener.Addr().(*net.TCPAddr).IP
			if gotIP.IsLoopback() != tt.wantLoopback {
				t.Errorf("listener IP %q loopback = %v, want %v", gotIP, gotIP.IsLoopback(), tt.wantLoopback)
			}
			if gotIP.IsUnspecified() != tt.wantAny {
				t.Errorf("listener IP %q unspecified = %v, want %v", gotIP, gotIP.IsUnspecified(), tt.wantAny)
			}
			if got := srv.ActualAddr(); got != net.JoinHostPort(tt.wantHost, strconv.Itoa(srv.ActualPort())) {
				t.Errorf("ActualAddr() = %q; want dialable host %q", got, tt.wantHost)
			}
			client := &http.Client{Transport: &http.Transport{}, Timeout: 5 * time.Second}
			t.Cleanup(client.CloseIdleConnections)
			resp, err := client.Get("http://" + srv.ActualAddr() + "/api/health")
			if err != nil {
				t.Fatalf("dial advertised address: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("health status = %d", resp.StatusCode)
			}
		})
	}
}

func TestServerRejectsInvalidListenAddress(t *testing.T) {
	srv := New(Config{Port: 0, ListenAddress: "radar.internal"})

	if err := srv.StartWithReady(nil); err == nil {
		t.Fatal("StartWithReady() error = nil, want invalid listen address error")
	} else if !strings.Contains(err.Error(), `invalid listen address "radar.internal"`) {
		t.Fatalf("StartWithReady() error = %q, want rejected address", err)
	}
	if srv.listener != nil {
		t.Fatalf("StartWithReady() listener = %v, want nil after validation failure", srv.listener)
	}
}

func TestServerBindFailureIncludesAddress(t *testing.T) {
	blocked, err := net.Listen("tcp", net.JoinHostPort(DefaultListenAddress, "0"))
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer blocked.Close()

	port := blocked.Addr().(*net.TCPAddr).Port
	srv := New(Config{Port: port, ListenAddress: DefaultListenAddress})
	err = srv.StartWithReady(nil)
	if err == nil {
		t.Fatal("StartWithReady() error = nil, want bind failure")
	}
	wantAddress := net.JoinHostPort(DefaultListenAddress, strconv.Itoa(port))
	if !strings.Contains(err.Error(), "listen on "+wantAddress) {
		t.Fatalf("StartWithReady() error = %q, want address %q", err, wantAddress)
	}
	if srv.listener != nil {
		t.Fatalf("StartWithReady() listener = %v, want nil after bind failure", srv.listener)
	}
}

func TestShouldWarnUnauthenticatedListener(t *testing.T) {
	tests := []struct {
		name          string
		listenAddress string
		authEnabled   bool
		want          bool
	}{
		{name: "unauthenticated all interfaces", listenAddress: AllInterfacesAddress, want: true},
		{name: "unauthenticated loopback", listenAddress: DefaultListenAddress},
		{name: "unauthenticated localhost", listenAddress: "localhost"},
		{name: "authenticated all interfaces", listenAddress: AllInterfacesAddress, authEnabled: true},
		{name: "unauthenticated explicit ipv4", listenAddress: "192.0.2.10", want: true},
		{name: "unauthenticated explicit ipv6", listenAddress: "2001:db8::10", want: true},
		{name: "ipv6 wildcard", listenAddress: "::", want: true},
		{name: "alternate loopback", listenAddress: "127.0.0.2"},
		{name: "ipv6 loopback", listenAddress: "::1"},
		{name: "authenticated explicit ipv4", listenAddress: "192.0.2.10", authEnabled: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldWarnUnauthenticatedListener(tt.listenAddress, tt.authEnabled); got != tt.want {
				t.Fatalf("shouldWarnUnauthenticatedListener(%q, %v) = %v, want %v", tt.listenAddress, tt.authEnabled, got, tt.want)
			}
		})
	}
}

func TestProtectUnauthenticatedLoopback(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	tests := []struct {
		name          string
		listenAddress string
		host          string
		authConfig    auth.Config
		tunnel        bool
		wantStatus    int
	}{
		{name: "loopback host", listenAddress: DefaultListenAddress, host: "localhost:9280", wantStatus: http.StatusNoContent},
		{name: "localhost subdomain", listenAddress: DefaultListenAddress, host: "radar.localhost:9280", wantStatus: http.StatusNoContent},
		{name: "localhost trailing dot", listenAddress: DefaultListenAddress, host: "localhost.:9280", wantStatus: http.StatusNoContent},
		{name: "IPv6 loopback host", listenAddress: DefaultListenAddress, host: "[::1]:9280", wantStatus: http.StatusNoContent},
		{name: "rebinding host", listenAddress: DefaultListenAddress, host: "attacker.example:9280", wantStatus: http.StatusForbidden},
		{name: "localhost lookalike", listenAddress: DefaultListenAddress, host: "localhost.evil.example:9280", wantStatus: http.StatusForbidden},
		{name: "shared listener", listenAddress: AllInterfacesAddress, host: "radar.example.com", wantStatus: http.StatusNoContent},
		{name: "authenticated deployment", listenAddress: DefaultListenAddress, host: "radar.example.com", authConfig: auth.Config{Mode: "oidc"}, wantStatus: http.StatusNoContent},
		{name: "authenticated tunnel", listenAddress: DefaultListenAddress, host: "hub.example.com", tunnel: true, wantStatus: http.StatusNoContent},
		{name: "alternate loopback host", listenAddress: "127.0.0.2", host: "127.0.0.2:9280", wantStatus: http.StatusNoContent},
		{name: "alternate loopback rebinding", listenAddress: "127.0.0.2", host: "attacker.example:9280", wantStatus: http.StatusForbidden},
		{name: "ipv6 listener", listenAddress: "::1", host: "[::1]:9280", wantStatus: http.StatusNoContent},
		{name: "ipv6 listener rebinding", listenAddress: "::1", host: "attacker.example:9280", wantStatus: http.StatusForbidden},
		{name: "explicit ipv4 listener", listenAddress: "192.0.2.10", host: "192.0.2.10:9280", wantStatus: http.StatusNoContent},
		{name: "explicit ipv6 listener", listenAddress: "2001:db8::10", host: "[2001:db8::10]:9280", wantStatus: http.StatusNoContent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Server{listenAddress: tt.listenAddress, authConfig: tt.authConfig}
			handler := s.protectUnauthenticatedLoopback(next)
			if tt.tunnel {
				handler = cloud.AuthenticatedTunnelHandler(handler)
			}
			req := httptest.NewRequest(http.MethodGet, "http://"+tt.host+"/api/health", nil)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", w.Code, tt.wantStatus, w.Body.String())
			}
			if tt.wantStatus == http.StatusForbidden && !strings.Contains(w.Body.String(), "enable authentication for a reverse proxy") {
				t.Fatalf("body = %q, want reverse-proxy remediation", w.Body.String())
			}
		})
	}
}

func TestLoopbackHostProtectionIsMounted(t *testing.T) {
	s := New(Config{ListenAddress: DefaultListenAddress})

	tests := []struct {
		name       string
		host       string
		wantStatus int
	}{
		{name: "rejects rebinding host", host: "attacker.example:9280", wantStatus: http.StatusForbidden},
		{name: "accepts loopback host", host: "localhost:9280", wantStatus: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://"+tt.host+"/api/health", nil)
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, req)
			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", w.Code, tt.wantStatus, w.Body.String())
			}
		})
	}
}
