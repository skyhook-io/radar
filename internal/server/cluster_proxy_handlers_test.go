package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/settings"
)

func putContextProxy(t *testing.T, s *Server, host, contextName, body string) *httptest.ResponseRecorder {
	t.Helper()
	router := chi.NewRouter()
	router.Put("/api/contexts/{name}/proxy", s.handleSetContextProxy)
	req := httptest.NewRequest(http.MethodPut, "/api/contexts/"+contextName+"/proxy", strings.NewReader(body))
	req.Host = host
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func savedProxies(t *testing.T) map[string]string {
	t.Helper()
	proxies, err := settings.ClusterProxies()
	if err != nil {
		t.Fatal(err)
	}
	return proxies
}

func isolateProxyTest(t *testing.T) *[]string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Cleanup(k8s.SetTestLocalMode())
	t.Cleanup(k8s.SetTestRegistryEntry("prod", "/kube/prod.yaml", "prod"))
	var probed []string
	orig := testContextProxy
	testContextProxy = func(_ context.Context, name, proxyURL string) (string, error) {
		probed = append(probed, name+" via "+proxyURL)
		binding := k8s.ContextBindingFor(name)
		if strings.Contains(proxyURL, "dead") {
			return "", errors.New("cluster unreachable: proxyconnect tcp: connection refused")
		}
		if strings.Contains(proxyURL, "expired") {
			return binding, fmt.Errorf("cluster unreachable: %w", apierrors.NewUnauthorized("token expired"))
		}
		return binding, nil
	}
	t.Cleanup(func() { testContextProxy = orig })
	origExists := contextExists
	contextExists = func(name string) bool { return name == "prod" }
	t.Cleanup(func() { contextExists = origExists })
	return &probed
}

