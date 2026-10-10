package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

func TestParseNamespacesForUser_GlobalNsIgnoresPick(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := newTestServer(t)
	s.setActiveNamespaceForUser(reqAs(""), []string{"default"})

	if got := s.parseNamespacesForUser(httptest.NewRequest("GET", "/api/resources/pods", nil)); !slices.Equal(got, []string{"default"}) {
		t.Fatalf("without globalNs = %v, want the pick [default]", got)
	}
	if got := s.parseNamespacesForUser(httptest.NewRequest("GET", "/api/resources/pods?globalNs=1", nil)); got != nil {
		t.Fatalf("with globalNs=1 = %v, want nil (all namespaces)", got)
	}
	if got := s.parseNamespacesForUser(httptest.NewRequest("GET", "/api/resources/pods?globalNs=1&namespaces=broken", nil)); !slices.Equal(got, []string{"broken"}) {
		t.Fatalf("explicit filter with globalNs=1 = %v, want [broken]", got)
	}
	if got := s.parseNamespacesForUser(httptest.NewRequest("GET", "/api/resources/pods?globalNs=0", nil)); !slices.Equal(got, []string{"default"}) {
		t.Fatalf("globalNs=0 = %v, want the pick [default]", got)
	}
	if picks := s.getActiveNamespaceForUser(reqAs("")); !slices.Equal(picks, []string{"default"}) {
		t.Errorf("a globalNs read disturbed the saved pick: %v", picks)
	}
}

func TestParseNamespacesForUser_GlobalNsStillIntersectsRBAC(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := newTestServer(t)
	s.permCache = pkgauth.NewPermissionCache()
	s.permCache.Set("alice", nil, &pkgauth.UserPermissions{AllowedNamespaces: []string{"default", "broken"}})
	s.permCache.Set("carol", nil, &pkgauth.UserPermissions{AllowedNamespaces: []string{"broken"}})
	s.setActiveNamespaceForUser(reqAs("alice"), []string{"broken"})

	asUser := func(user, target string) *http.Request {
		r := httptest.NewRequest("GET", target, nil)
		return r.WithContext(pkgauth.ContextWithUser(r.Context(), &pkgauth.User{Username: user}))
	}

	if got := s.parseNamespacesForUser(asUser("alice", "/api/resources/pods")); !slices.Equal(got, []string{"broken"}) {
		t.Fatalf("alice without globalNs = %v, want the pick [broken]", got)
	}
	if got := s.parseNamespacesForUser(asUser("alice", "/api/resources/pods?globalNs=1")); !slices.Equal(got, []string{"default", "broken"}) {
		t.Fatalf("alice with globalNs=1 = %v, want her RBAC ceiling [default broken]", got)
	}
	if got := s.parseNamespacesForUser(asUser("carol", "/api/resources/pods?globalNs=1")); !slices.Equal(got, []string{"broken"}) {
		t.Fatalf("carol with globalNs=1 = %v, want her RBAC ceiling [broken]", got)
	}
	if got := s.parseNamespacesForUser(asUser("carol", "/api/resources/pods?globalNs=1&namespace=default")); !noNamespaceAccess(got) {
		t.Fatalf("carol naming a denied namespace with globalNs=1 = %v, want no access", got)
	}
}

func TestParseNamespacesForUser_GlobalNsKeepsForcedCacheScope(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := newTestServer(t)
	k8s.ForceNamespaceScope = true
	k8s.SetFallbackNamespace("prod")
	t.Cleanup(func() {
		k8s.ForceNamespaceScope = false
		k8s.SetFallbackNamespace("")
	})

	if got := s.parseNamespacesForUser(httptest.NewRequest("GET", "/api/resources/pods?globalNs=1", nil)); !slices.Equal(got, []string{"prod"}) {
		t.Fatalf("globalNs=1 under --namespace-scope = %v, want the pinned [prod]", got)
	}
	if got := s.parseNamespacesForUser(httptest.NewRequest("GET", "/api/resources/pods?globalNs=1&namespace=staging", nil)); !noNamespaceAccess(got) {
		t.Fatalf("globalNs=1 naming a namespace outside the pin = %v, want no access", got)
	}
}

// The same opt-out through the HTTP surface: a pick made in the UI narrows
// plain list and search reads, and globalNs=1 lifts it for both while the
// caller's RBAC still bounds the result.
func TestProxyAuth_GlobalNsReadsIgnoreSavedPick(t *testing.T) {
	prevCtx := k8s.SetTestContextName("test-ctx")
	t.Cleanup(func() { k8s.SetTestContextName(prevCtx) })
	env := newAuthTestServer(t)
	env.srv.permCache.Set("alice", nil, &pkgauth.UserPermissions{AllowedNamespaces: []string{"default", "broken"}})
	env.srv.permCache.Set("carol", nil, &pkgauth.UserPermissions{AllowedNamespaces: []string{"broken"}})
	env.srv.setActiveNamespaceForUser(reqAs("alice"), []string{"broken"})
	env.srv.setActiveNamespaceForUser(reqAs("carol"), []string{"broken"})

	deployments := func(user, path string) []string {
		t.Helper()
		resp := env.authGet(t, path, user, "")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s as %s: status %d", path, user, resp.StatusCode)
		}
		var items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		var names []string
		for _, it := range items {
			names = append(names, it.Metadata.Name)
		}
		slices.Sort(names)
		return names
	}
	searchHits := func(user, path string) []string {
		t.Helper()
		resp := env.authGet(t, path, user, "")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s as %s: status %d", path, user, resp.StatusCode)
		}
		var body struct {
			Hits []struct {
				Name string `json:"name"`
			} `json:"hits"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		var names []string
		for _, h := range body.Hits {
			names = append(names, h.Name)
		}
		slices.Sort(names)
		return names
	}

	if got := deployments("alice", "/api/resources/deployments"); !slices.Equal(got, []string{"stuck-app"}) {
		t.Errorf("alice list without globalNs = %v, want the picked namespace's [stuck-app]", got)
	}
	if got := deployments("alice", "/api/resources/deployments?globalNs=1"); !slices.Equal(got, []string{"nginx", "stuck-app"}) {
		t.Errorf("alice list with globalNs=1 = %v, want [nginx stuck-app]", got)
	}
	if got := deployments("alice", "/api/resources/deployments?globalNs=1&namespaces=default"); !slices.Equal(got, []string{"nginx"}) {
		t.Errorf("alice list with globalNs=1 and an explicit filter = %v, want [nginx]", got)
	}
	if got := deployments("carol", "/api/resources/deployments?globalNs=1"); !slices.Equal(got, []string{"stuck-app"}) {
		t.Errorf("carol list with globalNs=1 = %v, want her RBAC-bounded [stuck-app]", got)
	}

	if got := searchHits("alice", "/api/search?q=kind:Deployment"); !slices.Equal(got, []string{"stuck-app"}) {
		t.Errorf("alice search without globalNs = %v, want [stuck-app]", got)
	}
	if got := searchHits("alice", "/api/search?q=kind:Deployment&globalNs=1"); !slices.Equal(got, []string{"nginx", "stuck-app"}) {
		t.Errorf("alice search with globalNs=1 = %v, want [nginx stuck-app]", got)
	}
	if got := searchHits("carol", "/api/search?q=kind:Deployment&globalNs=1"); !slices.Equal(got, []string{"stuck-app"}) {
		t.Errorf("carol search with globalNs=1 = %v, want her RBAC-bounded [stuck-app]", got)
	}
}
