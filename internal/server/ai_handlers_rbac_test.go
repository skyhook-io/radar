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

// AI list path RBAC at the /api/ai/resources/{kind} layer.
//
// handleAIListResources shares preflightResourceList with
// handleListResources so the same gates run on both paths:
//   - cluster-scoped SAR for Node / cluster-scoped CRDs
//   - list-namespaces SAR for `kind=namespaces`
//   - per-namespace and/or cluster-wide list-secrets SAR for `kind=secrets`
//
// Where the REST path returns 200 with `[]` for denies (legacy frontend
// shape that doesn't leak kind existence), the AI path returns the
// explicit status so agents see the failure instead of confusing
// "empty cluster" output.

func TestAI_SecretsList_PerNamespaceDenied_Returns403(t *testing.T) {
	// alice has namespace access to default but per-namespace
	// `list secrets` is denied. preflightResourceList must intercept
	// before reaching the cache.
	env := newAuthTestServer(t)
	env.srv.permCache.Set("alice", &auth.UserPermissions{
		AllowedNamespaces: []string{"default"},
	})
	seedServerSecretListCanI(t, env, "alice", nil, []string{"default"})

	resp := env.authGet(t, "/api/ai/resources/secrets?namespace=default", "alice", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for AI secrets list with per-namespace deny, got %d", resp.StatusCode)
	}
}

func TestAI_NodesList_NoClusterRBAC_Returns403(t *testing.T) {
	// Nodes are cluster-scoped. Cluster-wide pod visibility
	// (AllowedNamespaces nil sentinel) is not a license to read
	// cluster-scoped kinds — the SAR-level gate must reject.
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: nil}
	perms.SetCanI("list", "", "nodes", "", false)
	env.srv.permCache.Set("broad-reader", perms)

	resp := env.authGet(t, "/api/ai/resources/nodes", "broad-reader", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for AI nodes list without cluster-scope RBAC, got %d", resp.StatusCode)
	}
}

func TestAI_NamespacesList_NoListNamespacesSAR_Returns403(t *testing.T) {
	// /api/ai/resources/namespaces returns full Namespace objects.
	// Strict SAR gate — cluster-wide pod RBAC alone is not sufficient.
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: nil}
	perms.SetCanI("list", "", "namespaces", "", false)
	env.srv.permCache.Set("broad-reader", perms)

	resp := env.authGet(t, "/api/ai/resources/namespaces", "broad-reader", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for AI namespaces list without list-namespaces SAR, got %d", resp.StatusCode)
	}
}

// TestAI_ListServices_WithGroup_RoutesToDynamicCache pins the group-aware
// short-circuit in handleAIListResources. For kind=services with no group,
// the typed core Service list path returns the seeded nginx Service. For
// kind=services&group=serving.knative.dev, the handler must skip the
// typed cache (which is group-blind — it would silently return core
// Services and drop the group filter on the floor) and route through
// aiListDynamic instead. Mirrors the same fix on GET in PR #721.
//
// The smoke TestMain seeds typed caches only; the dynamic resource cache
// isn't initialized, so the dynamic path surfaces a 500 with "resource
// discovery not initialized". That 500 IS the assertion: pre-fix the
// handler would return 200 with the core Service rows (silent
// wrong-kind result), which is the bug.
func TestAI_ListServices_WithGroup_RoutesToDynamicCache(t *testing.T) {
	env := newAuthTestServer(t)
	env.srv.permCache.Set("bob", &auth.UserPermissions{
		AllowedNamespaces: []string{"default"},
	})

	// Baseline: no group → typed cache returns the seeded core Service.
	respCore := env.authGet(t, "/api/ai/resources/services?namespace=default", "bob", "")
	defer respCore.Body.Close()
	if respCore.StatusCode != http.StatusOK {
		t.Fatalf("baseline (no group): expected 200, got %d", respCore.StatusCode)
	}
	var coreRows []map[string]any
	if err := json.NewDecoder(respCore.Body).Decode(&coreRows); err != nil {
		t.Fatalf("decode core: %v", err)
	}
	var foundNginxSvc bool
	for _, row := range coreRows {
		if row["kind"] == "Service" && row["name"] == "nginx" {
			foundNginxSvc = true
			break
		}
	}
	if !foundNginxSvc {
		t.Fatalf("baseline (no group): expected nginx Service in typed list, got %+v", coreRows)
	}

	// With group: must route through aiListDynamic. Dynamic cache isn't
	// initialized in the smoke harness, so we expect either 400 ("unknown
	// resource kind") or 500 ("dynamic resource cache not initialized" /
	// "resource discovery not initialized") — anything BUT a 200 with
	// core Services, which is the pre-fix wrong-result path.
	respCRD := env.authGet(t, "/api/ai/resources/services?namespace=default&group=serving.knative.dev", "bob", "")
	defer respCRD.Body.Close()
	if respCRD.StatusCode == http.StatusOK {
		var crdRows []map[string]any
		if err := json.NewDecoder(respCRD.Body).Decode(&crdRows); err == nil {
			for _, row := range crdRows {
				if row["name"] == "nginx" {
					t.Fatalf("group=serving.knative.dev leaked typed core Service into result (pre-fix bug): row=%+v", row)
				}
			}
		}
	}
	if respCRD.StatusCode != http.StatusBadRequest && respCRD.StatusCode != http.StatusInternalServerError && respCRD.StatusCode != http.StatusOK {
		t.Fatalf("group=serving.knative.dev: unexpected status %d (want 400/500 from uninitialized dynamic cache, or 200 with non-core rows)", respCRD.StatusCode)
	}
}

func TestAI_DeploymentsList_HappyPath_AttachesSummaryContext(t *testing.T) {
	// Allowed user, summary-verbosity default. The envelope must
	// include the seeded nginx deployment AND each row must carry a
	// summaryContext field (the load-bearing new wire shape this PR
	// adds — pin it so a refactor that skipped attachment surfaces
	// here).
	env := newAuthTestServer(t)
	env.srv.permCache.Set("bob", &auth.UserPermissions{
		AllowedNamespaces: []string{"default"},
	})

	resp := env.authGet(t, "/api/ai/resources/deployments", "bob", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var rows []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(rows) == 0 {
		t.Fatalf("allowed user got 0 deployments, expected seeded nginx")
	}

	// AI list rows are flat (kind/name/namespace at the top level —
	// the minified shape, distinct from the REST handler's K8s-native
	// metadata-nested objects). Find the nginx row and assert
	// summaryContext is present. Empty map is acceptable (the
	// deployment is healthy and not managed by an external
	// controller) — what matters is the envelope field exists so
	// consumers don't have to special-case its absence.
	var found bool
	for _, row := range rows {
		if row["name"] != "nginx" {
			continue
		}
		found = true
		if _, has := row["summaryContext"]; !has {
			t.Errorf("nginx row missing summaryContext envelope: %+v", row)
		}
	}
	if !found {
		t.Errorf("nginx deployment not in AI list response: %+v", rows)
	}
}
