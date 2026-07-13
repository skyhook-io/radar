package argocd

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/pkg/argoapi"
)

type fakeArgo struct {
	token            string
	loggedIn         bool
	managedRequests  atomic.Int64
	managedResources string
}

func (f *fakeArgo) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/version", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"Version": "v2.13.0"}`))
	})
	mux.HandleFunc("/api/v1/session/userinfo", func(w http.ResponseWriter, r *http.Request) {
		if f.token != "" && r.Header.Get("Authorization") != "Bearer "+f.token {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error": "invalid session", "message": "invalid session: token is expired"}`))
			return
		}
		if f.loggedIn {
			_, _ = w.Write([]byte(`{"loggedIn": true, "username": "admin", "iss": "argocd"}`))
		} else {
			_, _ = w.Write([]byte(`{"loggedIn": false}`))
		}
	})
	mux.HandleFunc("/api/v1/applications/", func(w http.ResponseWriter, r *http.Request) {
		f.managedRequests.Add(1)
		body := f.managedResources
		if body == "" {
			body = `{"items": []}`
		}
		_, _ = w.Write([]byte(body))
	})
	return mux
}

func newTestManager(cfg config.Config) *Manager {
	return &Manager{
		k8sClient:   func() kubernetes.Interface { return nil },
		k8sConfig:   func() *rest.Config { return nil },
		inCluster:   func() bool { return false },
		contextName: func() string { return "" },
		loadConfig:  func() config.Config { return cfg },
	}
}

// TestAutoDiscoveryTokenBoundToContext pins that an auto-discovery token
// (empty URL) is bound to the context it was configured under, and that a
// context switch makes Probe refuse to connect (never sending the token to a
// different cluster's discovered Argo) by returning errTokenContextMismatch.
func TestAutoDiscoveryTokenBoundToContext(t *testing.T) {
	ctx := "cluster-a"
	m := newTestManager(config.Config{})
	m.contextName = func() string { return ctx }

	m.SetConfig("", "secret-token", false, true) // auto-discovery mode
	if m.tokenContext != "cluster-a" {
		t.Fatalf("tokenContext = %q, want the config-time context", m.tokenContext)
	}

	// Switch to a different context: the token is bound to cluster-a, so Probe
	// must refuse rather than send it to cluster-b's discovered argocd-server.
	ctx = "cluster-b"
	if err := m.Probe(context.Background()); !errors.Is(err, errTokenContextMismatch) {
		t.Fatalf("Probe after context switch = %v, want errTokenContextMismatch", err)
	}
}

// TestSetConfigPreservesTokenContext pins that re-saving with a carried-forward
// token (tokenIsFresh=false) does NOT rebind the token to the current context.
// Only an explicitly-provided token (tokenIsFresh=true) re-stamps tokenContext;
// otherwise editing an unrelated setting after a context switch would silently
// defeat the auto-discovery context guard.
func TestSetConfigPreservesTokenContext(t *testing.T) {
	ctx := "cluster-a"
	m := newTestManager(config.Config{})
	m.contextName = func() string { return ctx }

	m.SetConfig("", "secret-token", false, true) // fresh token on cluster-a
	if m.tokenContext != "cluster-a" {
		t.Fatalf("tokenContext = %q, want cluster-a", m.tokenContext)
	}

	// Switch context, then re-save carrying the same token forward.
	ctx = "cluster-b"
	m.SetConfig("", "secret-token", false, false) // stale token, not fresh
	if m.tokenContext != "cluster-a" {
		t.Fatalf("tokenContext = %q after non-fresh re-save, want it still bound to cluster-a", m.tokenContext)
	}
	if err := m.Probe(context.Background()); !errors.Is(err, errTokenContextMismatch) {
		t.Fatalf("Probe = %v, want errTokenContextMismatch (token still bound to cluster-a)", err)
	}
}

