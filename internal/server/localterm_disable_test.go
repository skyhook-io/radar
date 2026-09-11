package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/k8s"
)

// --disable-local-terminal must refuse the endpoint, not merely hide it from
// the advertised capabilities: the handler runs a shell on the Radar host, so
// an operator who turns it off must actually lose the route.
func TestLocalTerminalDisabledRejectsRequest(t *testing.T) {
	previousForceDisableLocalTerminal := k8s.ForceDisableLocalTerminal
	t.Cleanup(func() {
		k8s.ForceDisableLocalTerminal = previousForceDisableLocalTerminal
	})
	t.Cleanup(k8s.SetTestLocalMode())

	k8s.ForceDisableLocalTerminal = true
	if k8s.IsInCluster() {
		t.Fatal("precondition: expected local mode")
	}

	w := httptest.NewRecorder()
	(&Server{}).handleLocalTerminal(w, httptest.NewRequest(http.MethodGet, "/api/local-terminal", nil))

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
	if got, want := w.Body.String(), "{\"error\":\"local terminal is disabled\"}\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestLocalTerminalInClusterRejectsRequest(t *testing.T) {
	previousForceInCluster := k8s.ForceInCluster
	previousForceDisableLocalTerminal := k8s.ForceDisableLocalTerminal
	t.Cleanup(func() {
		k8s.ForceInCluster = previousForceInCluster
		k8s.ForceDisableLocalTerminal = previousForceDisableLocalTerminal
	})

	k8s.ForceInCluster = true
	k8s.ForceDisableLocalTerminal = false

	w := httptest.NewRecorder()
	(&Server{}).handleLocalTerminal(w, httptest.NewRequest(http.MethodGet, "/api/local-terminal", nil))

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
	if got, want := w.Body.String(), "{\"error\":\"local terminal not available in-cluster mode\"}\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestLocalTerminalAuthEnabledRejectsRequest(t *testing.T) {
	previousForceDisableLocalTerminal := k8s.ForceDisableLocalTerminal
	t.Cleanup(func() {
		k8s.ForceDisableLocalTerminal = previousForceDisableLocalTerminal
	})
	t.Cleanup(k8s.SetTestLocalMode())

	k8s.ForceDisableLocalTerminal = false
	w := httptest.NewRecorder()
	server := &Server{authConfig: auth.Config{Mode: "proxy"}}
	server.handleLocalTerminal(w, httptest.NewRequest(http.MethodGet, "/api/local-terminal", nil))

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
	if got, want := w.Body.String(), "{\"error\":\"local terminal is unavailable when authentication is enabled\"}\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestLocalTerminalAuthenticatedTunnelRejectsRequest(t *testing.T) {
	previousForceDisableLocalTerminal := k8s.ForceDisableLocalTerminal
	t.Cleanup(func() {
		k8s.ForceDisableLocalTerminal = previousForceDisableLocalTerminal
	})
	t.Cleanup(k8s.SetTestLocalMode())

	k8s.ForceDisableLocalTerminal = false
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://localhost:9280/api/local-terminal", nil)
	handler := cloud.AuthenticatedTunnelHandler(http.HandlerFunc((&Server{listenAddress: DefaultListenAddress}).handleLocalTerminal))
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
	if got, want := w.Body.String(), "{\"error\":\"local terminal is unavailable over the Radar Hub tunnel\"}\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestLocalTerminalRejectsNonLoopbackRequestHost(t *testing.T) {
	previousForceDisableLocalTerminal := k8s.ForceDisableLocalTerminal
	t.Cleanup(func() {
		k8s.ForceDisableLocalTerminal = previousForceDisableLocalTerminal
	})
	t.Cleanup(k8s.SetTestLocalMode())

	k8s.ForceDisableLocalTerminal = false
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://attacker.example/api/local-terminal", nil)
	req.Header.Set("Origin", "http://attacker.example")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	(&Server{}).handleLocalTerminal(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
	if got, want := w.Body.String(), "{\"error\":\"local terminal is only available over a loopback address\"}\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestLocalTerminalRejectsSpoofedLoopbackHostOnSharedListener(t *testing.T) {
	previousForceDisableLocalTerminal := k8s.ForceDisableLocalTerminal
	t.Cleanup(func() {
		k8s.ForceDisableLocalTerminal = previousForceDisableLocalTerminal
	})
	t.Cleanup(k8s.SetTestLocalMode())

	k8s.ForceDisableLocalTerminal = false
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://localhost:9280/api/local-terminal", nil)
	(&Server{listenAddress: AllInterfacesAddress}).handleLocalTerminal(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
	if got, want := w.Body.String(), "{\"error\":\"local terminal is only available over a loopback address\"}\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestLocalTerminalAvailableOnUnauthenticatedLoopback(t *testing.T) {
	previousForceDisableLocalTerminal := k8s.ForceDisableLocalTerminal
	t.Cleanup(func() {
		k8s.ForceDisableLocalTerminal = previousForceDisableLocalTerminal
	})
	t.Cleanup(k8s.SetTestLocalMode())

	k8s.ForceDisableLocalTerminal = false
	req := httptest.NewRequest(http.MethodGet, "http://localhost:9280/api/local-terminal", nil)
	status, message := (&Server{listenAddress: DefaultListenAddress}).localTerminalUnavailable(req)
	if status != 0 || message != "" {
		t.Fatalf("local terminal unavailable on supported loopback deployment: status=%d message=%q", status, message)
	}
}

func TestLocalTerminalRejectsCrossOriginWebSocket(t *testing.T) {
	previousForceDisableLocalTerminal := k8s.ForceDisableLocalTerminal
	t.Cleanup(func() {
		k8s.ForceDisableLocalTerminal = previousForceDisableLocalTerminal
	})
	t.Cleanup(k8s.SetTestLocalMode())
	k8s.ForceDisableLocalTerminal = false

	s := &Server{listenAddress: DefaultListenAddress}
	srv := httptest.NewServer(http.HandlerFunc(s.handleLocalTerminal))
	t.Cleanup(srv.Close)

	_, resp, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(srv.URL, "http"),
		http.Header{"Origin": {"https://evil.example"}},
	)
	if err == nil {
		t.Fatal("cross-origin local terminal handshake succeeded")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("rejection response = %v, want HTTP 403", resp)
	}
}

func TestCapabilitiesAdvertiseLocalTerminalAvailability(t *testing.T) {
	previousForceDisableLocalTerminal := k8s.ForceDisableLocalTerminal
	previousConnection := k8s.GetConnectionStatus()
	t.Cleanup(func() {
		k8s.ForceDisableLocalTerminal = previousForceDisableLocalTerminal
		k8s.SetConnectionStatus(previousConnection)
	})
	t.Cleanup(k8s.SetTestLocalMode())
	k8s.ForceDisableLocalTerminal = false
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})

	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"apiVersion": "authorization.k8s.io/v1",
			"kind":       "SelfSubjectAccessReview",
			"status":     map[string]any{"allowed": true},
		}); err != nil {
			t.Errorf("encode SelfSubjectAccessReview: %v", err)
		}
	}))
	t.Cleanup(apiServer.Close)

	client, err := kubernetes.NewForConfig(&rest.Config{Host: apiServer.URL})
	if err != nil {
		t.Fatalf("build Kubernetes client: %v", err)
	}
	previousClient := k8s.SetTestClient(client)
	k8s.InvalidateCapabilitiesCache()
	t.Cleanup(func() {
		k8s.InvalidateCapabilitiesCache()
		k8s.SetTestClient(previousClient)
	})

	tests := []struct {
		name   string
		server Server
		host   string
		tunnel bool
		want   bool
	}{
		{name: "authenticated deployment", server: Server{listenAddress: DefaultListenAddress, authConfig: auth.Config{Mode: "proxy"}}, host: "radar.example.com", want: false},
		{name: "shared listener", server: Server{listenAddress: AllInterfacesAddress}, host: "localhost:9280", want: false},
		{name: "non-loopback request host", server: Server{listenAddress: DefaultListenAddress}, host: "radar.example.com", want: false},
		{name: "authenticated tunnel", server: Server{listenAddress: DefaultListenAddress}, host: "localhost:9280", tunnel: true, want: false},
		{name: "supported loopback deployment", server: Server{listenAddress: DefaultListenAddress}, host: "localhost:9280", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://"+tt.host+"/api/capabilities", nil)
			w := httptest.NewRecorder()
			handler := http.Handler(http.HandlerFunc(tt.server.handleCapabilities))
			if tt.tunnel {
				handler = cloud.AuthenticatedTunnelHandler(handler)
			}
			handler.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body = %s", w.Code, http.StatusOK, w.Body.String())
			}
			var response struct {
				LocalTerminal bool `json:"localTerminal"`
			}
			if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
				t.Fatalf("decode capabilities: %v", err)
			}
			if response.LocalTerminal != tt.want {
				t.Fatalf("localTerminal = %v, want %v", response.LocalTerminal, tt.want)
			}
		})
	}

	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateDisconnected})
	k8s.InvalidateCapabilitiesCache()
	for _, tt := range []struct {
		name   string
		server Server
		host   string
		want   bool
	}{
		{name: "supported loopback deployment", server: Server{listenAddress: DefaultListenAddress}, host: "localhost:9280", want: true},
		{name: "shared listener", server: Server{listenAddress: AllInterfacesAddress}, host: "localhost:9280", want: false},
	} {
		t.Run("disconnected/"+tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://"+tt.host+"/api/capabilities", nil)
			w := httptest.NewRecorder()
			tt.server.handleCapabilities(w, req)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body = %s", w.Code, http.StatusOK, w.Body.String())
			}
			var response struct {
				LocalTerminal bool `json:"localTerminal"`
			}
			if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
				t.Fatalf("decode capabilities: %v", err)
			}
			if response.LocalTerminal != tt.want {
				t.Fatalf("localTerminal = %v, want %v", response.LocalTerminal, tt.want)
			}
		})
	}
}
