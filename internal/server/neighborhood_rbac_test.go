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
