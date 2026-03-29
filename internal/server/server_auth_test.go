package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
)

// newAuthServer creates a minimal Server with the given auth config for testing.
// No router or k8s client — only use for direct handler tests.
func newAuthServer(cfg auth.Config) *Server {
	cfg.Defaults()
	s := &Server{authConfig: cfg}
	if cfg.Enabled() {
		s.permCache = auth.NewPermissionCache()
	}
	return s
}

// requestWithUser creates an HTTP request with an authenticated user in context.
func requestWithUser(method, path string, user *auth.User) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	if user != nil {
		r = r.WithContext(auth.ContextWithUser(r.Context(), user))
	}
	return r
}

// --- handleAuthMe ---

func TestHandleAuthMe_AuthDisabled(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "none"})
	w := httptest.NewRecorder()
	s.handleAuthMe(w, httptest.NewRequest("GET", "/api/auth/me", nil))

	var body map[string]any
	json.NewDecoder(w.Body).Decode(&body)

	if body["authEnabled"] != false {
		t.Errorf("authEnabled = %v, want false", body["authEnabled"])
	}
	if _, has := body["username"]; has {
		t.Error("username should not be present when no user in context")
	}
}

func TestHandleAuthMe_AuthEnabled_NoUser(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	w := httptest.NewRecorder()
	s.handleAuthMe(w, httptest.NewRequest("GET", "/api/auth/me", nil))

	var body map[string]any
	json.NewDecoder(w.Body).Decode(&body)

	if body["authEnabled"] != true {
		t.Errorf("authEnabled = %v, want true", body["authEnabled"])
	}
	if _, has := body["username"]; has {
		t.Error("username should not be present when no user in context")
	}
}

func TestHandleAuthMe_AuthEnabled_WithUser(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	w := httptest.NewRecorder()
	r := requestWithUser("GET", "/api/auth/me", &auth.User{
		Username: "alice",
		Groups:   []string{"devs", "admins"},
	})
	s.handleAuthMe(w, r)

	var body map[string]any
	json.NewDecoder(w.Body).Decode(&body)

	if body["authEnabled"] != true {
		t.Errorf("authEnabled = %v, want true", body["authEnabled"])
	}
	if body["username"] != "alice" {
		t.Errorf("username = %v, want alice", body["username"])
	}
	groups, _ := body["groups"].([]any)
	if len(groups) != 2 {
		t.Errorf("groups = %v, want 2 groups", groups)
	}
}

// --- parseNamespaces ---

func TestParseNamespaces(t *testing.T) {
	tests := []struct {
		name   string
		query  string
		want   []string
		wantNil bool
	}{
		{"no params", "", nil, true},
		{"single namespace param", "namespace=prod", []string{"prod"}, false},
		{"plural namespaces param", "namespaces=dev,staging,prod", []string{"dev", "staging", "prod"}, false},
		{"plural takes precedence", "namespaces=dev&namespace=prod", []string{"dev"}, false},
		{"trims whitespace", "namespaces= dev , staging ", []string{"dev", "staging"}, false},
		{"filters empty segments", "namespaces=dev,,staging,", []string{"dev", "staging"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, _ := url.ParseQuery(tt.query)
			got := parseNamespaces(q)
			if tt.wantNil {
				if got != nil {
					t.Errorf("expected nil, got %v", got)
				}
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i, ns := range got {
				if ns != tt.want[i] {
					t.Errorf("got[%d] = %q, want %q", i, ns, tt.want[i])
				}
			}
		})
	}
}

// --- noNamespaceAccess (nil vs empty contract) ---

func TestNoNamespaceAccess(t *testing.T) {
	tests := []struct {
		name string
		ns   []string
		want bool
	}{
		{"nil means all namespaces", nil, false},
		{"empty means no access", []string{}, true},
		{"populated means filtered", []string{"dev"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := noNamespaceAccess(tt.ns); got != tt.want {
				t.Errorf("noNamespaceAccess(%v) = %v, want %v", tt.ns, got, tt.want)
			}
		})
	}
}

// --- getUserNamespaces ---

func TestGetUserNamespaces_NoAuth(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "none"})
	r := httptest.NewRequest("GET", "/", nil) // no user in context

	got := s.getUserNamespaces(r, []string{"dev", "prod"})
	if len(got) != 2 || got[0] != "dev" || got[1] != "prod" {
		t.Errorf("no auth should passthrough: got %v", got)
	}
}

func TestGetUserNamespaces_NoAuth_NilRequested(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "none"})
	r := httptest.NewRequest("GET", "/", nil)

	got := s.getUserNamespaces(r, nil)
	if got != nil {
		t.Errorf("no auth + nil requested should return nil (all namespaces), got %v", got)
	}
}

