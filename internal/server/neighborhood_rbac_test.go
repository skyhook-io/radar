package server

import (
	"net/http"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/pkg/topology"
)

// canReadNeighborhoodNode must apply per-kind Secret RBAC inside an allowed
// namespace. Mirrors the gate that handleGetResource already applies: namespace
// access alone is NOT sufficient — the SA backing the cache may have cluster-
// wide secrets RBAC (Helm release visibility) the calling user does not. If the
// per-kind SAR is missing, Secret nodes would leak through the neighborhood BFS
// to users who can't read them directly.

func makeSecretNode(ns, name string) *topology.Node {
	return &topology.Node{
		ID:     "secret/" + ns + "/" + name,
		Kind:   topology.KindSecret,
		Name:   name,
		Status: topology.StatusHealthy,
		Data: map[string]any{
			"namespace":  ns,
			"apiVersion": "v1",
		},
	}
}

func makeConfigMapNode(ns, name string) *topology.Node {
	return &topology.Node{
		ID:     "configmap/" + ns + "/" + name,
		Kind:   topology.KindConfigMap,
		Name:   name,
		Status: topology.StatusHealthy,
		Data: map[string]any{
			"namespace":  ns,
			"apiVersion": "v1",
		},
	}
}

// TestCanReadNeighborhoodNode_SecretRequiresPerKindRBAC pins the new gate:
// a user with namespace access but no per-namespace `get secrets` SAR must be
// denied. Same setup as the existing handleGetResource secrets RBAC tests.
func TestCanReadNeighborhoodNode_SecretRequiresPerKindRBAC(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	s.permCache.Set("alice", &auth.UserPermissions{
		AllowedNamespaces: []string{"default"},
	})
	// alice has namespace access to default but NOT per-namespace secrets-get.
	perms := s.permCache.Get("alice")
	perms.SetCanI("get", "", "secrets", "default", false)

	r := requestWithUser("GET", "/api/ai/neighborhood/pod/default/anything", &auth.User{
		Username: "alice",
	})
	secret := makeSecretNode("default", "nginx-tls")

	if s.canReadNeighborhoodNode(r, secret) {
		t.Error("Secret node leaked through neighborhood gate: namespace access without per-kind RBAC must deny")
	}
}

// Counterpart: a user WITH namespace access AND per-namespace secrets-get
// must be allowed. Locks down the positive path so the gate isn't blanket-deny.
func TestCanReadNeighborhoodNode_SecretAllowedWithPerKindRBAC(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	s.permCache.Set("bob", &auth.UserPermissions{
		AllowedNamespaces: []string{"default"},
	})
	perms := s.permCache.Get("bob")
	perms.SetCanI("get", "", "secrets", "default", true)

	r := requestWithUser("GET", "/api/ai/neighborhood/pod/default/anything", &auth.User{
		Username: "bob",
	})
	secret := makeSecretNode("default", "nginx-tls")

	if !s.canReadNeighborhoodNode(r, secret) {
		t.Error("authorized user denied: namespace access + per-kind RBAC should pass")
	}
}

// Sanity: non-Secret namespaced kinds (e.g. ConfigMap) ride on the namespace
// gate alone — adding the Secret-specific tightening must not regress that.
func TestCanReadNeighborhoodNode_ConfigMapStaysOnNamespaceGate(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	s.permCache.Set("alice", &auth.UserPermissions{
		AllowedNamespaces: []string{"default"},
	})
	// alice has no configmap-specific SAR seeded — namespace access alone
	// should pass for ConfigMap because we deliberately do NOT tighten that.
	r := requestWithUser("GET", "/api/ai/neighborhood/pod/default/anything", &auth.User{
		Username: "alice",
	})
	cm := makeConfigMapNode("default", "nginx-conf")

	if !s.canReadNeighborhoodNode(r, cm) {
		t.Error("ConfigMap node denied: namespace access should be sufficient (no per-kind tightening for ConfigMap)")
	}
}

// makeNodeClassNode builds a topology pseudo-kind NodeClass node. The Kind is
// the synthesized topology label ("NodeClass"), not a real K8s resource — the
// actual variants are EC2NodeClass / AKSNodeClass / GCPNodeClass.
func makeNodeClassNode(name string) *topology.Node {
	return &topology.Node{
		ID:     "nodeclass/" + name,
		Kind:   topology.KindNodeClass,
		Name:   name,
		Status: topology.StatusHealthy,
		Data: map[string]any{
			// No namespace — NodeClass is cluster-scoped.
			"apiVersion": "karpenter.k8s.aws/v1",
		},
	}
}

