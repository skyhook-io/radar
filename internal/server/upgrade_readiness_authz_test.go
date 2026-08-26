package server

import (
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
)

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
