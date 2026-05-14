package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
)

// Per-namespace Secret RBAC at the REST layer.
//
// The chart can grant the Radar ServiceAccount cluster-wide list/get/watch
// on secrets (any of rbac.secrets / rbac.helm / auth.mode != "none" /
// cloud.enabled triggers it — Helm release metadata IS K8s Secrets, and the
// K8s `view` role excludes them, so Helm visibility requires the elevated
// grant). Whenever the cache holds secrets the user can't read directly,
// handleListResources / handleGetResource must enforce per-user Secret
// RBAC server-side instead of relying on the chart's coarse grant.

// seedServerSecretListCanI seeds the per-namespace canI cache for `list` on
// secrets only. Each list-handler test should call this (not the both-verbs
// variant) so a regression where the handler checks the wrong verb surfaces
// as a failed assertion.
func seedServerSecretListCanI(t *testing.T, env *authTestEnv, username string, allowedNamespaces []string, deniedNamespaces []string) {
	t.Helper()
	seedServerSecretCanIVerb(t, env, username, "list", allowedNamespaces, deniedNamespaces)
}

// seedServerSecretGetCanI is the `get`-verb counterpart. See seedServerSecretListCanI.
func seedServerSecretGetCanI(t *testing.T, env *authTestEnv, username string, allowedNamespaces []string, deniedNamespaces []string) {
	t.Helper()
	seedServerSecretCanIVerb(t, env, username, "get", allowedNamespaces, deniedNamespaces)
}

func seedServerSecretCanIVerb(t *testing.T, env *authTestEnv, username, verb string, allowedNamespaces []string, deniedNamespaces []string) {
	t.Helper()
	perms := env.srv.permCache.Get(username)
	if perms == nil {
		t.Fatalf("user %q not in perm cache; call permCache.Set first", username)
	}
	for _, ns := range allowedNamespaces {
		perms.SetCanI(verb, "", "secrets", ns, true)
	}
	for _, ns := range deniedNamespaces {
		perms.SetCanI(verb, "", "secrets", ns, false)
	}
}

