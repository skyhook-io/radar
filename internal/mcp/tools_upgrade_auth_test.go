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
	perms := getPermCache().Get("upgrade-matrix", nil)
	perms.SetCanI("list", "", "nodes", "", true)
	perms.SetCanI("list", "", "persistentvolumes", "", false)
	perms.SetCanI("list", "storage.k8s.io", "csidrivers", "", true)
	perms.SetCanI("list", "admissionregistration.k8s.io", "validatingwebhookconfigurations", "", true)
	perms.SetCanI("list", "scheduling.k8s.io", "workloads", "team-a", true)
	perms.SetCanI("list", "", "secrets", "team-a", true)
	perms.SetCanI("list", "", "secrets", "team-b", false)

	authz := mcpUpgradeAuthorizer{ctx: ctx}
	for _, tc := range []struct {
		group, resource, namespace     string
		wantAllowed, wantAuthoritative bool
	}{
		{"", "nodes", "", true, true},
		{"", "persistentvolumes", "", false, true},
		{"storage.k8s.io", "csidrivers", "", true, true},
		{"admissionregistration.k8s.io", "validatingwebhookconfigurations", "", true, true},
		{"scheduling.k8s.io", "workloads", "team-a", true, true},
		// Unseeded → SAR against a nil client → fail closed. Cluster-wide pod
		// visibility must not imply cluster-scoped reads.
		{"apiregistration.k8s.io", "apiservices", "", false, false},
	} {
		if got := authz.CanList(tc.group, tc.resource, tc.namespace); got.Allowed != tc.wantAllowed || got.Authoritative != tc.wantAuthoritative {
			t.Fatalf("CanList(%q,%q,%q) = %+v, want allowed=%v authoritative=%v", tc.group, tc.resource, tc.namespace, got, tc.wantAllowed, tc.wantAuthoritative)
		}
	}
	filtered := authz.FilterNamespacesByCanList("", "secrets", []string{"team-a", "team-b"})
	if len(filtered) != 1 || filtered[0] != "team-a" {
		t.Fatalf("FilterNamespacesByCanList = %v, want [team-a]", filtered)
	}
	// Subresource SARs are not memoized; with no apiserver they fail closed
	// for an authenticated user and pass through when auth is off.
	if decision := authz.CanGetSubresource("", "nodes", "proxy"); decision.Allowed || decision.Authoritative {
		t.Fatalf("authenticated nodes/proxy with no apiserver = %+v, want non-authoritative failure", decision)
	}
	if decision := (mcpUpgradeAuthorizer{ctx: context.Background()}).CanGetSubresource("", "nodes", "proxy"); !decision.Allowed || !decision.Authoritative {
		t.Fatalf("auth-off subresource decision = %+v, want authoritative pass-through", decision)
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
	getPermCache().Set("upgrade-scope-user", nil, &pkgauth.UserPermissions{AllowedNamespaces: []string{"tenant-b"}})
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