func TestGetUserNamespaces_CachedClusterAdmin(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	user := &auth.User{Username: "admin"}
	// nil AllowedNamespaces = cluster admin (all namespaces)
	s.permCache.Set("admin", &auth.UserPermissions{AllowedNamespaces: nil})

	r := requestWithUser("GET", "/", user)
	got := s.getUserNamespaces(r, []string{"dev", "prod"})

	if len(got) != 2 || got[0] != "dev" || got[1] != "prod" {
		t.Errorf("cluster admin should see all requested: got %v", got)
	}
}

func TestGetUserNamespaces_CachedRestricted(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	user := &auth.User{Username: "alice"}
	s.permCache.Set("alice", &auth.UserPermissions{AllowedNamespaces: []string{"dev", "staging"}})

	r := requestWithUser("GET", "/", user)
	got := s.getUserNamespaces(r, []string{"dev", "prod", "staging"})

	// Should intersect: dev + staging allowed, prod denied
	allowed := map[string]bool{}
	for _, ns := range got {
		allowed[ns] = true
	}
	if len(got) != 2 || !allowed["dev"] || !allowed["staging"] {
		t.Errorf("expected [dev staging], got %v", got)
	}
	if allowed["prod"] {
		t.Error("prod should not be in result")
	}
}

func TestGetUserNamespaces_CachedNoAccess(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	user := &auth.User{Username: "nobody"}
	// empty (not nil) = no access
	s.permCache.Set("nobody", &auth.UserPermissions{AllowedNamespaces: []string{}})

	r := requestWithUser("GET", "/", user)
	got := s.getUserNamespaces(r, []string{"dev"})

	if !noNamespaceAccess(got) {
		t.Errorf("empty AllowedNamespaces should yield no access, got %v (nil=%v)", got, got == nil)
	}
}

func TestGetUserNamespaces_CachedRestricted_AllNamespacesRequested(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	user := &auth.User{Username: "alice"}
	s.permCache.Set("alice", &auth.UserPermissions{AllowedNamespaces: []string{"dev", "staging"}})

	r := requestWithUser("GET", "/", user)
	// nil requested = "all namespaces" → should return user's allowed list
	got := s.getUserNamespaces(r, nil)

	if len(got) != 2 {
		t.Errorf("expected user's 2 allowed namespaces, got %v", got)
	}
}

func TestGetUserNamespaces_UncachedFailsClosed_WhenK8sNotReady(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	user := &auth.User{Username: "alice"}
	// Don't pre-populate cache — forces discovery path.
	// k8s.GetClient() returns nil in test env → fail-closed (deny access).

	r := requestWithUser("GET", "/", user)
	got := s.getUserNamespaces(r, []string{"dev"})

	// When k8s client isn't ready, deny access (fail-closed)
	if !noNamespaceAccess(got) {
		t.Errorf("expected no access (fail-closed) when k8s not ready, got %v", got)
	}
}

// --- Proxy auth smoke tests (full router with middleware) ---

// authTestEnv holds a test server with proxy auth enabled plus the
// underlying Server for direct access to permCache.
type authTestEnv struct {
	ts  *httptest.Server
	srv *Server
}

// newAuthTestServer creates an httptest.Server with proxy auth enabled.
// Depends on k8s cache being initialized by TestMain in server_smoke_test.go.
func newAuthTestServer(t *testing.T) *authTestEnv {
	t.Helper()
	srv := New(Config{
		DevMode: true,
		AuthConfig: auth.Config{
			Mode:         "proxy",
			Secret:       "test-secret",
			UserHeader:   "X-Forwarded-User",
			GroupsHeader: "X-Forwarded-Groups",
		},
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		srv.Stop()
	})
	return &authTestEnv{ts: ts, srv: srv}
}

