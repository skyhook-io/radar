package mcp

import (
	"context"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

// TestMCPUpgradeAuthorizerDecisionMatrix is one half of the cross-surface
// regression net for the upgrade evidence seam: the same grants seeded into
// the HTTP permission cache must produce the same decisions from
// httpUpgradeAuthorizer (TestHTTPUpgradeAuthorizerDecisionMatrix in
// internal/server). Change both tables together.
func TestMCPUpgradeAuthorizerDecisionMatrix(t *testing.T) {
	ctx := withClusterAdmin(t, "upgrade-matrix")
	perms := getPermCache().Get("upgrade-matrix")
	perms.SetCanI("list", "", "nodes", "", true)
	perms.SetCanI("list", "", "persistentvolumes", "", false)
	perms.SetCanI("list", "admissionregistration.k8s.io", "validatingwebhookconfigurations", "", true)
	perms.SetCanI("list", "", "secrets", "team-a", true)
	perms.SetCanI("list", "", "secrets", "team-b", false)

	authz := mcpUpgradeAuthorizer{ctx: ctx}
	for _, tc := range []struct {
		group, resource, namespace string
		want                       bool
	}{
		{"", "nodes", "", true},
		{"", "persistentvolumes", "", false},
		{"admissionregistration.k8s.io", "validatingwebhookconfigurations", "", true},
		// Unseeded → SAR against a nil client → fail closed. Cluster-wide pod
		// visibility must not imply cluster-scoped reads.
		{"apiregistration.k8s.io", "apiservices", "", false},
	} {
		if got := authz.CanList(tc.group, tc.resource, tc.namespace); got != tc.want {
			t.Fatalf("CanList(%q,%q,%q) = %v, want %v", tc.group, tc.resource, tc.namespace, got, tc.want)
		}
	}
	filtered := authz.FilterNamespacesByCanList("", "secrets", []string{"team-a", "team-b"})
	if len(filtered) != 1 || filtered[0] != "team-a" {
		t.Fatalf("FilterNamespacesByCanList = %v, want [team-a]", filtered)
	}
	// Subresource SARs are not memoized; with no apiserver they fail closed
	// for an authenticated user and pass through when auth is off.
	if authz.CanGetSubresource("", "nodes", "proxy") {
		t.Fatal("authenticated nodes/proxy with no apiserver to ask must fail closed")
	}
	if noAuth := (mcpUpgradeAuthorizer{ctx: context.Background()}); !noAuth.CanGetSubresource("", "nodes", "proxy") {
		t.Fatal("auth-off subresource check must pass through (SA RBAC applies at the client layer)")
	}
}

func TestMCPUpgradeAuthorizerNamespacesMirrorsHTTPScope(t *testing.T) {
	// Mirrors upgradeReadinessNamespaces: browsing picker ignored, RBAC
	// ceiling applied, --namespace-scope is a hard boundary intersected with
	// the user's access.
	ctx := withClusterAdmin(t, "upgrade-scope-admin")
	if got := (mcpUpgradeAuthorizer{ctx: ctx}).Namespaces(); got != nil {
		t.Fatalf("cluster-admin scope = %v, want cluster-wide (nil)", got)
	}

	restricted := pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: "upgrade-scope-user"})
	getPermCache().Set("upgrade-scope-user", &pkgauth.UserPermissions{AllowedNamespaces: []string{"tenant-b"}})
	if got := (mcpUpgradeAuthorizer{ctx: restricted}).Namespaces(); len(got) != 1 || got[0] != "tenant-b" {
		t.Fatalf("restricted scope = %v, want [tenant-b]", got)
	}

	previousForce := k8s.ForceNamespaceScope
	k8s.ForceNamespaceScope = true
	k8s.SetNamespaceScopeOverride("tenant-a")
	t.Cleanup(func() {
		k8s.SetNamespaceScopeOverride("")
		k8s.ForceNamespaceScope = previousForce
	})
	got := (mcpUpgradeAuthorizer{ctx: restricted}).Namespaces()
	if got == nil || len(got) != 0 {
		t.Fatalf("forced scope tenant-a ∩ user tenant-b = %v, want explicit no-access (empty non-nil)", got)
	}
}
