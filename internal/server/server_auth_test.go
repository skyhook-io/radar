package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
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
	// Non-Cloud user — cloudRole must be absent so the frontend's
	// useCloudRole hook treats them as "not under Cloud."
	if _, has := body["cloudRole"]; has {
		t.Errorf("cloudRole should not be present for non-Cloud user (groups=%v)", groups)
	}
}

func TestHandleAuthMe_CloudUser_ExposesCloudRole(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	w := httptest.NewRecorder()
	r := requestWithUser("GET", "/api/auth/me", &auth.User{
		Username: "bob",
		Groups:   []string{"cloud:viewer", "cloud:org:abc"},
	})
	s.handleAuthMe(w, r)

	var body map[string]any
	json.NewDecoder(w.Body).Decode(&body)

	if body["cloudRole"] != "viewer" {
		t.Errorf("cloudRole = %v, want viewer", body["cloudRole"])
	}
}

// --- parseNamespaces ---

func TestParseNamespaces(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		want    []string
		wantNil bool
	}{
		{"no params", "", nil, true},
		{"single namespace param", "namespace=prod", []string{"prod"}, false},
		{"plural namespaces param", "namespaces=dev,staging,prod", []string{"dev", "staging", "prod"}, false},
		{"plural takes precedence", "namespaces=dev&namespace=prod", []string{"dev"}, false},
		{"trims whitespace", "namespaces= dev , staging ", []string{"dev", "staging"}, false},
		{"filters empty segments", "namespaces=dev,,staging,", []string{"dev", "staging"}, false},
		{"dedupes namespaces preserving order", "namespaces=dev,staging,dev,prod,staging", []string{"dev", "staging", "prod"}, false},
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

func TestProxyAuth_PodMetricsNamespaceGated(t *testing.T) {
	paths := []string{
		"/api/metrics/pods/default/web-0",
		"/api/metrics/pods/default/web-0/history",
	}

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			env := newAuthTestServer(t)
			env.srv.permCache.Set("carol", &auth.UserPermissions{AllowedNamespaces: []string{"other"}})

			resp := env.authGet(t, path, "carol", "")
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("%s: status = %d, want %d", path, resp.StatusCode, http.StatusForbidden)
			}
		})
	}
}

