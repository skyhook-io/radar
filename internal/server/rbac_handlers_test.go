package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/rbac"
)

// Tests for /api/rbac/subject/..., /api/rbac/role/..., /api/rbac/whoami.
// These use the global testServer set up in server_smoke_test.go's TestMain,
// which now seeds an RBAC fixture (default/app-sa, default/app-reader Role
// bound by default/app-binding, plus a system:authenticated grant to a
// rbac-test-view ClusterRole). No proxy auth — these tests cover the
// no-auth path; auth-gated tests live in TestRBAC_Subject_Forbidden_When*.

func TestRBAC_Subject_ServiceAccount_DirectAndInherited(t *testing.T) {
	resp, err := http.Get(testServer.URL + "/api/rbac/subject/ServiceAccount/default/app-sa")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var got SubjectResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if got.Subject.Name != "app-sa" || got.Subject.Namespace != "default" {
		t.Errorf("wrong subject echoed back: %+v", got.Subject)
	}
	if len(got.Direct) != 1 {
		t.Fatalf("expected 1 direct binding, got %d (%+v)", len(got.Direct), got.Direct)
	}
	if got.Direct[0].Binding.Name != "app-binding" {
		t.Errorf("expected app-binding, got %s", got.Direct[0].Binding.Name)
	}
	if got.Direct[0].Role.Name != "app-reader" {
		t.Errorf("expected role=app-reader, got %s", got.Direct[0].Role.Name)
	}
	// Inherited from system:authenticated should be populated by the
	// rbac-test-auth-view ClusterRoleBinding.
	if len(got.InheritedFromGroups) == 0 {
		t.Fatal("expected at least one inherited group (system:authenticated)")
	}
	foundAuthGroup := false
	for _, g := range got.InheritedFromGroups {
		if g.GroupName == "system:authenticated" {
			foundAuthGroup = true
			if len(g.Bindings) == 0 {
				t.Error("system:authenticated group should have at least one binding")
			}
		}
	}
	if !foundAuthGroup {
		t.Errorf("expected system:authenticated in inherited groups, got %+v", got.InheritedFromGroups)
	}
	if len(got.Flat) == 0 {
		t.Error("expected non-empty flat rule set")
	}
}

func TestRBAC_Subject_ServiceAccount_NoBindings(t *testing.T) {
	// A SA that doesn't exist (or has no bindings) should return an empty
	// direct list — NOT a 404. The endpoint is "what's bound to this
	// identity", and "nothing" is a valid answer that the UI uses for the
	// empty-state message.
	resp, err := http.Get(testServer.URL + "/api/rbac/subject/ServiceAccount/default/never-bound")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var got SubjectResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Direct) != 0 {
		t.Errorf("expected 0 direct bindings, got %d", len(got.Direct))
	}
	// Inherited may still be non-empty — every SA inherits from
	// system:authenticated, which the test fixture grants rbac-test-view.
	// We don't assert empty here; just that the structure is well-formed.
}

func TestRBAC_Subject_RejectsBadKind(t *testing.T) {
	resp, err := http.Get(testServer.URL + "/api/rbac/subject/BogusKind/x")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for unknown kind, got %d", resp.StatusCode)
	}
}

func TestRBAC_Role_Reverse_Lookup(t *testing.T) {
	resp, err := http.Get(testServer.URL + "/api/rbac/role/Role/default/app-reader")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var got RoleResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Role.Name != "app-reader" || got.Role.Namespace != "default" {
		t.Errorf("wrong role echoed: %+v", got.Role)
	}
	if len(got.Bindings) != 1 {
		t.Fatalf("expected 1 binding referencing app-reader, got %d", len(got.Bindings))
	}
	if got.Bindings[0].Binding.Name != "app-binding" {
		t.Errorf("expected app-binding, got %s", got.Bindings[0].Binding.Name)
	}
	// Subjects must be inlined — the whole point of the reverse-lookup is
	// to answer "who is this granted to" without a second fetch.
	if len(got.Bindings[0].Subjects) != 1 {
		t.Fatalf("expected 1 subject, got %d", len(got.Bindings[0].Subjects))
	}
	wantSubj := rbac.Subject{Kind: "ServiceAccount", Namespace: "default", Name: "app-sa"}
	if got.Bindings[0].Subjects[0] != wantSubj {
		t.Errorf("wrong subject: got %+v, want %+v", got.Bindings[0].Subjects[0], wantSubj)
	}
}

func TestRBAC_Role_ClusterRole_UsesUnderscoreNamespace(t *testing.T) {
	resp, err := http.Get(testServer.URL + "/api/rbac/role/ClusterRole/_/rbac-test-view")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for ClusterRole reverse-lookup, got %d", resp.StatusCode)
	}

	var got RoleResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Role.Kind != "ClusterRole" || got.Role.Namespace != "" {
		t.Errorf("expected ClusterRole with empty namespace, got %+v", got.Role)
	}
	if len(got.Bindings) != 1 {
		t.Fatalf("expected 1 binding, got %d", len(got.Bindings))
	}
}

