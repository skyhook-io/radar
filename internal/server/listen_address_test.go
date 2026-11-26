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
			if got := unauthenticatedListenerWarning(tt.listenAddress, tt.authEnabled, false); got != tt.want {
				t.Fatalf("unauthenticatedListenerWarning(%q, %v, false) = %v, want %v", tt.listenAddress, tt.authEnabled, got, tt.want)
			}
		})
	}
}

func TestProtectUnauthenticatedHost(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	tests := []struct {
		name          string
		listenAddress string
		host          string
		authConfig    auth.Config
		tunnel        bool
		allowedHosts  []string
		allowAnyHost  bool
		wantStatus    int
		wantBody      string
	}{
		{name: "loopback host", listenAddress: DefaultListenAddress, host: "localhost:9280", wantStatus: http.StatusNoContent},
		{name: "localhost subdomain", listenAddress: DefaultListenAddress, host: "radar.localhost:9280", wantStatus: http.StatusNoContent},
		{name: "localhost trailing dot", listenAddress: DefaultListenAddress, host: "localhost.:9280", wantStatus: http.StatusNoContent},
		{name: "IPv6 loopback host", listenAddress: DefaultListenAddress, host: "[::1]:9280", wantStatus: http.StatusNoContent},
		{name: "rebinding host", listenAddress: DefaultListenAddress, host: "attacker.example:9280", wantStatus: http.StatusForbidden},
		{name: "localhost lookalike", listenAddress: DefaultListenAddress, host: "localhost.evil.example:9280", wantStatus: http.StatusForbidden},
		{name: "shared listener rebinding", listenAddress: AllInterfacesAddress, host: "attacker.example:9280", wantStatus: http.StatusForbidden, wantBody: `add \"attacker.example\" to --allowed-hosts`},
		{name: "shared listener unlisted name", listenAddress: AllInterfacesAddress, host: "radar.example.com", wantStatus: http.StatusForbidden},
		{name: "shared listener listed name", listenAddress: AllInterfacesAddress, host: "Radar.Example.com.:443", allowedHosts: []string{"radar.example.com"}, wantStatus: http.StatusNoContent},
		{name: "shared listener any host", listenAddress: AllInterfacesAddress, host: "attacker.example", allowAnyHost: true, wantStatus: http.StatusNoContent},
		{name: "shared listener localhost", listenAddress: AllInterfacesAddress, host: "localhost:9280", wantStatus: http.StatusNoContent},
		{name: "shared listener IPv4", listenAddress: AllInterfacesAddress, host: "192.168.1.6:9280", wantStatus: http.StatusNoContent},
		{name: "shared listener IPv6", listenAddress: AllInterfacesAddress, host: "[2001:db8::10]:9280", wantStatus: http.StatusNoContent},
		{name: "shared listener single-label name", listenAddress: AllInterfacesAddress, host: "radar:9280", wantStatus: http.StatusNoContent},
		{name: "shared listener service name", listenAddress: AllInterfacesAddress, host: "radar.radar.svc:9280", wantStatus: http.StatusNoContent},
		{name: "shared listener service FQDN", listenAddress: AllInterfacesAddress, host: "radar.radar.svc.cluster.local.:9280", wantStatus: http.StatusNoContent},
		{name: "shared listener svc lookalike", listenAddress: AllInterfacesAddress, host: "radar.svc.attacker.example", wantStatus: http.StatusForbidden},
		{name: "shared listener cluster.local lookalike", listenAddress: AllInterfacesAddress, host: "svc.cluster.local.attacker.example", wantStatus: http.StatusForbidden},
		{name: "shared listener empty host", listenAddress: AllInterfacesAddress, host: "", wantStatus: http.StatusForbidden},
		{name: "authenticated shared listener", listenAddress: AllInterfacesAddress, host: "attacker.example", authConfig: auth.Config{Mode: "proxy"}, wantStatus: http.StatusNoContent},
		{name: "shared listener tunnel", listenAddress: AllInterfacesAddress, host: "hub.example.com", tunnel: true, wantStatus: http.StatusNoContent},
		{name: "explicit ipv4 listener rebinding", listenAddress: "192.0.2.10", host: "attacker.example:9280", wantStatus: http.StatusForbidden},
		{name: "shared listener wildcard subdomain", listenAddress: AllInterfacesAddress, host: "radar.example.com", allowedHosts: []string{"*.example.com"}, wantStatus: http.StatusNoContent},
		{name: "shared listener wildcard nested", listenAddress: AllInterfacesAddress, host: "a.radar.example.com", allowedHosts: []string{"*.example.com"}, wantStatus: http.StatusNoContent},
		{name: "shared listener wildcard excludes apex", listenAddress: AllInterfacesAddress, host: "example.com", allowedHosts: []string{"*.example.com"}, wantStatus: http.StatusForbidden},
		{name: "shared listener wildcard lookalike", listenAddress: AllInterfacesAddress, host: "radar.example.com.attacker.example", allowedHosts: []string{"*.example.com"}, wantStatus: http.StatusForbidden},
		{name: "shared listener wildcard suffix lookalike", listenAddress: AllInterfacesAddress, host: "radarexample.com", allowedHosts: []string{"*.example.com"}, wantStatus: http.StatusForbidden},
		{name: "shared listener oversized host", listenAddress: AllInterfacesAddress, host: strings.Repeat("a.", 200) + "example.com", allowedHosts: []string{"*.example.com"}, wantStatus: http.StatusForbidden},
		{name: "loopback ignores allowed hosts", listenAddress: DefaultListenAddress, host: "radar.example.com", allowedHosts: []string{"radar.example.com"}, wantStatus: http.StatusForbidden},
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
			s := &Server{listenAddress: tt.listenAddress, authConfig: tt.authConfig, allowAnyHost: tt.allowAnyHost, allowedHosts: map[string]struct{}{}}
			for _, host := range tt.allowedHosts {
				s.allowedHosts[host] = struct{}{}
			}
			handler := s.protectUnauthenticatedHost(next)
			if tt.tunnel {
				handler = cloud.AuthenticatedTunnelHandler(handler)
			}
			req := httptest.NewRequest(http.MethodGet, "http://placeholder/api/health", nil)
			req.Host = tt.host
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", w.Code, tt.wantStatus, w.Body.String())
			}
			if tt.wantBody != "" && !strings.Contains(w.Body.String(), tt.wantBody) {
				t.Fatalf("body = %q, want %q", w.Body.String(), tt.wantBody)
			}
			if tt.wantStatus == http.StatusForbidden && !strings.Contains(w.Body.String(), "enable authentication") {
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

func TestSharedListenerHostProtectionIsMounted(t *testing.T) {
	mcp := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	s := New(Config{
		ListenAddress:  AllInterfacesAddress,
		AllowedHosts:   []string{"radar.internal"},
		TrustedOrigins: []string{"https://radar.example.com:8443", "http://bücher.example"},
		MCPHandler:     mcp,
	})

	tests := []struct {
		name       string
		method     string
		path       string
		host       string
		wantStatus int
	}{
		{name: "mcp rebinding", method: http.MethodPost, path: "/mcp", host: "attacker.example:9280", wantStatus: http.StatusForbidden},
		{name: "api write rebinding", method: http.MethodPost, path: "/api/cluster/namespace", host: "attacker.example:9280", wantStatus: http.StatusForbidden},
		{name: "health rebinding", method: http.MethodGet, path: "/api/health", host: "attacker.example:9280", wantStatus: http.StatusForbidden},
		{name: "mcp by IP", method: http.MethodPost, path: "/mcp", host: "192.168.1.6:9280", wantStatus: http.StatusNoContent},
		{name: "mcp allowed host", method: http.MethodPost, path: "/mcp", host: "radar.internal:9280", wantStatus: http.StatusNoContent},
		{name: "mcp trusted origin host", method: http.MethodPost, path: "/mcp", host: "radar.example.com", wantStatus: http.StatusNoContent},
		{name: "mcp trusted origin IDN host", method: http.MethodPost, path: "/mcp", host: "xn--bcher-kva.example", wantStatus: http.StatusNoContent},
		{name: "health by pod IP", method: http.MethodGet, path: "/api/health", host: "10.0.0.7:9280", wantStatus: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, "http://placeholder"+tt.path, strings.NewReader("{}"))
			req.Host = tt.host
			req.Header.Set("Origin", "http://"+tt.host)
			req.Header.Set("Sec-Fetch-Site", "same-origin")
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, req)
			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", w.Code, tt.wantStatus, w.Body.String())
			}
		})
	}
}

func TestParseAllowedHosts(t *testing.T) {
	hosts, err := ParseAllowedHosts(" Radar.Example.com. , *, radar.internal,*.Corp.Example,bücher.example,*.Bücher.example,,")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(hosts, ",") != "radar.example.com,*,radar.internal,*.corp.example,xn--bcher-kva.example,*.xn--bcher-kva.example" {
		t.Fatalf("hosts = %q", hosts)
	}
	for _, bad := range []string{"https://radar.example.com", "radar.example.com:9280", "*.com", "a.*.example.com", "**.example.com", "user@radar", "[::1]"} {
		if hosts, err := ParseAllowedHosts(bad + ",ok.example"); err == nil || strings.Join(hosts, ",") != "ok.example" {
			t.Errorf("%q: hosts=%q err=%v, want rejected entry and ok.example kept", bad, hosts, err)
		}
	}
}