// TestRestoreConfigRestoresTokenContextBinding pins the connect-rollback guard:
// trying a fresh token in a new cluster re-stamps tokenContext to that cluster
// BEFORE the probe. When the probe fails, the rollback must put the PRIOR binding
// back onto the restored (older) token — otherwise that token would be left
// marked valid for the wrong cluster in memory, defeating the cross-cluster guard
// until restart. A plain SetConfig(fresh=false) rollback would leave the new
// stamp; RestoreConfig restores the binding explicitly.
func TestRestoreConfigRestoresTokenContextBinding(t *testing.T) {
	ctx := "cluster-a"
	m := newTestManager(config.Config{})
	m.contextName = func() string { return ctx }

	m.SetConfig("", "token-a", false, true) // fresh auto-discovery token on cluster-a
	if m.tokenContext != "cluster-a" {
		t.Fatalf("tokenContext = %q, want cluster-a", m.tokenContext)
	}

	// Operator switches to cluster-b and tries to connect a fresh token there; the
	// candidate SetConfig(fresh=true) stamps tokenContext=cluster-b before the probe.
	ctx = "cluster-b"
	m.SetConfig("", "token-b", false, true)
	if m.tokenContext != "cluster-b" {
		t.Fatalf("candidate tokenContext = %q, want cluster-b", m.tokenContext)
	}

	// The probe fails, so the handler rolls back to the prior token + binding.
	m.RestoreConfig("", "token-a", false, "cluster-a")
	if m.tokenContext != "cluster-a" {
		t.Fatalf("tokenContext after rollback = %q, want cluster-a restored", m.tokenContext)
	}
	// Still in cluster-b: the restored token is bound to cluster-a, so the guard
	// must fire rather than send token-a to cluster-b's argocd-server.
	if err := m.Probe(context.Background()); !errors.Is(err, errTokenContextMismatch) {
		t.Fatalf("Probe after rollback = %v, want errTokenContextMismatch (guard intact)", err)
	}
}

// TestIsConfigured pins that IsConfigured reflects whether the integration has
// connection settings (explicit URL or token), independent of any live probe.
func TestIsConfigured(t *testing.T) {
	m := newTestManager(config.Config{})
	if m.IsConfigured() {
		t.Fatal("IsConfigured = true on a fresh manager, want false")
	}
	m.SetConfig("https://argocd.example.com", "", false, false)
	if !m.IsConfigured() {
		t.Fatal("IsConfigured = false after setting an explicit URL, want true")
	}
	m.SetConfig("", "just-a-token", false, true)
	if !m.IsConfigured() {
		t.Fatal("IsConfigured = false after setting a token, want true")
	}
}

func TestProbeManualURL(t *testing.T) {
	fa := &fakeArgo{token: "good", loggedIn: true}
	srv := httptest.NewServer(fa.handler())
	defer srv.Close()

	m := newTestManager(config.Config{})
	m.SetConfig(srv.URL, "good", false, true)

	if _, ok := m.Get(); ok {
		t.Fatal("Get should report not connected before Probe")
	}
	if err := m.Probe(context.Background()); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if _, ok := m.Get(); !ok {
		t.Fatal("Get should report connected after Probe")
	}
	if m.Address() != srv.URL {
		t.Errorf("Address = %q, want %q", m.Address(), srv.URL)
	}
}

func TestProbeUnreachable(t *testing.T) {
	m := newTestManager(config.Config{})
	m.SetConfig("http://127.0.0.1:1", "", false, true)

	err := m.Probe(context.Background())
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("err = %v, want ErrUnreachable", err)
	}
	if _, ok := m.Get(); ok {
		t.Error("Get should report not connected after failed probe")
	}
}

func TestProbeTokenInvalid(t *testing.T) {
	fa := &fakeArgo{token: "good", loggedIn: true}
	srv := httptest.NewServer(fa.handler())
	defer srv.Close()

	m := newTestManager(config.Config{})
	m.SetConfig(srv.URL, "wrong", false, true)

	err := m.Probe(context.Background())
	if !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("err = %v, want ErrTokenInvalid", err)
	}
	if errors.Is(err, ErrUnreachable) {
		t.Error("token rejection must not classify as unreachable")
	}
}

