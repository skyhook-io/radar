package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
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