// authPost sends a POST with proxy auth headers and a JSON body.
func (e *authTestEnv) authPost(t *testing.T, path, user, groups, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("POST", e.ts.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-User", user)
	if groups != "" {
		req.Header.Set("X-Forwarded-Groups", groups)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
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
		"/api/connection",
		"/mcp",
		// pprof is mounted on non-cloud builds; the auth middleware must
		// still gate it (leaks the in-memory K8s cache otherwise).
		"/debug/pprof/heap",
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

	// POST /api/connection/retry is the reported state-changing endpoint —
	// assert the mutating verb is gated, not just the GET surface.
	t.Run("POST /api/connection/retry", func(t *testing.T) {
		resp, err := http.Post(env.ts.URL+"/api/connection/retry", "application/json", nil)
		if err != nil {
			t.Fatalf("POST /api/connection/retry: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("expected 401, got %d", resp.StatusCode)
		}
	})
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

func TestProxyAuth_DashboardClusterScopedCountsRequireClusterScopedRBAC(t *testing.T) {
	env := newAuthTestServer(t)

	perms := &auth.UserPermissions{AllowedNamespaces: nil}
	perms.SetCanI("list", "", "nodes", "", false)
	perms.SetCanI("list", "", "namespaces", "", false)
	env.srv.permCache.Set("broad-reader", perms)

	resp := env.authGet(t, "/api/dashboard", "broad-reader", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body DashboardResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode dashboard: %v", err)
	}
	if body.ResourceCounts.Namespaces != 0 {
		t.Fatalf("namespace count leaked without namespace read RBAC: %d", body.ResourceCounts.Namespaces)
	}
	if body.ResourceCounts.Nodes.Total != 0 {
		t.Fatalf("node count leaked without node read RBAC: %+v", body.ResourceCounts.Nodes)
	}
	if body.NodeVersionSkew != nil {
		t.Fatalf("node version skew leaked without node read RBAC: %+v", body.NodeVersionSkew)
	}
}

func TestProxyAuth_ClusterScopedReadsRequireClusterScopedRBAC(t *testing.T) {
	env := newAuthTestServer(t)

	// Cluster-wide pod visibility (sentinel nil) is NOT a license to read
	// cluster-scoped kinds — those need their own SAR. Pin that distinction
	// for /api/resources/{cluster-only-kind}: a regression that drops the
	// ClassifyKindScope guard would surface here.
	perms := &auth.UserPermissions{AllowedNamespaces: nil}
	perms.SetCanI("list", "", "nodes", "", false)
	perms.SetCanI("list", "rbac.authorization.k8s.io", "clusterroles", "", false)
	env.srv.permCache.Set("broad-reader", perms)

	for _, kind := range []string{"nodes", "clusterroles"} {
		resp := env.authGet(t, "/api/resources/"+kind, "broad-reader", "")
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			t.Fatalf("%s: expected 200, got %d", kind, resp.StatusCode)
		}
		var items []any
		json.NewDecoder(resp.Body).Decode(&items)
		resp.Body.Close()
		if len(items) != 0 {
			t.Errorf("%s leaked without cluster-scoped read RBAC: %d items", kind, len(items))
		}
	}

	// Top nodes is the metrics-table sibling and was missed by the original
	// cached-read sweep — pin its gate too.
	resp := env.authGet(t, "/api/metrics/top/nodes", "broad-reader", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/api/metrics/top/nodes: expected 200, got %d", resp.StatusCode)
	}
	var top []any
	json.NewDecoder(resp.Body).Decode(&top)
	if len(top) != 0 {
		t.Errorf("top/nodes leaked without node RBAC: %d entries", len(top))
	}
}

func TestProxyAuth_NamespacesResource_RequiresListNamespacesSAR(t *testing.T) {
	// /api/resources/namespaces returns full Namespace objects (labels,
	// annotations, spec). Cluster-wide pod RBAC alone (AllowedNamespaces
	// nil sentinel from DiscoverNamespaces' list-pods probe) does NOT
	// license that — pin the strict SAR gate so a regression that lets
	// nil-sentinel users see Namespace metadata surfaces here.
	env := newAuthTestServer(t)

	perms := &auth.UserPermissions{AllowedNamespaces: nil}
	perms.SetCanI("list", "", "namespaces", "", false)
	env.srv.permCache.Set("broad-reader", perms)

	resp := env.authGet(t, "/api/resources/namespaces", "broad-reader", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var items []any
	json.NewDecoder(resp.Body).Decode(&items)
	if len(items) != 0 {
		t.Errorf("namespaces leaked without list-namespaces RBAC: %d items", len(items))
	}
}

func TestProxyAuth_NamespacesResource_GetRequiresGetNamespacesSAR(t *testing.T) {
	// /api/resources/namespaces/_/{name} returns a full Namespace object.
	// Read access to resources IN a namespace is not the same RBAC tuple
	// as get-namespace — pin the strict gate. The 403 keeps a restricted
	// user from learning labels/annotations of namespaces they only have
	// pod-list access to.
	env := newAuthTestServer(t)

	perms := &auth.UserPermissions{AllowedNamespaces: []string{"alpha"}}
	perms.SetCanI("get", "", "namespaces", "", false)
	env.srv.permCache.Set("alice", perms)

	resp := env.authGet(t, "/api/resources/namespaces/_/alpha", "alice", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 without get-namespaces SAR, got %d", resp.StatusCode)
	}
}

func TestProxyAuth_CanI_CacheIsPerUser(t *testing.T) {
	// Pin the load-bearing isolation claim: canI cache results MUST be per
	// user. Alice's cached allow on /nodes must NOT authorize Bob's request.
	// A regression that moved canI to a process-wide map (or keyed by verb
	// alone) would surface here.
	env := newAuthTestServer(t)

	alicePerms := &auth.UserPermissions{AllowedNamespaces: nil}
	alicePerms.SetCanI("list", "", "nodes", "", true)
	env.srv.permCache.Set("alice", alicePerms)

	// Bob has the same namespace ceiling but no cached node allow. He must
	// not inherit alice's grant from the canI cache.
	bobPerms := &auth.UserPermissions{AllowedNamespaces: nil}
	bobPerms.SetCanI("list", "", "nodes", "", false)
	env.srv.permCache.Set("bob", bobPerms)

	// Sanity: alice's grant works (she can see nodes — the SA has them).
	aliceResp := env.authGet(t, "/api/resources/nodes", "alice", "")
	defer aliceResp.Body.Close()
	if aliceResp.StatusCode != http.StatusOK {
		t.Fatalf("alice: expected 200, got %d", aliceResp.StatusCode)
	}

	// Bob hits the same endpoint. canI says deny → empty result, not the
	// list alice saw.
	bobResp := env.authGet(t, "/api/resources/nodes", "bob", "")
	defer bobResp.Body.Close()
	var bobItems []any
	json.NewDecoder(bobResp.Body).Decode(&bobItems)
	if len(bobItems) != 0 {
		t.Errorf("bob's nodes leaked via cross-user canI cache: %d items", len(bobItems))
	}
}

func TestHandleSetActiveNamespace_RejectsDeniedNamespace(t *testing.T) {
	// Picking a namespace the user can't see must 403, not silently store —
	// otherwise a restricted user could probe namespace existence by
	// observing 200 vs 403. Pin the info-leak guard.
	env := newAuthTestServer(t)
	env.srv.permCache.Set("alice", &auth.UserPermissions{
		AllowedNamespaces: []string{"alpha"},
	})

	resp := env.authPost(t, "/api/cluster/namespace", "alice", "", `{"namespaces":["forbidden-ns"]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for denied pick, got %d", resp.StatusCode)
	}

	// And the server must not have stored anything — re-fetch the scope.
	scopeResp := env.authGet(t, "/api/cluster/namespace-scope", "alice", "")
	defer scopeResp.Body.Close()
	var scope NamespaceScopeResponse
	if err := json.NewDecoder(scopeResp.Body).Decode(&scope); err != nil {
		t.Fatalf("decode scope: %v", err)
	}
	if len(scope.Actives) != 0 {
		t.Errorf("denied pick was stored: Actives=%v", scope.Actives)
	}
}

func TestHandleSetActiveNamespace_RejectsLegacyShape(t *testing.T) {
	// Older clients used to POST {"namespace":"x"}. After the rename to
	// {"namespaces":[…]}, the legacy shape must 400, not silently clear the
	// user's saved pick — Go's default JSON decoder ignores unknown fields,
	// so without DisallowUnknownFields the legacy body would leave
	// Namespaces nil and run the "empty = clear" path.
	prev := k8s.SetTestContextName("test-ctx")
	t.Cleanup(func() { k8s.SetTestContextName(prev) })

	env := newAuthTestServer(t)
	env.srv.permCache.Set("alice", &auth.UserPermissions{
		AllowedNamespaces: []string{"alpha"},
	})

	// Pre-seed alice's in-memory pick so we can verify the rejected POST
	// doesn't perturb it. Going through the handler would hit
	// handleGetNamespaceScope's eviction in this fake (which has no real
	// namespaces in the cache), so we set the pick directly.
	aliceReq := requestWithUser("GET", "/api/cluster/namespace", &auth.User{Username: "alice"})
	env.srv.setActiveNamespaceForUser(aliceReq, []string{"alpha"})

	resp := env.authPost(t, "/api/cluster/namespace", "alice", "", `{"namespace":"alpha"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for legacy {namespace} body, got %d", resp.StatusCode)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "unknown field") {
		t.Errorf("expected error to mention unknown field, got: %s", body)
	}

	// The rejected request must not have touched the stored pick. Asserting
	// directly on the in-memory map sidesteps the test fake's namespace
	// eviction path that would shrink the pick on a round-trip GET.
	got := env.srv.getActiveNamespaceForUser(aliceReq)
	if len(got) != 1 || got[0] != "alpha" {
		t.Errorf("rejected legacy POST mutated stored pick: got %v, want [alpha]", got)
	}
}

func TestHandleGetNamespaceScope_NoPick_EmitsEmptySliceNotNull(t *testing.T) {
	// Pin the wire contract: actives and accessibleNamespaces must serialize
	// as [] (non-nil empty), not null. Without the nil-coercion in
	// handleGetNamespaceScope the frontend crashed on `scope.actives.slice()`
	// — caught by /visual-test. The defensive code is small and easy to
	// regress in a refactor, so pin the byte-level wire shape here.
	env := newAuthTestServer(t)
	env.srv.permCache.Set("alice", &auth.UserPermissions{
		AllowedNamespaces: []string{"alpha"},
	})

	resp := env.authGet(t, "/api/cluster/namespace-scope", "alice", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)

	if strings.Contains(string(body), `"actives":null`) {
		t.Errorf("actives marshalled as null (frontend crashes on .slice()): %s", body)
	}
	if !strings.Contains(string(body), `"actives":[`) {
		t.Errorf("expected actives:[…] in body, got: %s", body)
	}
	if strings.Contains(string(body), `"accessibleNamespaces":null`) {
		t.Errorf("accessibleNamespaces marshalled as null: %s", body)
	}
	if !strings.Contains(string(body), `"accessibleNamespaces":[`) {
		t.Errorf("expected accessibleNamespaces:[…] in body, got: %s", body)
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