func TestRBAC_Role_RejectsNamespacedClusterRole(t *testing.T) {
	// Passing an actual namespace for a ClusterRole is a client bug — the
	// endpoint must reject it, not silently look up a nonexistent role.
	resp, err := http.Get(testServer.URL + "/api/rbac/role/ClusterRole/default/rbac-test-view")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for namespaced ClusterRole request, got %d", resp.StatusCode)
	}
}

func TestRBAC_Whoami_RouteRegistered(t *testing.T) {
	// The test smoke server initializes the resource cache but not the
	// raw k8s client (k8sClient global). The whoami handler needs a real
	// (or fake) client to call SelfSubjectRulesReview, so we get a 503
	// here. We still want to verify the route is wired — a 404 would
	// mean the registration broke. Visual test against a live cluster
	// covers the happy path end-to-end.
	resp, err := http.Get(testServer.URL + "/api/rbac/whoami?namespace=default")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		t.Error("whoami route returned 404 — registration broken")
	}
	// 503 (client unavailable) or 200 (client wired) both indicate the
	// route exists. 4xx other than 404 would be a handler bug.
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("unexpected status %d (want 200 or 503)", resp.StatusCode)
	}
}

// TestRBAC_Subject_Forbidden_WhenCannotListRoleBindings verifies the
// requireRBACReadable gate fires 403 — silent partial responses would
// mislead operators about what they have access to. This is the only
// thing standing between a denied user and a partial reverse-lookup leak;
// without a test pinning it, a refactor to s.canRead could silently
// degrade to "always returns true" and the endpoint would start leaking
// cached binding data to users who shouldn't see it.
func TestRBAC_Subject_Forbidden_WhenCannotListRoleBindings(t *testing.T) {
	assertForbiddenWhenDeniedRBACList(t, "/api/rbac/subject/ServiceAccount/default/app-sa", "rolebindings")
}

func TestRBAC_Subject_Forbidden_WhenCannotListClusterRoleBindings(t *testing.T) {
	assertForbiddenWhenDeniedRBACList(t, "/api/rbac/subject/ServiceAccount/default/app-sa", "clusterrolebindings")
}

func TestRBAC_Role_Forbidden_WhenCannotListBindings(t *testing.T) {
	assertForbiddenWhenDeniedRBACList(t, "/api/rbac/role/Role/default/app-reader", "rolebindings")
}

func TestRBAC_Namespace_Forbidden_WhenCannotListBindings(t *testing.T) {
	assertForbiddenWhenDeniedRBACList(t, "/api/rbac/namespace/default", "rolebindings")
}

// assertForbiddenWhenDeniedRBACList seeds the test server's permission
// cache with a deny entry for the given RBAC resource ("rolebindings" or
// "clusterrolebindings"), then asserts the path returns 403. The seeded
// user is hardcoded — every test in this group uses the same one so
// canRead's permCache hit resolves the deny without ever calling SAR.
func assertForbiddenWhenDeniedRBACList(t *testing.T, path, deniedResource string) {
	t.Helper()
	if testServerSrv == nil {
		t.Skip("test server not initialized")
	}
	// The test server runs in DevMode (no auth), so permCache is nil — but
	// canRead short-circuits to allow only when permCache OR user is nil.
	// Wire up a fresh permCache for these tests; the seam still exercises
	// the production gate (canRead consults permCache when both are set).
	if testServerSrv.permCache == nil {
		testServerSrv.permCache = pkgauth.NewPermissionCache()
	}

	const username = "denied-user"

	// Seed a UserPermissions with the deny we care about, and an allow for
	// the other RBAC resource (so we know it's the specific deny that fires
	// the 403, not the absence of any cache entry).
	perms := &pkgauth.UserPermissions{}
	for _, resource := range []string{"rolebindings", "clusterrolebindings"} {
		allowed := resource != deniedResource
		perms.SetCanI("list", "rbac.authorization.k8s.io", resource, "", allowed)
	}
	testServerSrv.permCache.Set(username, perms)
	// Entry is keyed by the unique "denied-user" name and TTL'd by the
	// cache; no cleanup needed — other tests don't use this username.

	req, err := http.NewRequest(http.MethodGet, testServer.URL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	// Attach the user via context, then issue through a transport that
	// preserves the context (default client does).
	ctx := pkgauth.ContextWithUser(req.Context(), &pkgauth.User{Username: username})
	req = req.WithContext(ctx)

	// httptest.NewServer drops the inbound request context (it starts fresh
	// per connection), so we route through the in-process handler instead.
	rec := httptest.NewRecorder()
	testServerSrv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for %s when %s denied, got %d (body: %s)", path, deniedResource, rec.Code, rec.Body.String())
	}
}
