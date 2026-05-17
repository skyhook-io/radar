package server

import (
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
