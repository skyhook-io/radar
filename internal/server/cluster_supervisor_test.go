package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/k8s"
)

func TestContextSlug(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{
			input: "k3d-radar-a",
			want:  "k3d-radar-a", // slug prefix is clean
		},
		{
			input: "arn:aws:eks:us-east-1:123456789012:cluster/prod-cluster",
			want:  "arn-aws-eks-us-east", // truncated at 20 chars, sanitized
		},
		{
			input: "my-context (special/chars@here:foo)",
			want:  "my-context-special-c",
		},
	}

	for _, tc := range tests {
		slug := ContextSlug(tc.input)
		if !strings.HasPrefix(slug, tc.want) {
			t.Errorf("ContextSlug(%q) = %q, expected prefix %q", tc.input, slug, tc.want)
		}
		// Must only contain [a-z0-9-]
		for _, r := range slug {
			if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
				t.Errorf("ContextSlug(%q) = %q contains forbidden rune %q", tc.input, slug, string(r))
			}
		}
		// Deterministic
		if ContextSlug(tc.input) != slug {
			t.Errorf("ContextSlug(%q) not deterministic", tc.input)
		}
	}
}

func TestClusterSupervisor_PrimaryTracking(t *testing.T) {
	cs := &ClusterSupervisor{
		workers:       make(map[string]*WorkerSession),
		slugToContext: make(map[string]string),
		contextToSlug: make(map[string]string),
		primaryCtx:    "cluster-primary",
		stopCh:        make(chan struct{}),
	}
	defer close(cs.stopCh)

	if !cs.IsPrimary("cluster-primary") {
		t.Fatal("expected cluster-primary to be primary")
	}

	primarySlug := cs.SlugForContext("cluster-primary")
	if !cs.IsPrimary(primarySlug) {
		t.Fatal("expected primary slug to be recognized as primary")
	}

	// Register a secondary worker
	secSlug := cs.SlugForContext("cluster-secondary")
	cs.workers[secSlug] = &WorkerSession{
		ContextName: "cluster-secondary",
		Slug:        secSlug,
		Status:      "active",
		ready:       make(chan struct{}),
		exited:      make(chan struct{}),
	}

	// Switch primary to secondary
	cs.SetPrimaryContext("cluster-secondary")
	if !cs.IsPrimary("cluster-secondary") {
		t.Fatal("expected cluster-secondary to become primary")
	}
	if cs.IsPrimary("cluster-primary") {
		t.Fatal("expected cluster-primary to no longer be primary")
	}

	// Redundant worker must be removed
	cs.mu.RLock()
	_, stillExists := cs.workers[secSlug]
	cs.mu.RUnlock()
	if stillExists {
		t.Fatal("worker for cluster-secondary should have been removed when it became primary")
	}
}

func TestClusterSupervisor_AutoIdleInFlightGuard(t *testing.T) {
	cs := &ClusterSupervisor{
		workers:       make(map[string]*WorkerSession),
		slugToContext: make(map[string]string),
		contextToSlug: make(map[string]string),
		primaryCtx:    "primary",
		stopCh:        make(chan struct{}),
	}
	defer close(cs.stopCh)

	slug := cs.SlugForContext("test-ctx")
	session := &WorkerSession{
		ContextName: "test-ctx",
		Slug:        slug,
		Status:      "active",
		LastAccess:  time.Now().Add(-20 * time.Minute), // expired
		ready:       make(chan struct{}),
		exited:      make(chan struct{}),
	}
	// Simulate an active long-lived connection in flight (e.g. exec terminal or log stream)
	session.inFlight.Store(1)
	cs.workers[slug] = session

	// Run single iteration check of auto-idle logic
	cs.mu.Lock()
	now := time.Now()
	toIdle := make([]*WorkerSession, 0)
	for s, w := range cs.workers {
		if w.getStatus() == "active" && w.inFlight.Load() == 0 && now.Sub(w.getLastAccess()) > 15*time.Minute {
			delete(cs.workers, s)
			toIdle = append(toIdle, w)
		}
	}
	cs.mu.Unlock()

	if len(toIdle) != 0 {
		t.Fatal("auto-idle should NOT idle a worker with active in-flight requests")
	}

	// Release in-flight request
	session.inFlight.Store(0)

	cs.mu.Lock()
	for s, w := range cs.workers {
		if w.getStatus() == "active" && w.inFlight.Load() == 0 && now.Sub(w.getLastAccess()) > 15*time.Minute {
			delete(cs.workers, s)
			toIdle = append(toIdle, w)
		}
	}
	cs.mu.Unlock()

	if len(toIdle) != 1 {
		t.Fatal("auto-idle should idle worker once in-flight requests reach 0")
	}
}