func TestProbeLoggedOutTokenInvalid(t *testing.T) {
	fa := &fakeArgo{loggedIn: false}
	srv := httptest.NewServer(fa.handler())
	defer srv.Close()

	m := newTestManager(config.Config{})
	m.SetConfig(srv.URL, "some-token", false, true)

	if err := m.Probe(context.Background()); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("err = %v, want ErrTokenInvalid", err)
	}
}

func TestProbeNoTokenSkipsAuthCheck(t *testing.T) {
	fa := &fakeArgo{token: "required", loggedIn: true}
	srv := httptest.NewServer(fa.handler())
	defer srv.Close()

	m := newTestManager(config.Config{})
	m.SetConfig(srv.URL, "", false, true)

	if err := m.Probe(context.Background()); err != nil {
		t.Fatalf("Probe without token should only check reachability: %v", err)
	}
}

func TestSeedFromPersistedConfig(t *testing.T) {
	fa := &fakeArgo{token: "persisted", loggedIn: true}
	srv := httptest.NewServer(fa.handler())
	defer srv.Close()

	m := newTestManager(config.Config{
		ArgoCDURL:   srv.URL,
		ArgoCDToken: "persisted",
	})
	if err := m.Probe(context.Background()); err != nil {
		t.Fatalf("Probe with seeded config: %v", err)
	}
}

func TestManagedResourcesCachedTTL(t *testing.T) {
	fa := &fakeArgo{loggedIn: true, managedResources: `{"items": [{"kind": "Deployment", "name": "web"}]}`}
	srv := httptest.NewServer(fa.handler())
	defer srv.Close()

	m := newTestManager(config.Config{})
	m.SetConfig(srv.URL, "", false, true)

	q := argoapi.ManagedResourcesQuery{AppName: "guestbook", AppNamespace: "argocd"}
	first, err := m.ManagedResourcesCached(context.Background(), q)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if len(first) != 1 || first[0].Name != "web" {
		t.Fatalf("items = %+v", first)
	}
	if _, err := m.ManagedResourcesCached(context.Background(), q); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if got := fa.managedRequests.Load(); got != 1 {
		t.Errorf("upstream requests = %d, want 1 (second call should hit cache)", got)
	}

	other := argoapi.ManagedResourcesQuery{AppName: "other-app", AppNamespace: "argocd"}
	if _, err := m.ManagedResourcesCached(context.Background(), other); err != nil {
		t.Fatalf("other app: %v", err)
	}
	if got := fa.managedRequests.Load(); got != 2 {
		t.Errorf("upstream requests = %d, want 2 (different app is a different key)", got)
	}

	filtered := argoapi.ManagedResourcesQuery{AppName: "guestbook", AppNamespace: "argocd", Kind: "Deployment"}
	if _, err := m.ManagedResourcesCached(context.Background(), filtered); err != nil {
		t.Fatalf("filtered: %v", err)
	}
	if got := fa.managedRequests.Load(); got != 3 {
		t.Errorf("upstream requests = %d, want 3 (filtered query bypasses cache)", got)
	}
}

func TestResetDropsConnectionKeepsConfig(t *testing.T) {
	fa := &fakeArgo{loggedIn: true}
	srv := httptest.NewServer(fa.handler())
	defer srv.Close()

	m := newTestManager(config.Config{})
	m.SetConfig(srv.URL, "", false, true)
	if err := m.Probe(context.Background()); err != nil {
		t.Fatalf("Probe: %v", err)
	}

	m.Reset()
	if _, ok := m.Get(); ok {
		t.Fatal("Get should report not connected after Reset")
	}
	if err := m.Probe(context.Background()); err != nil {
		t.Fatalf("Probe after Reset should reconnect using kept config: %v", err)
	}
}