func TestProxyAuth_SecretsList_NamespaceRestricted_Denied(t *testing.T) {
	// alice has namespace access to default (where nginx-tls lives) but the
	// per-namespace canRead("","secrets","default","list") returns false.
	// The cache reads as the SA and may carry secrets her ClusterRole
	// excludes — the per-namespace SAR gate must intercept.
	env := newAuthTestServer(t)
	env.srv.permCache.Set("alice", &auth.UserPermissions{
		AllowedNamespaces: []string{"default"},
	})
	seedServerSecretListCanI(t, env, "alice", nil, []string{"default"})

	resp := env.authGet(t, "/api/resources/secrets", "alice", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var items []any
	json.NewDecoder(resp.Body).Decode(&items)
	if len(items) != 0 {
		t.Errorf("secret leaked through namespace-only gate: %d items", len(items))
	}
}

func TestProxyAuth_SecretsList_NamespaceRestricted_Allowed(t *testing.T) {
	// bob has both namespace access AND per-namespace secret RBAC for
	// default. The gate must pass through.
	env := newAuthTestServer(t)
	env.srv.permCache.Set("bob", &auth.UserPermissions{
		AllowedNamespaces: []string{"default"},
	})
	seedServerSecretListCanI(t, env, "bob", []string{"default"}, nil)

	resp := env.authGet(t, "/api/resources/secrets", "bob", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var items []any
	json.NewDecoder(resp.Body).Decode(&items)
	if len(items) == 0 {
		t.Errorf("authorized user got 0 secrets, expected the seeded nginx-tls")
	}
}

func TestProxyAuth_SecretsList_ClusterWideShape_NoSecretRBAC(t *testing.T) {
	// AllowedNamespaces==nil is the cluster-wide-namespace sentinel
	// (DiscoverNamespaces stage 1: cluster-wide list pods). It does NOT
	// imply cluster-wide list-secrets — a user can have cluster-wide pod
	// visibility and still lack secrets RBAC. Pin the cluster-scope SAR
	// gate so this conflation doesn't return.
	env := newAuthTestServer(t)
	env.srv.permCache.Set("broad-reader", &auth.UserPermissions{
		AllowedNamespaces: nil,
	})
	perms := env.srv.permCache.Get("broad-reader")
	perms.SetCanI("list", "", "secrets", "", false)

	resp := env.authGet(t, "/api/resources/secrets", "broad-reader", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var items []any
	json.NewDecoder(resp.Body).Decode(&items)
	if len(items) != 0 {
		t.Errorf("secrets leaked to cluster-wide-pods user without cluster-scope secrets SAR: %d items", len(items))
	}
}

func TestProxyAuth_SecretsList_ClusterWideShape_WithSecretRBAC(t *testing.T) {
	// Same cluster-wide-namespace shape, but with explicit cluster-scope
	// `list secrets` RBAC seeded. Cache returns every secret.
	env := newAuthTestServer(t)
	env.srv.permCache.Set("admin", &auth.UserPermissions{
		AllowedNamespaces: nil,
	})
	perms := env.srv.permCache.Get("admin")
	perms.SetCanI("list", "", "secrets", "", true)

	resp := env.authGet(t, "/api/resources/secrets", "admin", "system:masters")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var items []any
	json.NewDecoder(resp.Body).Decode(&items)
	if len(items) == 0 {
		t.Errorf("cluster-wide-namespace user with cluster-scope secrets SAR should see all secrets")
	}
}

func TestProxyAuth_SecretsGet_Denied(t *testing.T) {
	// 403 (not 404) when the namespace is allowed but per-kind RBAC denies.
	// 404 would let the user probe secret existence by observing 200 vs 404;
	// 403 is the explicit deny.
	env := newAuthTestServer(t)
	env.srv.permCache.Set("alice", &auth.UserPermissions{
		AllowedNamespaces: []string{"default"},
	})
	seedServerSecretGetCanI(t, env, "alice", nil, []string{"default"})

	resp := env.authGet(t, "/api/resources/secrets/default/nginx-tls", "alice", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

func TestProxyAuth_SecretsGet_Allowed(t *testing.T) {
	env := newAuthTestServer(t)
	env.srv.permCache.Set("bob", &auth.UserPermissions{
		AllowedNamespaces: []string{"default"},
	})
	seedServerSecretGetCanI(t, env, "bob", []string{"default"}, nil)

	resp := env.authGet(t, "/api/resources/secrets/default/nginx-tls", "bob", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

// TestProxyAuth_SearchSecrets_PerNamespaceFanout pins the search-side gate.
// /api/search must do per-namespace fanout for Secrets so a user with
// per-namespace secret RBAC sees those rows in search, matching the resource
// browser. A cluster-scope SAR alone would silently drop Secret for any
// namespace-bounded user.
func TestProxyAuth_SearchSecrets_PerNamespaceFanout(t *testing.T) {
	env := newAuthTestServer(t)
	env.srv.permCache.Set("alice", &auth.UserPermissions{
		AllowedNamespaces: []string{"default"},
	})
	seedServerSecretListCanI(t, env, "alice", []string{"default"}, nil)

	resp := env.authGet(t, "/api/search?q=kind:Secret", "alice", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body struct {
		Hits []struct {
			Name string `json:"name"`
		} `json:"hits"`
	}
	json.NewDecoder(resp.Body).Decode(&body)

	found := false
	for _, h := range body.Hits {
		if h.Name == "nginx-tls" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected nginx-tls in search hits for per-namespace-allowed user, got %d hits: %+v", len(body.Hits), body.Hits)
	}
}

func TestProxyAuth_SearchSecrets_NamespaceDenied(t *testing.T) {
	// Same alice setup but per-namespace secret denied — search should drop
	// Secret entirely (no hits) instead of leaking through.
	env := newAuthTestServer(t)
	env.srv.permCache.Set("alice", &auth.UserPermissions{
		AllowedNamespaces: []string{"default"},
	})
	seedServerSecretListCanI(t, env, "alice", nil, []string{"default"})

	resp := env.authGet(t, "/api/search?q=kind:Secret", "alice", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body struct {
		Hits []struct {
			Name string `json:"name"`
		} `json:"hits"`
	}
	json.NewDecoder(resp.Body).Decode(&body)

	for _, h := range body.Hits {
		if h.Name == "nginx-tls" {
			t.Errorf("secret leaked in search despite per-ns deny: %+v", body.Hits)
		}
	}
}

// TestSmokeSecretsList_NoAuthPassthrough verifies that auth.mode=none lets
// callers see every cached Secret. The Secret gate in handleListResources
// short-circuits on a nil user context (auth.UserFromContext); a regression
// that inverted that check would silently break Radar's primary local mode.
// Uses the auth-disabled global testServer (Config{DevMode: true}, no auth
// config) — distinct from the auth-enabled tests above.
func TestSmokeSecretsList_NoAuthPassthrough(t *testing.T) {
	resp := get(t, "/api/resources/secrets")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var items []struct {
		Metadata struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
	}
	json.NewDecoder(resp.Body).Decode(&items)

	want := map[string]string{"nginx-tls": "default", "system-token": "kube-system"}
	got := make(map[string]string, len(items))
	for _, it := range items {
		got[it.Metadata.Name] = it.Metadata.Namespace
	}
	for name, ns := range want {
		if got[name] != ns {
			t.Errorf("no-auth passthrough missing seeded secret %s/%s; got: %+v", ns, name, got)
		}
	}
}

// TestProxyAuth_SecretsList_PartialNamespaceAccess verifies the
// filterNamespacesByCanRead helper preserves the allowed subset when a user
// has namespace access to multiple namespaces but per-namespace secret RBAC
// in only some of them. The REST path uses s.filterNamespacesByCanRead +
// s.canRead (distinct from MCP's filterNamespacesByCanRead path), so the
// equivalent MCP test alone doesn't pin this code.
func TestProxyAuth_SecretsList_PartialNamespaceAccess(t *testing.T) {
	env := newAuthTestServer(t)
	env.srv.permCache.Set("alice", &auth.UserPermissions{
		AllowedNamespaces: []string{"default", "kube-system"},
	})
	seedServerSecretListCanI(t, env, "alice", []string{"default"}, []string{"kube-system"})

	resp := env.authGet(t, "/api/resources/secrets?namespaces=default,kube-system", "alice", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var items []struct {
		Metadata struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"metadata"`
	}
	json.NewDecoder(resp.Body).Decode(&items)

	var foundNginx, foundSystem bool
	for _, it := range items {
		if it.Metadata.Name == "nginx-tls" && it.Metadata.Namespace == "default" {
			foundNginx = true
		}
		if it.Metadata.Name == "system-token" && it.Metadata.Namespace == "kube-system" {
			foundSystem = true
		}
	}
	if !foundNginx {
		t.Errorf("expected nginx-tls (default, allowed) in result, got: %+v", items)
	}
	if foundSystem {
		t.Errorf("system-token (kube-system, denied) leaked despite per-namespace SAR deny: %+v", items)
	}
}
