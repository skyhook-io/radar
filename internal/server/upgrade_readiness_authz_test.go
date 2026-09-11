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
	perms.SetCanI("list", "storage.k8s.io", "csidrivers", "", true)
	perms.SetCanI("list", "admissionregistration.k8s.io", "validatingwebhookconfigurations", "", true)
	perms.SetCanI("list", "scheduling.k8s.io", "workloads", "team-a", true)
	perms.SetCanI("list", "", "secrets", "team-a", true)
	perms.SetCanI("list", "", "secrets", "team-b", false)
	s.permCache.Set("upgrade-matrix", nil, perms)

	r := requestWithUser("GET", "/api/upgrade-readiness", &auth.User{Username: "upgrade-matrix"})
	authz := httpUpgradeAuthorizer{s: s, r: r}
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
	if decision := authz.CanGetSubresource("", "nodes", "proxy"); decision.Allowed || decision.Authoritative {
		t.Fatalf("authenticated nodes/proxy with no apiserver = %+v, want non-authoritative failure", decision)
	}
	noAuthReq := requestWithUser("GET", "/api/upgrade-readiness", nil)
	if decision := (httpUpgradeAuthorizer{s: s, r: noAuthReq}).CanGetSubresource("", "nodes", "proxy"); !decision.Allowed || !decision.Authoritative {
		t.Fatalf("auth-off subresource decision = %+v, want authoritative pass-through", decision)
	}
}