func TestSetConfigRepoints(t *testing.T) {
	fa := &fakeArgo{loggedIn: true}
	srv := httptest.NewServer(fa.handler())
	defer srv.Close()

	m := newTestManager(config.Config{})
	m.SetConfig(srv.URL, "", false, true)
	if err := m.Probe(context.Background()); err != nil {
		t.Fatalf("Probe: %v", err)
	}

	m.SetConfig("http://127.0.0.1:1", "", false, true)
	if _, ok := m.Get(); ok {
		t.Fatal("Get should report not connected immediately after SetConfig")
	}
	if err := m.Probe(context.Background()); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("err = %v, want ErrUnreachable against the new URL", err)
	}
}

func TestDiscoverCandidates(t *testing.T) {
	svc := func(ns, name string, ports ...corev1.ServicePort) *corev1.Service {
		return &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ns,
				Name:      name,
				Labels:    map[string]string{"app.kubernetes.io/name": "argocd-server"},
			},
			Spec: corev1.ServiceSpec{Ports: ports},
		}
	}

	client := fake.NewSimpleClientset(
		svc("other-ns", "argocd-server",
			corev1.ServicePort{Name: "http", Port: 80, TargetPort: intstr.FromInt32(8080)},
		),
		svc("argocd", "argocd-server",
			corev1.ServicePort{Name: "http", Port: 80, TargetPort: intstr.FromInt32(8080)},
			corev1.ServicePort{Name: "https", Port: 443, TargetPort: intstr.FromInt32(8080)},
		),
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: "argocd",
				Name:      "argocd-repo-server",
				Labels:    map[string]string{"app.kubernetes.io/name": "argocd-repo-server"},
			},
			Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 8081}}},
		},
	)

	cands, err := discoverCandidates(context.Background(), client)
	if err != nil {
		t.Fatalf("discoverCandidates: %v", err)
	}
	if len(cands) != 2 {
		t.Fatalf("len(candidates) = %d, want 2 (repo-server label must not match)", len(cands))
	}

	first := cands[0]
	if first.namespace != "argocd" {
		t.Errorf("first candidate namespace = %q, want argocd first", first.namespace)
	}
	if first.scheme != "https" || first.port != 443 {
		t.Errorf("first candidate = %+v, want https:443 preferred", first)
	}
	if first.targetPort != 8080 {
		t.Errorf("targetPort = %d, want 8080 (container port)", first.targetPort)
	}
	if got := first.clusterURL(); got != "https://argocd-server.argocd.svc:443" {
		t.Errorf("clusterURL = %q", got)
	}

	second := cands[1]
	if second.scheme != "http" || second.port != 80 {
		t.Errorf("second candidate = %+v, want http:80 fallback", second)
	}
}

// TestSeedRestoresTokenContext pins the fix for "auto-discovery token never
// reconnects": a persisted auto-discovery token's context binding is restored on
// seed, so a same-context restart reconnects instead of failing the context
// guard — while a different context still correctly refuses.
func TestSeedRestoresTokenContext(t *testing.T) {
	ctx := "cluster-a"
	m := newTestManager(config.Config{
		ArgoCDToken:        "secret-token",
		ArgoCDTokenContext: "cluster-a",
	})
	m.contextName = func() string { return ctx }

	// Same context as the persisted binding: the context guard must pass (the
	// probe then fails later on discovery with no k8s client, but NOT as a
	// context mismatch).
	if err := m.Probe(context.Background()); errors.Is(err, errTokenContextMismatch) {
		t.Fatal("seed must restore tokenContext so a same-context restart doesn't mismatch")
	}

	// Different context: the cross-cluster guard must still refuse.
	ctx = "cluster-b"
	m.Reset()
	if err := m.Probe(context.Background()); !errors.Is(err, errTokenContextMismatch) {
		t.Fatalf("different context must still mismatch, got %v", err)
	}
}

