package server

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

func TestResolveRightsizingScanScopeUnrestrictedRequiresClusterListPerKind(t *testing.T) {
	s := newTestServer(t)
	s.permCache = pkgauth.NewPermissionCache()
	perms := &pkgauth.UserPermissions{AllowedNamespaces: nil}
	perms.SetCanI("list", "apps", "deployments", "", true)
	perms.SetCanI("list", "apps", "statefulsets", "", false)
	perms.SetCanI("list", "apps", "daemonsets", "", true)
	s.permCache.Set("alice", perms)

	scope := s.resolveRightsizingScanScope(reqAs("alice"), nil)
	if namespaces, ok := scope.NamespacesByKind["Deployment"]; !ok || namespaces != nil {
		t.Fatalf("Deployment scope = %v, present=%v; want unrestricted nil scope", namespaces, ok)
	}
	if _, ok := scope.NamespacesByKind["StatefulSet"]; ok {
		t.Fatal("StatefulSet included despite denied cluster-wide list")
	}
	if namespaces, ok := scope.NamespacesByKind["DaemonSet"]; !ok || namespaces != nil {
		t.Fatalf("DaemonSet scope = %v, present=%v; want unrestricted nil scope", namespaces, ok)
	}
	if !slices.Equal(scope.RestrictedKinds, []string{"StatefulSet"}) {
		t.Fatalf("restricted kinds = %v, want [StatefulSet]", scope.RestrictedKinds)
	}
}

func TestResolveRightsizingScanScopeSelectedNamespacesWithoutAuth(t *testing.T) {
	s := newTestServer(t)
	scope := s.resolveRightsizingScanScope(reqAs(""), []string{"beta", "alpha"})
	for _, kind := range []string{"Deployment", "StatefulSet", "DaemonSet"} {
		if got := scope.NamespacesByKind[kind]; !slices.Equal(got, []string{"alpha", "beta"}) {
			t.Errorf("%s namespaces = %v, want [alpha beta]", kind, got)
		}
	}
	if len(scope.RestrictedKinds) != 0 {
		t.Fatalf("restricted kinds = %v, want none", scope.RestrictedKinds)
	}
}

func TestResolveRightsizingScanScopeFiltersEachKindAcrossNamespaces(t *testing.T) {
	s := newTestServer(t)
	s.permCache = pkgauth.NewPermissionCache()
	perms := &pkgauth.UserPermissions{AllowedNamespaces: []string{"alpha", "beta"}}
	perms.SetCanI("list", "apps", "deployments", "alpha", true)
	perms.SetCanI("list", "apps", "deployments", "beta", false)
	perms.SetCanI("list", "apps", "statefulsets", "alpha", true)
	perms.SetCanI("list", "apps", "statefulsets", "beta", true)
	perms.SetCanI("list", "apps", "daemonsets", "alpha", false)
	perms.SetCanI("list", "apps", "daemonsets", "beta", false)
	s.permCache.Set("alice", perms)

	scope := s.resolveRightsizingScanScope(reqAs("alice"), []string{"beta", "alpha"})
	if got := scope.NamespacesByKind["Deployment"]; !slices.Equal(got, []string{"alpha"}) {
		t.Fatalf("Deployment namespaces = %v, want [alpha]", got)
	}
	if got := scope.NamespacesByKind["StatefulSet"]; !slices.Equal(got, []string{"alpha", "beta"}) {
		t.Fatalf("StatefulSet namespaces = %v, want [alpha beta]", got)
	}
	if _, ok := scope.NamespacesByKind["DaemonSet"]; ok {
		t.Fatal("DaemonSet included despite no readable requested namespace")
	}
	if !slices.Equal(scope.RestrictedKinds, []string{"Deployment", "DaemonSet"}) {
		t.Fatalf("restricted kinds = %v, want [Deployment DaemonSet]", scope.RestrictedKinds)
	}
}

func TestResolveRightsizingScanScopeNoNamespaceAccessExcludesAllKinds(t *testing.T) {
	s := newTestServer(t)
	scope := s.resolveRightsizingScanScope(reqAs(""), []string{})
	if len(scope.NamespacesByKind) != 0 {
		t.Fatalf("scope includes kinds with no namespace access: %v", scope.NamespacesByKind)
	}
	if !slices.Equal(scope.RestrictedKinds, []string{"Deployment", "StatefulSet", "DaemonSet"}) {
		t.Fatalf("restricted kinds = %v, want every scan kind", scope.RestrictedKinds)
	}
}

func TestHandleRightsizingScanExplicitDeniedNamespaceReturnsForbidden(t *testing.T) {
	s := newTestServer(t)
	s.permCache = pkgauth.NewPermissionCache()
	s.permCache.Set("alice", &pkgauth.UserPermissions{AllowedNamespaces: []string{"alpha"}})
	req := httptest.NewRequest(http.MethodPost, "/api/prometheus/rightsizing/scan?namespace=beta", nil)
	req = req.WithContext(pkgauth.ContextWithUser(req.Context(), &pkgauth.User{Username: "alice"}))
	rec := httptest.NewRecorder()

	s.handleRightsizingScan(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHasExplicitNamespaceFilter(t *testing.T) {
	for _, tt := range []struct {
		url  string
		want bool
	}{
		{url: "/api/prometheus/rightsizing/scan", want: false},
		{url: "/api/prometheus/rightsizing/scan?namespace=alpha", want: true},
		{url: "/api/prometheus/rightsizing/scan?namespaces=alpha,beta", want: true},
		{url: "/api/prometheus/rightsizing/scan?namespaces=,,", want: false},
	} {
		req := httptest.NewRequest(http.MethodPost, tt.url, nil)
		if got := hasExplicitNamespaceFilter(req); got != tt.want {
			t.Errorf("hasExplicitNamespaceFilter(%q) = %v, want %v", tt.url, got, tt.want)
		}
	}
}