func makeKnativeServiceNode(ns, name string) *topology.Node {
	return &topology.Node{
		ID:     "knativeservice/" + ns + "/" + name,
		Kind:   topology.KindKnativeService,
		Name:   name,
		Status: topology.StatusHealthy,
		Data: map[string]any{
			"namespace":  ns,
			"apiVersion": "serving.knative.dev/v1",
		},
	}
}

// TestCanReadNeighborhoodNode_NodeClassRequiresPerProviderSAR pins the
// pseudo-kind cluster-scoped fix: NodeClass is a topology-only label that
// ClassifyKindScope doesn't recognize. Without the clusterScopedTopologyKinds
// lookup, NodeClass nodes hit the unclassified+empty-namespace allow branch
// and leak to users without provider-specific RBAC.
func TestCanReadNeighborhoodNode_NodeClassDeniedWithoutSAR(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	perms := &auth.UserPermissions{AllowedNamespaces: nil}
	// Deny all NodeClass variants. The helper iterates the table; entries
	// not in discovery (typical for the test env) are skipped by the
	// discovery filter — so the SARs only fire on what discovery has.
	// disc=nil in tests → no entries filtered out → all 3 SARs run.
	perms.SetCanI("get", "karpenter.k8s.aws", "ec2nodeclasses", "", false)
	perms.SetCanI("get", "karpenter.azure.com", "aksnodeclasses", "", false)
	perms.SetCanI("get", "karpenter.k8s.gcp", "gcpnodeclasses", "", false)
	s.permCache.Set("alice", perms)

	r := requestWithUser("GET", "/api/ai/neighborhood/nodeclass/_/x", &auth.User{Username: "alice"})
	n := makeNodeClassNode("default-class")
	if s.canReadNeighborhoodNode(r, n) {
		t.Error("NodeClass pseudo-kind leaked to user without any provider get-SAR")
	}
}

// Counterpart: user with one provider's get-SAR sees NodeClass nodes. Mirrors
// the topology-strip semantics — denial requires ALL discovery-present
// providers to fail; a single allow is sufficient.
func TestCanReadNeighborhoodNode_NodeClassAllowedWithProviderSAR(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	perms := &auth.UserPermissions{AllowedNamespaces: nil}
	// Bob has EC2 access only — should still pass for NodeClass.
	perms.SetCanI("get", "karpenter.k8s.aws", "ec2nodeclasses", "", true)
	perms.SetCanI("get", "karpenter.azure.com", "aksnodeclasses", "", false)
	perms.SetCanI("get", "karpenter.k8s.gcp", "gcpnodeclasses", "", false)
	s.permCache.Set("bob", perms)

	r := requestWithUser("GET", "/api/ai/neighborhood/nodeclass/_/x", &auth.User{Username: "bob"})
	n := makeNodeClassNode("default-class")
	if !s.canReadNeighborhoodNode(r, n) {
		t.Error("NodeClass denied for user with EC2 get-SAR — single-provider RBAC must allow")
	}
}

// TestCanReadNeighborhoodNode_NodeClassPerVariantSAR pins the per-variant
// authorization fix: NodeClass has 3 entries in ClusterScopedKinds (EC2 /
// AKS / GCP). Before the fix, the helper iterated ALL entries and returned
// true on the FIRST passing SAR — so a user with EC2 RBAC saw AKS and GCP
// NodeClass nodes too. The fix matches the table row by BOTH Kind and the
// node's apiVersion-group, so an AKS NodeClass node is SARed against the
// AKS row only.
//
// Setup: Bob has EC2 RBAC only. An AKS NodeClass node (apiVersion=
// karpenter.azure.com/v1beta1) must be denied — his EC2 grant must not
// leak across providers.
func TestCanReadNeighborhoodNode_NodeClassPerVariantDeniesWrongProvider(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	perms := &auth.UserPermissions{AllowedNamespaces: nil}
	perms.SetCanI("get", "karpenter.k8s.aws", "ec2nodeclasses", "", true)
	perms.SetCanI("get", "karpenter.azure.com", "aksnodeclasses", "", false)
	perms.SetCanI("get", "karpenter.k8s.gcp", "gcpnodeclasses", "", false)
	s.permCache.Set("bob", perms)

	r := requestWithUser("GET", "/api/ai/neighborhood/nodeclass/_/x", &auth.User{Username: "bob"})

	// AKS NodeClass node — apiVersion-group is karpenter.azure.com. The
	// per-variant lookup must SAR ONLY the AKS row, which is denied.
	aks := &topology.Node{
		ID:     "nodeclass/aks-default",
		Kind:   topology.KindNodeClass,
		Name:   "aks-default",
		Status: topology.StatusHealthy,
		Data: map[string]any{
			"apiVersion": "karpenter.azure.com/v1beta1",
		},
	}
	if s.canReadNeighborhoodNode(r, aks) {
		t.Error("AKS NodeClass leaked to user with EC2-only RBAC — per-variant gate must deny cross-provider")
	}

	// Sanity counterpart: same user, EC2 NodeClass — must allow.
	ec2 := &topology.Node{
		ID:     "nodeclass/ec2-default",
		Kind:   topology.KindNodeClass,
		Name:   "ec2-default",
		Status: topology.StatusHealthy,
		Data: map[string]any{
			"apiVersion": "karpenter.k8s.aws/v1",
		},
	}
	if !s.canReadNeighborhoodNode(r, ec2) {
		t.Error("EC2 NodeClass denied to user with EC2 RBAC — per-variant gate must allow the matching provider")
	}
}

