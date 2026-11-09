package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
)

func TestApplicationSourcesAreWithheldFromCallersWhoCannotReadThem(t *testing.T) {
	s := &Server{permCache: auth.NewPermissionCache()}
	argo := &appSourceRef{Type: "gitops", Tool: "argocd", Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "billing"}
	release := &appSourceRef{Type: "gitops", Tool: "fluxcd", Group: "helm.toolkit.fluxcd.io", Kind: "HelmRelease", Namespace: "flux-system", Name: "podinfo"}
	helm := &appSourceRef{Type: "helm", Tool: "helm", Kind: "HelmRelease", Namespace: "apps", Name: "web"}
	shared := &applicationsResponse{Applications: []appRow{
		{Key: "argo", SourceRef: argo, SourceStatus: &appSourceStatus{Sync: "OutOfSync", Health: "Degraded"}, RuntimeHealth: "healthy", Health: "degraded"},
		{Key: "flux", SourceRef: release, RuntimeHealth: "healthy", Health: "healthy"},
		{Key: "helm", SourceRef: helm, RuntimeHealth: "healthy", Health: "healthy"},
		{Key: "none", RuntimeHealth: "healthy", Health: "healthy"},
	}, ArgoClaims: []argoClaim{
		{AppNamespace: "argocd", AppName: "billing"},
		{AppNamespace: "team-argocd", AppName: "private"},
	}}
	for _, allowed := range [][2]bool{{false, false}, {true, false}, {false, true}, {true, true}} {
		permissions := &auth.UserPermissions{AllowedNamespaces: []string{"apps"}}
		permissions.SetCanI("get", "argoproj.io", "applications", "argocd", allowed[0])
		permissions.SetCanI("get", "helm.toolkit.fluxcd.io", "helmreleases", "flux-system", allowed[1])
		s.permCache.Set("viewer", nil, permissions)
		r := httptest.NewRequest(http.MethodGet, "/api/applications", nil)
		r = r.WithContext(auth.ContextWithUser(r.Context(), &auth.User{Username: "viewer"}))
		resp := s.filterApplicationsForUser(r, shared)
		rows := resp.Applications
		if len(resp.ArgoClaims) != map[bool]int{true: 1, false: 0}[allowed[0]] || len(resp.ArgoClaims) == 1 && resp.ArgoClaims[0].AppName != "billing" {
			t.Fatalf("allowed=%v claims = %+v", allowed, resp.ArgoClaims)
		}
		if rows[0].sourceWithheld == allowed[0] || rows[1].sourceWithheld == allowed[1] || rows[2].sourceWithheld || rows[3].sourceWithheld {
			t.Fatalf("allowed=%v withheld flags wrong: %+v", allowed, rows)
		}

		if got := rows[0]; allowed[0] != (got.SourceRef != nil) || allowed[0] != (got.SourceStatus != nil) || got.Health != map[bool]string{true: "degraded", false: "healthy"}[allowed[0]] {
			t.Fatalf("allowed=%v Argo row = %+v", allowed, got)
		}
		if got := rows[1]; allowed[1] != (got.SourceRef != nil) {
			t.Fatalf("allowed=%v Flux row = %+v", allowed, got)
		}
		if rows[2].SourceRef != helm {
			t.Fatalf("native Helm source withheld: %+v", rows[2])
		}
		if len(shared.ArgoClaims) != 2 || shared.Applications[0].sourceWithheld || shared.Applications[0].SourceRef != argo || shared.Applications[0].SourceStatus == nil || shared.Applications[0].Health != "degraded" || shared.Applications[1].SourceRef != release {
			t.Fatal("per-user projection mutated shared cache")
		}
	}
	anonymous := httptest.NewRequest(http.MethodGet, "/api/applications", nil)
	if s.filterApplicationsForUser(anonymous, shared) != shared {
		t.Fatal("local no-auth response was copied or changed")
	}
}