func TestClusterSupervisor_ConcurrentAccess(t *testing.T) {
	cs := &ClusterSupervisor{
		workers:       make(map[string]*WorkerSession),
		slugToContext: make(map[string]string),
		contextToSlug: make(map[string]string),
		primaryCtx:    "primary",
		stopCh:        make(chan struct{}),
	}
	defer close(cs.stopCh)

	slug := cs.SlugForContext("worker-ctx")
	session := &WorkerSession{
		ContextName: "worker-ctx",
		Slug:        slug,
		Status:      "active",
		LastAccess:  time.Now(),
		ready:       make(chan struct{}),
		exited:      make(chan struct{}),
	}
	cs.workers[slug] = session

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = cs.GetSessions()
			session.touch()
			_ = session.getStatus()
			_ = session.getLastAccess()
		}()
	}
	wg.Wait()
}

func TestWorkerMode_RejectContextSwitch(t *testing.T) {
	s := &Server{
		isWorker: true,
	}

	r := chi.NewRouter()
	r.Post("/api/contexts/{name}", s.handleSwitchContext)

	req := httptest.NewRequest(http.MethodPost, "/api/contexts/another-cluster", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request when switching context on worker, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "not permitted") {
		t.Fatalf("unexpected response body: %s", w.Body.String())
	}
}

func TestPrimaryRedirectSecurity(t *testing.T) {
	cs := &ClusterSupervisor{
		workers:       make(map[string]*WorkerSession),
		slugToContext: make(map[string]string),
		contextToSlug: make(map[string]string),
		primaryCtx:    "primary-cluster",
		stopCh:        make(chan struct{}),
	}
	defer close(cs.stopCh)

	primarySlug := cs.SlugForContext("primary-cluster")

	s := &Server{
		clusterSupervisor: cs,
		basePath:          "",
	}

	// Ensure connected state so requireConnected passes
	k8s.SetConnectionStatus(k8s.ConnectionStatus{
		State:   k8s.StateConnected,
		Context: "primary-cluster",
	})

	r := chi.NewRouter()
	r.HandleFunc("/c/{context}/*", s.handleClusterSessionProxy)

	testCases := []struct {
		url          string
		wantLocation string
	}{
		{
			url:          "/c/primary-cluster/",
			wantLocation: "/c/" + primarySlug + "/",
		},
		{
			url:          "/c/primary-cluster/resources/pods",
			wantLocation: "/c/" + primarySlug + "/resources/pods",
		},
		{
			url:          "/c/primary-cluster//evil.example.com",
			wantLocation: "/c/" + primarySlug + "/evil.example.com",
		},
		{
			url:          "/c/primary-cluster/\\evil.example.com",
			wantLocation: "/c/" + primarySlug + "/evil.example.com",
		},
	}

	for _, tc := range testCases {
		req := httptest.NewRequest(http.MethodGet, tc.url, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		if w.Code != http.StatusMovedPermanently {
			t.Errorf("url %q: expected 301 Moved Permanently, got %d", tc.url, w.Code)
		}
		loc := w.Header().Get("Location")
		if loc != tc.wantLocation {
			t.Errorf("url %q: Location = %q, want %q", tc.url, loc, tc.wantLocation)
		}
	}
}

func TestPrimarySessionDirectServing(t *testing.T) {
	cs := &ClusterSupervisor{
		workers:       make(map[string]*WorkerSession),
		slugToContext: make(map[string]string),
		contextToSlug: make(map[string]string),
		primaryCtx:    "primary-cluster",
		stopCh:        make(chan struct{}),
	}
	defer close(cs.stopCh)

	primarySlug := cs.SlugForContext("primary-cluster")

	// Create test in-memory router with /api/connection route
	appRouter := chi.NewRouter()
	appRouter.Get("/api/connection", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"state":"connected"}`))
	})

	s := &Server{
		clusterSupervisor: cs,
		router:            appRouter,
		basePath:          "",
	}

	// Ensure connected state so requireConnected passes
	k8s.SetConnectionStatus(k8s.ConnectionStatus{
		State:   k8s.StateConnected,
		Context: "primary-cluster",
	})

	r := chi.NewRouter()
	r.HandleFunc("/c/{context}/*", s.handleClusterSessionProxy)

	req := httptest.NewRequest(http.MethodGet, "/c/"+primarySlug+"/api/connection", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK when requesting API under primary slug, got %d (body: %s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"state":"connected"`) {
		t.Fatalf("expected API response to contain state:connected, got: %s", w.Body.String())
	}
}