func TestSetContextProxySavesAfterProbe(t *testing.T) {
	probed := isolateProxyTest(t)
	s := &Server{listenAddress: DefaultListenAddress}

	w := putContextProxy(t, s, "127.0.0.1:9280", "prod", `{"proxyUrl":" socks5://user:secret@bastion:1080 "}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("response leaks proxy credentials: %s", w.Body)
	}
	if got := savedProxies(t)[k8s.ContextBindingFor("prod")]; got != "socks5://user:secret@bastion:1080" {
		t.Fatalf("saved proxy = %q", got)
	}
	if len(*probed) != 1 {
		t.Fatalf("probes = %v, want one before saving", *probed)
	}

	w = putContextProxy(t, s, "127.0.0.1:9280", "prod", `{"proxyUrl":""}`)
	if w.Code != http.StatusOK {
		t.Fatalf("clear status = %d, body %s", w.Code, w.Body)
	}
	if len(savedProxies(t)) != 0 {
		t.Fatal("empty proxyUrl must remove the saved proxy")
	}
	if len(*probed) != 1 {
		t.Fatalf("clearing must not probe, probes = %v", *probed)
	}
}

func TestSetContextProxyRefusesBadOrUnreachableProxy(t *testing.T) {
	isolateProxyTest(t)
	s := &Server{listenAddress: DefaultListenAddress}

	if w := putContextProxy(t, s, "127.0.0.1:9280", "prod", `{"proxyUrl":"ftp://proxy:21"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("unsupported scheme status = %d", w.Code)
	}
	t.Cleanup(k8s.SetTestRegistryEntry("gone", "/kube/gone.yaml", "gone"))
	if w := putContextProxy(t, s, "127.0.0.1:9280", "missing", `{"proxyUrl":""}`); w.Code != http.StatusNotFound {
		t.Fatalf("unknown context status = %d", w.Code)
	}
	if w := putContextProxy(t, s, "127.0.0.1:9280", "gone", `{"proxyUrl":""}`); w.Code != http.StatusNotFound {
		t.Fatalf("unknown context status = %d", w.Code)
	}
	w := putContextProxy(t, s, "127.0.0.1:9280", "prod", `{"proxyUrl":"http://dead:3128"}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "proxy_unreachable") {
		t.Fatalf("unreachable proxy: status = %d, body %s", w.Code, w.Body)
	}
	if len(savedProxies(t)) != 0 {
		t.Fatal("a proxy that failed its probe must not be saved")
	}
}

// Shared without authentication, anyone reaching the address is a user, but
// none may reroute Radar's Kubernetes traffic through a proxy of their choosing.
func TestSetContextProxyGate(t *testing.T) {
	cases := []struct {
		name   string
		server *Server
		host   string
		tunnel bool
	}{
		{"shared listener", &Server{listenAddress: "0.0.0.0"}, "127.0.0.1:9280", false},
		{"non-loopback host", &Server{listenAddress: DefaultListenAddress}, "radar.internal", false},
		{"authentication enabled", &Server{listenAddress: DefaultListenAddress, authConfig: auth.Config{Mode: "proxy"}}, "127.0.0.1:9280", false},
		{"Radar Hub tunnel", &Server{listenAddress: DefaultListenAddress}, "127.0.0.1:9280", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probed := isolateProxyTest(t)
			router := chi.NewRouter()
			router.Put("/api/contexts/{name}/proxy", tc.server.handleSetContextProxy)
			var handler http.Handler = router
			if tc.tunnel {
				handler = cloud.AuthenticatedTunnelHandler(router)
			}
			req := httptest.NewRequest(http.MethodPut, "/api/contexts/prod/proxy", strings.NewReader(`{"proxyUrl":"http://proxy:3128"}`))
			req.Host = tc.host
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body %s", w.Code, w.Body)
			}
			if len(*probed) != 0 || len(savedProxies(t)) != 0 {
				t.Fatal("a refused request must neither probe nor save")
			}
		})
	}
}

// Expired credentials answer 401 through the proxy, which proves the proxy
// works; the connection screen deals with the credentials.
func TestSetContextProxyAcceptsAProxyThatReachedTheAPIServer(t *testing.T) {
	isolateProxyTest(t)
	s := &Server{listenAddress: DefaultListenAddress}
	if w := putContextProxy(t, s, "127.0.0.1:9280", "prod", `{"proxyUrl":"http://expired:3128"}`); w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	if savedProxies(t)[k8s.ContextBindingFor("prod")] != "http://expired:3128" {
		t.Fatal("a proxy the API server answered through must be saved")
	}
}

func TestListContextsReportsSavedProxyWithoutCredentials(t *testing.T) {
	isolateProxyTest(t)
	if err := settings.SetClusterProxy(k8s.ContextBindingFor("prod"), "socks5://user:secret@bastion:1080"); err != nil {
		t.Fatal(err)
	}
	orig := availableContexts
	availableContexts = func() ([]k8s.ContextInfo, error) { return []k8s.ContextInfo{{Name: "prod"}, {Name: "other"}}, nil }
	t.Cleanup(func() { availableContexts = orig })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/contexts", nil)
	req.Host = "127.0.0.1:9280"
	(&Server{listenAddress: DefaultListenAddress}).handleListContexts(w, req)
	if strings.Contains(w.Body.String(), "secret") {
		t.Fatalf("context list leaks proxy credentials: %s", w.Body)
	}
	var got []k8s.ContextInfo
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got[0].ProxyURL != "socks5://bastion:1080" || got[1].ProxyURL != "" {
		t.Fatalf("proxies = %q, %q", got[0].ProxyURL, got[1].ProxyURL)
	}
}

// Where the proxy can't be set, the list doesn't say which proxy host Radar
// reaches a cluster through.
func TestListContextsOmitsSavedProxyWhereItCantBeSet(t *testing.T) {
	isolateProxyTest(t)
	if err := settings.SetClusterProxy(k8s.ContextBindingFor("prod"), "socks5://bastion:1080"); err != nil {
		t.Fatal(err)
	}
	orig := availableContexts
	availableContexts = func() ([]k8s.ContextInfo, error) { return []k8s.ContextInfo{{Name: "prod"}}, nil }
	t.Cleanup(func() { availableContexts = orig })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/contexts", nil)
	req.Host = "127.0.0.1:9280"
	(&Server{listenAddress: "0.0.0.0"}).handleListContexts(w, req)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "bastion") {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
}

// A kubeconfig reload during the probe can point the name at another file:
// the proxy is never saved for a context it wasn't tested against.
func TestSetContextProxyRefusesWhenTheContextChangedDuringTheProbe(t *testing.T) {
	isolateProxyTest(t)
	orig := testContextProxy
	testContextProxy = func(ctx context.Context, name, proxyURL string) (string, error) {
		binding, err := orig(ctx, name, proxyURL)
		t.Cleanup(k8s.SetTestRegistryEntry("prod", "/kube/other.yaml", "prod"))
		return binding, err
	}
	s := &Server{listenAddress: DefaultListenAddress}
	w := putContextProxy(t, s, "127.0.0.1:9280", "prod", `{"proxyUrl":"http://proxy:3128"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	if got := savedProxies(t); len(got) != 0 {
		t.Fatalf("saved %v for a context that wasn't probed", got)
	}
}
