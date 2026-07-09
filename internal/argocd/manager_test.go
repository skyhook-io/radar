package argocd

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
// context switch makes the probe drop it (never sending it to a different
// cluster's discovered Argo). Discovery has no k8s client here, so the probe
// can't connect — the assertion is that the token was neutralized for the
// wrong context.
func TestAutoDiscoveryTokenBoundToContext(t *testing.T) {
	ctx := "cluster-a"
	m := newTestManager(config.Config{})
	m.contextName = func() string { return ctx }

	m.SetConfig("", "secret-token", false) // auto-discovery mode
	if m.tokenContext != "cluster-a" {
		t.Fatalf("tokenContext = %q, want the config-time context", m.tokenContext)
	}

	// Same context: the snapshot keeps the token.
	m.mu.Lock()
	snap := m.snapshotLocked()
	m.mu.Unlock()
	if snap.manualURL == "" && snap.token != "" && snap.tokenContext != m.currentContextName() {
		t.Fatal("token should be usable in the bound context")
	}

	// Switch context: the same guard now drops the token.
	ctx = "cluster-b"
	if !(snap.manualURL == "" && snap.token != "" && snap.tokenContext != m.currentContextName()) {
		t.Fatal("token must be dropped after a context switch (bound to cluster-a, now on cluster-b)")
	}
}

func TestProbeManualURL(t *testing.T) {
	fa := &fakeArgo{token: "good", loggedIn: true}
	srv := httptest.NewServer(fa.handler())
	defer srv.Close()

	m := newTestManager(config.Config{})
	m.SetConfig(srv.URL, "good", false)

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
	m.SetConfig("http://127.0.0.1:1", "", false)

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
	m.SetConfig(srv.URL, "wrong", false)

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
	m.SetConfig(srv.URL, "some-token", false)

	if err := m.Probe(context.Background()); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("err = %v, want ErrTokenInvalid", err)
	}
}

func TestProbeNoTokenSkipsAuthCheck(t *testing.T) {
	fa := &fakeArgo{token: "required", loggedIn: true}
	srv := httptest.NewServer(fa.handler())
	defer srv.Close()

	m := newTestManager(config.Config{})
	m.SetConfig(srv.URL, "", false)

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
	m.SetConfig(srv.URL, "", false)

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
	m.SetConfig(srv.URL, "", false)
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
	m.SetConfig(srv.URL, "", false)
	if err := m.Probe(context.Background()); err != nil {
		t.Fatalf("Probe: %v", err)
	}

	m.SetConfig("http://127.0.0.1:1", "", false)
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