// KnativeService is a namespaced pseudo-kind. The cluster-scoped table
// shouldn't match it; the helper must fall through to the namespaced branch
// and ride on namespace access alone (no per-kind tightening for Knative).
func TestCanReadNeighborhoodNode_KnativeServiceUsesNamespaceGate(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	s.permCache.Set("alice", &auth.UserPermissions{AllowedNamespaces: []string{"prod"}})

	r := requestWithUser("GET", "/api/ai/neighborhood/service/prod/api", &auth.User{Username: "alice"})
	n := makeKnativeServiceNode("prod", "api")
	if !s.canReadNeighborhoodNode(r, n) {
		t.Error("namespaced pseudo-kind KnativeService denied — namespace access should be sufficient")
	}

	// Sanity: user without namespace access → denied.
	s.permCache.Set("carol", &auth.UserPermissions{AllowedNamespaces: []string{"staging"}})
	r2 := requestWithUser("GET", "/api/ai/neighborhood/service/prod/api", &auth.User{Username: "carol"})
	if s.canReadNeighborhoodNode(r2, n) {
		t.Error("KnativeService allowed for user without namespace access — namespace gate must apply")
	}
}

// pseudoKindTuplesForTest is the test analogue of the inline
// topology.RBACTuplesForKind call in handleAINeighborhood — disc=nil in
// tests, so no rows are filtered, exactly matching the production path with
// discovery offline.
func pseudoKindTuplesForTest(kind, group string) (tuples []topology.SARTuple, fallthroughAllow bool) {
	t, _, fa := topology.RBACTuplesForKind(kind, group, nil)
	return t, fa
}

// TestAllowPseudoKindTuples_NodeClass pins the root-preflight helper: a user
// without ANY provider get-SAR for NodeClass must be denied, while a user
// with EC2 RBAC must be allowed (single-provider grant is sufficient,
// matching the per-node gate). This is the kind-only variant used at root
// preflight before the topology has resolved a concrete node with an
// apiVersion — so we iterate every table row under that kind and allow on
// any pass.
func TestAllowPseudoKindTuples_NodeClass_DeniedWithoutSAR(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	perms := &auth.UserPermissions{AllowedNamespaces: nil}
	perms.SetCanI("get", "karpenter.k8s.aws", "ec2nodeclasses", "", false)
	perms.SetCanI("get", "karpenter.azure.com", "aksnodeclasses", "", false)
	perms.SetCanI("get", "karpenter.k8s.gcp", "gcpnodeclasses", "", false)
	s.permCache.Set("alice", perms)

	r := requestWithUser("GET", "/api/ai/neighborhood/nodeclass/_/x", &auth.User{Username: "alice"})
	tuples, fallthroughAllow := pseudoKindTuplesForTest("nodeclass", "")
	if len(tuples) == 0 {
		t.Fatal("RBACTuplesForKind returned 0 tuples for nodeclass — table wiring is broken")
	}
	if s.allowPseudoKindTuples(r, tuples, fallthroughAllow) {
		t.Error("nodeclass root preflight allowed user without any provider get-SAR — must deny")
	}
}

