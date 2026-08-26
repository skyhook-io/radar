package server

import (
	"context"
	"slices"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
)

// TestHTTPUpgradeAuthorizerDecisionMatrix is one half of the cross-surface
// regression net for the upgrade evidence seam: the same grants seeded into
// the MCP permission cache must produce the same decisions from
// mcpUpgradeAuthorizer (TestMCPUpgradeAuthorizerDecisionMatrix in
// internal/mcp). Change both tables together.
func TestHTTPUpgradeAuthorizerDecisionMatrix(t *testing.T) {
	s := &Server{permCache: auth.NewPermissionCache()}
	perms := &auth.UserPermissions{AllowedNamespaces: nil}
	perms.SetCanI("list", "", "nodes", "", true)
	perms.SetCanI("list", "", "persistentvolumes", "", false)
	perms.SetCanI("list", "admissionregistration.k8s.io", "validatingwebhookconfigurations", "", true)
	perms.SetCanI("list", "", "secrets", "team-a", true)
	perms.SetCanI("list", "", "secrets", "team-b", false)
	s.permCache.Set("upgrade-matrix", perms)

	r := requestWithUser("GET", "/api/upgrade-readiness", &auth.User{Username: "upgrade-matrix"})
	authz := httpUpgradeAuthorizer{s: s, r: r}
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
	if authz.CanGetSubresource("", "nodes", "proxy") {
		t.Fatal("authenticated nodes/proxy with no apiserver to ask must fail closed")
	}
	noAuthReq := requestWithUser("GET", "/api/upgrade-readiness", nil)
	if noAuth := (httpUpgradeAuthorizer{s: s, r: noAuthReq}); !noAuth.CanGetSubresource("", "nodes", "proxy") {
		t.Fatal("auth-off subresource check must pass through (SA RBAC applies at the client layer)")
	}
}

type fakeUpgradeAuthorizer struct {
	namespaces  []string
	canList     map[string]bool
	filterCalls []string
	filtered    []string
}

func (f *fakeUpgradeAuthorizer) Namespaces() []string { return f.namespaces }
func (f *fakeUpgradeAuthorizer) CanList(group, resource, namespace string) bool {
	return f.canList[group+"/"+resource+"/"+namespace]
}
func (f *fakeUpgradeAuthorizer) CanGetSubresource(group, resource, subresource string) bool {
	return false
}
func (f *fakeUpgradeAuthorizer) FilterNamespacesByCanList(group, resource string, namespaces []string) []string {
	f.filterCalls = append(f.filterCalls, group+"/"+resource)
	return f.filtered
}

func TestResolveHelmNamespacesForAuthorizerDecisions(t *testing.T) {
	ctx := context.Background()

	t.Run("no namespaced access refuses helm collection", func(t *testing.T) {
		authz := &fakeUpgradeAuthorizer{namespaces: []string{}}
		if _, ok := resolveHelmNamespacesForAuthorizer(ctx, authz, []string{}); ok {
			t.Fatal("no-access scope must refuse Helm collection entirely")
		}
	})

	t.Run("scoped ceiling passes through unchanged", func(t *testing.T) {
		authz := &fakeUpgradeAuthorizer{}
		got, ok := resolveHelmNamespacesForAuthorizer(ctx, authz, []string{"team-a"})
		if !ok || !slices.Equal(got, []string{"team-a"}) {
			t.Fatalf("scoped resolution = (%v, %v), want the ceiling unchanged", got, ok)
		}
	})

	t.Run("authenticated cluster-wide without secrets narrows to secret-listable namespaces", func(t *testing.T) {
		authz := &fakeUpgradeAuthorizer{
			canList:  map[string]bool{"/secrets/": false},
			filtered: []string{"team-a"},
		}
		ctxUser := auth.ContextWithUser(ctx, &auth.User{Username: "alice"})
		got, ok := resolveHelmNamespacesForAuthorizer(ctxUser, authz, nil)
		if !ok || !slices.Equal(got, []string{"team-a"}) {
			t.Fatalf("secrets-narrowed resolution = (%v, %v), want [team-a]", got, ok)
		}
		if !slices.Contains(authz.filterCalls, "/secrets") {
			t.Fatalf("filter calls = %v, want a per-namespace secrets filter", authz.filterCalls)
		}
	})

	t.Run("authenticated cluster-wide with secrets stays cluster-wide", func(t *testing.T) {
		authz := &fakeUpgradeAuthorizer{canList: map[string]bool{"/secrets/": true}}
		ctxUser := auth.ContextWithUser(ctx, &auth.User{Username: "alice"})
		got, ok := resolveHelmNamespacesForAuthorizer(ctxUser, authz, nil)
		if !ok || got != nil {
			t.Fatalf("cluster-wide resolution = (%v, %v), want nil (cluster-wide)", got, ok)
		}
		if len(authz.filterCalls) != 0 {
			t.Fatalf("filter calls = %v, want none when cluster-wide secrets access exists", authz.filterCalls)
		}
	})
}