func TestWatchForwardExit_DropsWhenCurrentForwardDies(t *testing.T) {
	m := newTestManager(config.Config{})
	fwd := &activeForward{localPort: 12345, stopCh: make(chan struct{}), cancel: func() {}}
	m.forward = fwd
	m.baseURL = "https://localhost:12345"
	m.client = newClient(m.baseURL, "tok", false)

	// Pre-loaded errCh makes watchForwardExit return synchronously.
	errCh := make(chan error, 1)
	errCh <- errors.New("pod restarted")
	m.watchForwardExit(fwd, errCh)

	if m.forward != nil || m.client != nil || m.baseURL != "" {
		t.Fatalf("expected connection dropped after unexpected forward exit; forward=%v client=%v baseURL=%q", m.forward, m.client, m.baseURL)
	}
	// Get() must now report not-connected so a reprobe is triggered.
	if _, ok := m.Get(); ok {
		t.Error("Get() should report not-connected after the forward was dropped")
	}
}

func TestWatchForwardExit_NoOpAfterForwardReplaced(t *testing.T) {
	m := newTestManager(config.Config{})
	oldFwd := &activeForward{localPort: 1, stopCh: make(chan struct{}), cancel: func() {}}
	newFwd := &activeForward{localPort: 2, stopCh: make(chan struct{}), cancel: func() {}}
	// A deliberate reconnect already replaced oldFwd with newFwd.
	m.forward = newFwd
	m.baseURL = "https://localhost:2"
	m.client = newClient(m.baseURL, "tok", false)

	errCh := make(chan error, 1)
	errCh <- nil
	m.watchForwardExit(oldFwd, errCh)

	if m.forward != newFwd || m.client == nil || m.baseURL != "https://localhost:2" {
		t.Fatal("a superseded forward's exit must not drop the current connection")
	}
}

// TestSeedFromEnvBindsAndMarksManaged pins that an env-provisioned token is bound
// to the deploy-time context (so the auto-discovery cross-cluster guard accepts it
// for the cluster it runs in) and is flagged environment-managed.
func TestSeedFromEnvBindsAndMarksManaged(t *testing.T) {
	ctx := "in-cluster"
	m := newTestManager(config.Config{})
	m.contextName = func() string { return ctx }

	m.SeedFromEnv("", "env-token", false)
	if !m.envManaged {
		t.Fatal("SeedFromEnv must mark the manager environment-managed")
	}
	if m.tokenContext != "in-cluster" {
		t.Fatalf("tokenContext = %q, want the deploy-time context", m.tokenContext)
	}
	if !m.IsConfigured() {
		t.Fatal("an env-seeded token must count as configured")
	}
	// Same context: the cross-cluster guard must accept it (a context switch would
	// still refuse it, exactly as a UI-set auto-discovery token).
	if err := m.Probe(context.Background()); errors.Is(err, errTokenContextMismatch) {
		t.Fatalf("Probe on the deploy context = %v, must not be a context mismatch", err)
	}
}

// TestEnvManagedConfigReflectsEffective pins that EnvManagedConfig returns the
// effective env URL/TLS (never the token) only when env-managed — the config
// endpoint uses this so the read-only card shows the real endpoint, not disk.
func TestEnvManagedConfigReflectsEffective(t *testing.T) {
	m := newTestManager(config.Config{ArgoCDURL: "https://disk.example.com", ArgoCDToken: "disk-token"})
	m.contextName = func() string { return "in-cluster" }

	if _, _, ok := m.EnvManagedConfig(); ok {
		// Before seeding it lazily loads the disk config, which is NOT env-managed.
		t.Fatal("EnvManagedConfig must report ok=false when not env-managed")
	}

	m.SeedFromEnv("https://argocd.example.com", "env-token", true)
	url, insecure, ok := m.EnvManagedConfig()
	if !ok || url != "https://argocd.example.com" || !insecure {
		t.Fatalf("EnvManagedConfig = (%q, %v, %v), want the effective env values", url, insecure, ok)
	}
}

