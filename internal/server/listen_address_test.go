package server

import (
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
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
		{name: "arbitrary address", input: "192.0.2.10", wantErr: true},
		{name: "arbitrary hostname", input: "radar.internal", wantErr: true},
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := socketAddress(tt.listenAddress, 9280); got != tt.want {
				t.Fatalf("socketAddress(%q, 9280) = %q, want %q", tt.listenAddress, got, tt.want)
			}
		})
	}
}

func TestServerListenAddress(t *testing.T) {
	tests := []struct {
		name          string
		listenAddress string
		wantLoopback  bool
		wantAny       bool
	}{
		{name: "zero value is loopback", wantLoopback: true},
		{name: "explicit loopback", listenAddress: DefaultListenAddress, wantLoopback: true},
		{name: "explicit all interfaces", listenAddress: AllInterfacesAddress, wantAny: true},
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
			case <-time.After(5 * time.Second):
				t.Fatal("server did not become ready")
			}

			gotIP := srv.listener.Addr().(*net.TCPAddr).IP
			if gotIP.IsLoopback() != tt.wantLoopback {
				t.Errorf("listener IP %q loopback = %v, want %v", gotIP, gotIP.IsLoopback(), tt.wantLoopback)
			}
			if gotIP.IsUnspecified() != tt.wantAny {
				t.Errorf("listener IP %q unspecified = %v, want %v", gotIP, gotIP.IsUnspecified(), tt.wantAny)
			}
			if got := srv.ActualAddr(); got != net.JoinHostPort("localhost", strconv.Itoa(srv.ActualPort())) {
				t.Errorf("ActualAddr() = %q; want dialable localhost address", got)
			}

			srv.Stop()
			select {
			case err := <-errCh:
				if err != nil && !errors.Is(err, net.ErrClosed) {
					t.Fatalf("server shutdown error: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("server did not stop")
			}
		})
	}
}

func TestServerRejectsInvalidListenAddress(t *testing.T) {
	srv := New(Config{Port: 0, ListenAddress: "192.0.2.10"})

	if err := srv.StartWithReady(nil); err == nil {
		t.Fatal("StartWithReady() error = nil, want invalid listen address error")
	} else if !strings.Contains(err.Error(), `invalid listen address "192.0.2.10"`) {
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldWarnUnauthenticatedListener(tt.listenAddress, tt.authEnabled); got != tt.want {
				t.Fatalf("shouldWarnUnauthenticatedListener(%q, %v) = %v, want %v", tt.listenAddress, tt.authEnabled, got, tt.want)
			}
		})
	}
}
