package server

import (
	"net/http/httptest"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
)

func TestUpgradeReadinessNamespacesIgnoresBrowsingFilter(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/upgrade-readiness?namespaces=payments", nil)
	if got := (&Server{}).upgradeReadinessNamespaces(req); got != nil {
		t.Fatalf("upgrade namespace scope = %v, want cluster-wide", got)
	}
}

func TestUpgradeReadinessNamespacesHonorsForcedScope(t *testing.T) {
	previousForce := k8s.ForceNamespaceScope
	k8s.ForceNamespaceScope = true
	k8s.SetNamespaceScopeOverride("tenant-a")
	t.Cleanup(func() {
		k8s.SetNamespaceScopeOverride("")
		k8s.ForceNamespaceScope = previousForce
	})

	req := httptest.NewRequest("GET", "/api/upgrade-readiness", nil)
	got := (&Server{}).upgradeReadinessNamespaces(req)
	if len(got) != 1 || got[0] != "tenant-a" {
		t.Fatalf("upgrade namespace scope = %v, want [tenant-a]", got)
	}
}

func TestUpgradeReadinessNamespacesIntersectsForcedScopeWithUserAccess(t *testing.T) {
	previousForce := k8s.ForceNamespaceScope
	k8s.ForceNamespaceScope = true
	k8s.SetNamespaceScopeOverride("tenant-a")
	t.Cleanup(func() {
		k8s.SetNamespaceScopeOverride("")
		k8s.ForceNamespaceScope = previousForce
	})

	s := &Server{permCache: auth.NewPermissionCache()}
	s.permCache.Set("alice", nil, &auth.UserPermissions{AllowedNamespaces: []string{"tenant-b"}})
	req := requestWithUser("GET", "/api/upgrade-readiness", &auth.User{Username: "alice"})
	if got := s.upgradeReadinessNamespaces(req); !noNamespaceAccess(got) {
		t.Fatalf("upgrade namespace scope = %v, want no access", got)
	}
}