func TestAllowPseudoKindTuples_NodeClass_AllowedWithProviderSAR(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	perms := &auth.UserPermissions{AllowedNamespaces: nil}
	// Bob has EC2 RBAC only. Root preflight without a known provider group
	// must still allow — the per-node Allow gate will then drop AKS/GCP
	// variants. This mirrors topology-strip semantics: a single provider
	// grant is sufficient for the kind-level gate.
	perms.SetCanI("get", "karpenter.k8s.aws", "ec2nodeclasses", "", true)
	perms.SetCanI("get", "karpenter.azure.com", "aksnodeclasses", "", false)
	perms.SetCanI("get", "karpenter.k8s.gcp", "gcpnodeclasses", "", false)
	s.permCache.Set("bob", perms)

	r := requestWithUser("GET", "/api/ai/neighborhood/nodeclass/_/x", &auth.User{Username: "bob"})
	tuples, fallthroughAllow := pseudoKindTuplesForTest("nodeclass", "")
	if !s.allowPseudoKindTuples(r, tuples, fallthroughAllow) {
		t.Error("nodeclass root preflight denied user with EC2 get-SAR — single-provider RBAC must pass")
	}
}

// Same shape for NodePool, which has a SINGLE row in the table (karpenter.sh).
// Pin that the kind-only path works for single-entry pseudo-kinds too.
func TestAllowPseudoKindTuples_NodePool(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	denyPerms := &auth.UserPermissions{AllowedNamespaces: nil}
	denyPerms.SetCanI("get", "karpenter.sh", "nodepools", "", false)
	s.permCache.Set("alice", denyPerms)

	allowPerms := &auth.UserPermissions{AllowedNamespaces: nil}
	allowPerms.SetCanI("get", "karpenter.sh", "nodepools", "", true)
	s.permCache.Set("bob", allowPerms)

	tuples, fallthroughAllow := pseudoKindTuplesForTest("nodepool", "")
	if len(tuples) != 1 {
		t.Fatalf("expected 1 nodepool tuple, got %d", len(tuples))
	}
	rDeny := requestWithUser("GET", "/api/ai/neighborhood/nodepool/_/x", &auth.User{Username: "alice"})
	if s.allowPseudoKindTuples(rDeny, tuples, fallthroughAllow) {
		t.Error("nodepool root preflight allowed user without karpenter.sh/nodepools get-SAR")
	}
	rAllow := requestWithUser("GET", "/api/ai/neighborhood/nodepool/_/x", &auth.User{Username: "bob"})
	if !s.allowPseudoKindTuples(rAllow, tuples, fallthroughAllow) {
		t.Error("nodepool root preflight denied user WITH karpenter.sh/nodepools get-SAR")
	}
}

// TestNeighborhood_NodeClassRootPreflightNotBadRequest is the integration
// pin: the URL /api/ai/neighborhood/nodeclass/_/foo must NOT return 400
// "namespace is required" — that's the regression we're fixing.
//
// Two assertions:
//   - Unauthorized user (no provider get-SAR): preflight must reject as 403,
//     not 400.
//   - Authorized user (EC2 get-SAR): preflight passes, BFS runs against the
//     test cache (which has no NodeClass nodes), root lookup misses → 404,
//     also not 400. Both prove the preflight is no longer rejecting on
//     namespace.
func TestNeighborhood_NodeClassRootPreflightNotBadRequest(t *testing.T) {
	env := newAuthTestServer(t)

	// Unauthorized: all provider SARs denied → 403, not 400.
	denyPerms := &auth.UserPermissions{AllowedNamespaces: nil}
	denyPerms.SetCanI("get", "karpenter.k8s.aws", "ec2nodeclasses", "", false)
	denyPerms.SetCanI("get", "karpenter.azure.com", "aksnodeclasses", "", false)
	denyPerms.SetCanI("get", "karpenter.k8s.gcp", "gcpnodeclasses", "", false)
	env.srv.permCache.Set("alice", denyPerms)

	resp := env.authGet(t, "/api/ai/neighborhood/nodeclass/_/foo", "alice", "")
	resp.Body.Close()
	if resp.StatusCode == http.StatusBadRequest {
		t.Errorf("nodeclass root preflight returned 400 — regression: pseudo-kind classified as namespaced")
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("unauthorized user got %d for nodeclass — want 403 (cluster-scoped pseudo-kind gate)", resp.StatusCode)
	}

	// Authorized: EC2 get-SAR allowed → preflight passes; BFS finds no
	// NodeClass node in the seeded cache → 404, not 400.
	allowPerms := &auth.UserPermissions{AllowedNamespaces: nil}
	allowPerms.SetCanI("get", "karpenter.k8s.aws", "ec2nodeclasses", "", true)
	allowPerms.SetCanI("get", "karpenter.azure.com", "aksnodeclasses", "", false)
	allowPerms.SetCanI("get", "karpenter.k8s.gcp", "gcpnodeclasses", "", false)
	env.srv.permCache.Set("bob", allowPerms)

	resp2 := env.authGet(t, "/api/ai/neighborhood/nodeclass/_/foo", "bob", "")
	resp2.Body.Close()
	if resp2.StatusCode == http.StatusBadRequest {
		t.Errorf("nodeclass root preflight returned 400 even with EC2 SAR — regression")
	}
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("authorized user got %d for nonexistent nodeclass — want 404 (preflight passed, BFS empty)", resp2.StatusCode)
	}
}

