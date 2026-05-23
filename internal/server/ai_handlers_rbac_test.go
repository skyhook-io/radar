package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
)

// RBAC preflight on /api/ai/resources/{kind}/{namespace}/{name}.
//
// The AI single-resource GET returns the same resource bytes (just minified
// + wrapped in a resourceContext block) as /api/resources/{kind}/{ns}/{name}.
// It must therefore enforce the same per-user RBAC gates that
// handleGetResource enforces — otherwise a user could read Secret values via
// the AI surface even when the REST surface correctly returns 403.
//
// Both handlers call s.preflightResourceGet, so these tests pin the AI
// endpoint's gates (and a regression that bypasses the helper on the AI side
// would surface here even if the REST tests still pass).

func TestProxyAuth_AIGetSecret_PerNamespaceRBAC_Denied(t *testing.T) {
	// alice has namespace access to "default" but the per-namespace
	// canRead("","secrets","default","get") returns false. The cache holds
	// nginx-tls (seeded as the SA which has cluster-wide secrets RBAC),
	// so without the preflight a 200 would leak secret bytes.
	env := newAuthTestServer(t)
	env.srv.permCache.Set("alice", &auth.UserPermissions{
		AllowedNamespaces: []string{"default"},
	})
	seedServerSecretGetCanI(t, env, "alice", nil, []string{"default"})

	resp := env.authGet(t, "/api/ai/resources/secret/default/nginx-tls", "alice", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for AI get-secret without per-ns get SAR, got %d", resp.StatusCode)
	}
}

func TestProxyAuth_AIGetNode_ClusterScopedRBAC_Denied(t *testing.T) {
	// Node is cluster-scoped — the AI GET must require per-kind get-node SAR.
	// AllowedNamespaces==nil (cluster-wide-namespace sentinel) is NOT a
	// license to read cluster-scoped kinds: that's the exact conflation the
	// preflight helper guards against. A regression that dropped the
	// ClassifyKindScope arm would let nodes through here.
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: nil}
	perms.SetCanI("get", "", "nodes", "", false)
	env.srv.permCache.Set("broad-reader", perms)

	resp := env.authGet(t, "/api/ai/resources/node/_/worker-1", "broad-reader", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for AI get-node without cluster-scoped get-node SAR, got %d", resp.StatusCode)
	}
}

func TestProxyAuth_AIGetPod_NamespaceDenied(t *testing.T) {
	// alice has namespace access only to "default" — a get against a pod
	// in "kube-system" must 403 BEFORE any fetch, matching handleGetResource.
	// A regression that fetched first and then filtered would let timing
	// signal whether the pod exists (oracle).
	env := newAuthTestServer(t)
	env.srv.permCache.Set("alice", &auth.UserPermissions{
		AllowedNamespaces: []string{"default"},
	})

	resp := env.authGet(t, "/api/ai/resources/pods/kube-system/some-pod", "alice", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for AI get-pod in disallowed namespace, got %d", resp.StatusCode)
	}
}

func TestProxyAuth_AIGetPod_NamespaceAllowed(t *testing.T) {
	// Sanity check: a user with namespace access AND who hits an existing
	// resource gets a 200 with the {resource, resourceContext} envelope.
	// Pins that the preflight isn't accidentally over-gating happy-path
	// requests (e.g., a misordered check that always denies).
	env := newAuthTestServer(t)
	env.srv.permCache.Set("bob", &auth.UserPermissions{
		AllowedNamespaces: []string{"default"},
	})

	resp := env.authGet(t, "/api/ai/resources/pods/default/nginx-abc-xyz", "bob", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on allowed AI get-pod, got %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if _, ok := body["resource"]; !ok {
		t.Errorf("expected 'resource' field in AI get response, got: %+v", body)
	}
	if _, ok := body["resourceContext"]; !ok {
		t.Errorf("expected 'resourceContext' field in AI get response, got: %+v", body)
	}
}