// TestResolveEnvArgo covers the source-precedence + validation paths without
// touching the manager (M2): file>inline precedence, no-token no-op, invalid URL,
// userinfo rejection, and non-boolean TLS.
func TestResolveEnvArgo(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	readOK := func(want string) func(string) ([]byte, error) {
		return func(p string) ([]byte, error) { return []byte(want), nil }
	}

	t.Run("no token is a no-op", func(t *testing.T) {
		_, _, _, ok, err := resolveEnvArgo(env(map[string]string{envArgoURL: "https://x.example.com"}), readOK(""))
		if ok || err != nil {
			t.Fatalf("got ok=%v err=%v, want (false, nil) when no token is set", ok, err)
		}
	})

	t.Run("inline token", func(t *testing.T) {
		url, token, insecure, ok, err := resolveEnvArgo(env(map[string]string{envArgoToken: "  inline  "}), readOK(""))
		if !ok || err != nil || token != "inline" || url != "" || insecure {
			t.Fatalf("got (%q,%q,%v,%v,%v)", url, token, insecure, ok, err)
		}
	})

	t.Run("file takes precedence over inline", func(t *testing.T) {
		_, token, _, ok, err := resolveEnvArgo(
			env(map[string]string{envArgoToken: "inline", envArgoTokenFile: "/run/secrets/argo"}),
			readOK("file-token\n"),
		)
		if !ok || err != nil || token != "file-token" {
			t.Fatalf("got token=%q ok=%v err=%v, want the file token", token, ok, err)
		}
	})

	t.Run("empty file errors", func(t *testing.T) {
		_, _, _, _, err := resolveEnvArgo(env(map[string]string{envArgoTokenFile: "/run/secrets/argo"}), readOK("  \n"))
		if err == nil {
			t.Fatal("an empty token file must error, not silently fall through")
		}
	})

	t.Run("invalid URL errors", func(t *testing.T) {
		_, _, _, _, err := resolveEnvArgo(env(map[string]string{envArgoToken: "t", envArgoURL: "ftp://x"}), readOK(""))
		if err == nil {
			t.Fatal("a non-http(s) URL must error")
		}
	})

	t.Run("userinfo URL errors", func(t *testing.T) {
		_, _, _, _, err := resolveEnvArgo(env(map[string]string{envArgoToken: "t", envArgoURL: "https://u:p@x.example.com"}), readOK(""))
		if err == nil {
			t.Fatal("a URL embedding userinfo credentials must error")
		}
	})

	t.Run("malformed URL error does not echo the raw input", func(t *testing.T) {
		// A bad percent-escape in the userinfo makes url.Parse fail — the error must
		// not carry the raw credential-bearing string into the logs.
		secret := "s3cr3t%zz"
		_, _, _, _, err := resolveEnvArgo(env(map[string]string{envArgoToken: "t", envArgoURL: "https://u:" + secret + "@x.example.com"}), readOK(""))
		if err == nil {
			t.Fatal("a malformed URL must error")
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error %q leaked the raw URL/credential", err)
		}
	})

	t.Run("non-boolean TLS errors", func(t *testing.T) {
		_, _, _, _, err := resolveEnvArgo(env(map[string]string{envArgoToken: "t", envArgoInsecure: "yesplease"}), readOK(""))
		if err == nil {
			t.Fatal("a non-boolean insecure flag must error, not silently default")
		}
	})

	t.Run("valid full config", func(t *testing.T) {
		url, token, insecure, ok, err := resolveEnvArgo(env(map[string]string{
			envArgoToken:    "t",
			envArgoURL:      "https://argocd.example.com/",
			envArgoInsecure: "true",
		}), readOK(""))
		if !ok || err != nil || token != "t" || url != "https://argocd.example.com/" || !insecure {
			t.Fatalf("got (%q,%q,%v,%v,%v)", url, token, insecure, ok, err)
		}
	})
}