// Same for NodePool — sanity that the fix covers all pseudo-kinds the
// agent might request, not just NodeClass.
func TestNeighborhood_NodePoolRootPreflightNotBadRequest(t *testing.T) {
	env := newAuthTestServer(t)

	denyPerms := &auth.UserPermissions{AllowedNamespaces: nil}
	denyPerms.SetCanI("get", "karpenter.sh", "nodepools", "", false)
	env.srv.permCache.Set("alice", denyPerms)
	resp := env.authGet(t, "/api/ai/neighborhood/nodepool/_/foo", "alice", "")
	resp.Body.Close()
	if resp.StatusCode == http.StatusBadRequest {
		t.Errorf("nodepool root preflight returned 400 — pseudo-kind misclassified as namespaced")
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("unauthorized user got %d for nodepool — want 403", resp.StatusCode)
	}

	allowPerms := &auth.UserPermissions{AllowedNamespaces: nil}
	allowPerms.SetCanI("get", "karpenter.sh", "nodepools", "", true)
	env.srv.permCache.Set("bob", allowPerms)
	resp2 := env.authGet(t, "/api/ai/neighborhood/nodepool/_/foo", "bob", "")
	resp2.Body.Close()
	if resp2.StatusCode == http.StatusBadRequest {
		t.Errorf("nodepool root preflight returned 400 even with karpenter.sh SAR")
	}
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("authorized user got %d for nonexistent nodepool — want 404", resp2.StatusCode)
	}
}

// TestNeighborhood_SecretRootIncluded pins the IncludeSecrets fix at the
// REST handler boundary. DefaultBuildOptions sets IncludeSecrets=false, so
// Secret nodes don't enter the topology and root lookup for kind=secret
// always returns "resource not found in topology" → 404, even for users
// authorized to read the Secret. The handler must override
// IncludeSecrets=true so authorized callers find the root, while the
// per-namespace `get secrets` Allow gate still 404s unauthorized callers
// via the empty-subgraph path.
//
// The seeded fake client (server_smoke_test.go TestMain) has a Secret
// "nginx-tls" in "default". This test depends on that seed.
func TestNeighborhood_SecretRootIncluded(t *testing.T) {
	env := newAuthTestServer(t)

	// Authorized user: namespace access to default + per-namespace
	// `get secrets` SAR. Both Allow checks (namespace + per-kind Secret)
	// must pass.
	env.srv.permCache.Set("bob", &auth.UserPermissions{
		AllowedNamespaces: []string{"default"},
	})
	bobPerms := env.srv.permCache.Get("bob")
	bobPerms.SetCanI("get", "", "secrets", "default", true)

	resp := env.authGet(t, "/api/ai/neighborhood/secret/default/nginx-tls", "bob", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("authorized user got %d for Secret root — IncludeSecrets override must surface Secret nodes in topology", resp.StatusCode)
	}

	// Unauthorized user: namespace access to default but NO per-namespace
	// `get secrets` SAR. Allow rejects the root → empty subgraph → 404.
	// Existence-hiding preserved: same 404 the IncludeSecrets=false path
	// produced, but now driven by RBAC instead of topology elision.
	env.srv.permCache.Set("alice", &auth.UserPermissions{
		AllowedNamespaces: []string{"default"},
	})
	alicePerms := env.srv.permCache.Get("alice")
	alicePerms.SetCanI("get", "", "secrets", "default", false)

	resp2 := env.authGet(t, "/api/ai/neighborhood/secret/default/nginx-tls", "alice", "")
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotFound {
		t.Errorf("unauthorized user got %d for Secret root — Allow gate must produce 404 via empty subgraph (existence-hiding)", resp2.StatusCode)
	}
}
