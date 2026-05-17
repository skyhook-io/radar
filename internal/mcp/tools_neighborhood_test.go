package mcp

import (
	"testing"

	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/topology"
)

// canReadNeighborhoodNodeMCP must apply per-kind Secret RBAC inside an allowed
// namespace. Mirrors handleGetResource: namespace access alone is NOT a
// sufficient gate for Secrets because the SA backing the cache may carry
// cluster-wide secrets RBAC (Helm release visibility) the calling user lacks.

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

// TestCanReadNeighborhoodNodeMCP_SecretRequiresPerKindRBAC pins the gate:
// a user with namespace access but no per-namespace `get secrets` SAR must be
// denied. The cache may hold Secrets the user can't read directly — the
// neighborhood graph must not leak them.
func TestCanReadNeighborhoodNodeMCP_SecretRequiresPerKindRBAC(t *testing.T) {
	ctx := withTestUserPerms(t, "alice", nil, []string{"default"})
	// alice has namespace access to default but explicit deny on secrets-get.
	perms := getPermCache().Get("alice")
	perms.SetCanI("get", "", "secrets", "default", false)

	secret := makeSecretNode("default", "nginx-tls")
	if canReadNeighborhoodNodeMCP(ctx, secret) {
		t.Error("Secret node leaked through MCP neighborhood gate: namespace access without per-kind RBAC must deny")
	}
}

// Counterpart: user WITH namespace access AND per-namespace secrets-get
// must be allowed. Locks down the positive path so the gate isn't blanket-deny.
func TestCanReadNeighborhoodNodeMCP_SecretAllowedWithPerKindRBAC(t *testing.T) {
	ctx := withTestUserPerms(t, "bob", nil, []string{"default"})
	perms := getPermCache().Get("bob")
	perms.SetCanI("get", "", "secrets", "default", true)

	secret := makeSecretNode("default", "nginx-tls")
	if !canReadNeighborhoodNodeMCP(ctx, secret) {
		t.Error("authorized user denied: namespace access + per-kind RBAC should pass")
	}
}

// Sanity: non-Secret namespaced kinds (e.g. ConfigMap) ride on the namespace
// gate alone. The Secret-specific tightening must not regress that.
func TestCanReadNeighborhoodNodeMCP_ConfigMapStaysOnNamespaceGate(t *testing.T) {
	ctx := withTestUserPerms(t, "alice", nil, []string{"default"})
	// No configmap-specific SAR seeded — namespace access alone should pass
	// because we deliberately do NOT tighten that for ConfigMap.
	cm := makeConfigMapNode("default", "nginx-conf")
	if !canReadNeighborhoodNodeMCP(ctx, cm) {
		t.Error("ConfigMap node denied: namespace access should be sufficient (no per-kind tightening for ConfigMap)")
	}
}

// Smoke: no-auth callers (no user in context) pass through. Matches the
// passthrough behavior of every per-namespace RBAC helper in this package.
func TestCanReadNeighborhoodNodeMCP_NoAuthPassthrough(t *testing.T) {
	// Empty context — no user attached. Helpers should not deny.
	secret := makeSecretNode("default", "nginx-tls")
	if !canReadNeighborhoodNodeMCP(pkgauth.ContextWithUser(t.Context(), nil), secret) {
		t.Error("no-auth caller denied — Secret gate must not fail-closed when auth is disabled")
	}
}