// authGet sends a GET with proxy auth headers to the test server.
func (e *authTestEnv) authGet(t *testing.T, path, user, groups string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", e.ts.URL+path, nil)
	req.Header.Set("X-Forwarded-User", user)
	if groups != "" {
		req.Header.Set("X-Forwarded-Groups", groups)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

func TestProxyAuth_UnauthenticatedBlocked(t *testing.T) {
	env := newAuthTestServer(t)

	endpoints := []string{
		"/api/topology",
		"/api/resources/pods",
		"/api/resources/deployments/default/nginx",
		"/api/namespaces",
		"/api/events",
		"/api/changes",
		"/api/dashboard",
		"/mcp",
	}

	for _, path := range endpoints {
		t.Run(path, func(t *testing.T) {
			resp, err := http.Get(env.ts.URL + path)
			if err != nil {
				t.Fatalf("GET %s: %v", path, err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("expected 401, got %d", resp.StatusCode)
			}
		})
	}
}

func TestProxyAuth_ExemptPaths(t *testing.T) {
	env := newAuthTestServer(t)

	// Health should work without auth
	resp, err := http.Get(env.ts.URL + "/api/health")
	if err != nil {
		t.Fatalf("GET /api/health: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestProxyAuth_AuthMeSoftAuth(t *testing.T) {
	env := newAuthTestServer(t)

	// /api/auth/me should work without auth (soft-auth path)
	resp, err := http.Get(env.ts.URL + "/api/auth/me")
	if err != nil {
		t.Fatalf("GET /api/auth/me: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)
	if body["authEnabled"] != true {
		t.Errorf("authEnabled = %v, want true", body["authEnabled"])
	}
	if _, has := body["username"]; has {
		t.Error("username should not be present without auth")
	}
}

func TestProxyAuth_AuthenticatedAllowed(t *testing.T) {
	env := newAuthTestServer(t)

	req, _ := http.NewRequest("GET", env.ts.URL+"/api/topology", nil)
	req.Header.Set("X-Forwarded-User", "alice")
	req.Header.Set("X-Forwarded-Groups", "devs")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/topology: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 with proxy headers, got %d", resp.StatusCode)
	}
}

func TestProxyAuth_SessionCookieRoundTrip(t *testing.T) {
	env := newAuthTestServer(t)

	// First request with proxy headers — should get a session cookie back
	req, _ := http.NewRequest("GET", env.ts.URL+"/api/auth/me", nil)
	req.Header.Set("X-Forwarded-User", "bob")
	req.Header.Set("X-Forwarded-Groups", "ops")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Find session cookie
	var sessionCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == auth.DefaultCookieName {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("expected session cookie from proxy auth")
	}

	// Second request with just the cookie (no proxy headers) — should still work
	req2, _ := http.NewRequest("GET", env.ts.URL+"/api/auth/me", nil)
	req2.AddCookie(sessionCookie)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Errorf("expected 200 with session cookie, got %d", resp2.StatusCode)
	}

	var body map[string]any
	json.NewDecoder(resp2.Body).Decode(&body)
	if body["username"] != "bob" {
		t.Errorf("username = %v, want bob", body["username"])
	}
}

// --- Namespace filtering smoke tests ---

func TestProxyAuth_NamespaceFiltering_Restricted(t *testing.T) {
	env := newAuthTestServer(t)

	// Pre-populate cache: alice can only see "staging" (not "default" where resources live)
	env.srv.permCache.Set("alice", &auth.UserPermissions{
		AllowedNamespaces: []string{"staging"},
	})

	// Request pods — should get empty result since "default" is denied
	resp := env.authGet(t, "/api/resources/pods", "alice", "devs")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var pods []any
	json.NewDecoder(resp.Body).Decode(&pods)
	if len(pods) != 0 {
		t.Errorf("restricted user should see 0 pods, got %d", len(pods))
	}
}

func TestProxyAuth_NamespaceFiltering_Allowed(t *testing.T) {
	env := newAuthTestServer(t)

	// Pre-populate cache: bob can see "default" (where test resources live)
	env.srv.permCache.Set("bob", &auth.UserPermissions{
		AllowedNamespaces: []string{"default"},
	})

	resp := env.authGet(t, "/api/resources/pods", "bob", "ops")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var pods []any
	json.NewDecoder(resp.Body).Decode(&pods)
	if len(pods) == 0 {
		t.Error("allowed user should see pods in default namespace")
	}
}

func TestProxyAuth_NamespaceFiltering_Topology(t *testing.T) {
	env := newAuthTestServer(t)

	// Pre-populate cache: restricted to a namespace with no resources
	env.srv.permCache.Set("viewer", &auth.UserPermissions{
		AllowedNamespaces: []string{"empty-ns"},
	})

	resp := env.authGet(t, "/api/topology", "viewer", "")
	defer resp.Body.Close()

	var body map[string]any
	json.NewDecoder(resp.Body).Decode(&body)

	nodes, _ := body["nodes"].([]any)
	if len(nodes) != 0 {
		t.Errorf("restricted user should see 0 topology nodes, got %d", len(nodes))
	}
}

func TestProxyAuth_NamespaceFiltering_ClusterAdmin(t *testing.T) {
	env := newAuthTestServer(t)

	// Pre-populate cache: nil AllowedNamespaces = cluster admin
	env.srv.permCache.Set("admin", &auth.UserPermissions{
		AllowedNamespaces: nil,
	})

	resp := env.authGet(t, "/api/resources/deployments", "admin", "system:masters")
	defer resp.Body.Close()

	var deps []any
	json.NewDecoder(resp.Body).Decode(&deps)
	if len(deps) == 0 {
		t.Error("cluster admin should see all deployments")
	}
}

func TestProxyAuth_NamespaceFiltering_NoAccess(t *testing.T) {
	env := newAuthTestServer(t)

	// Pre-populate cache: empty slice = no access
	env.srv.permCache.Set("nobody", &auth.UserPermissions{
		AllowedNamespaces: []string{},
	})

	resp := env.authGet(t, "/api/resources/pods", "nobody", "")
	defer resp.Body.Close()

	var pods []any
	json.NewDecoder(resp.Body).Decode(&pods)
	if len(pods) != 0 {
		t.Errorf("user with no access should see 0 pods, got %d", len(pods))
	}
}
