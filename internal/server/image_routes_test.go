package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
)

func TestImageRoutesPodAuthorization(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	previousClient := k8s.SetTestClient(nil)
	t.Cleanup(func() { k8s.SetTestClient(previousClient) })
	s := &Server{authConfig: auth.Config{Mode: "proxy"}, permCache: auth.NewPermissionCache()}
	s.authConfig.Defaults()
	router := chi.NewRouter()
	s.setupAppRoutes(router)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"team"}}
	perms.SetCanI("list", "", "pods", "team", true)
	perms.SetCanI("get", "", "pods", "team", false)
	s.permCache.Set("denied", nil, perms)
	s.permCache.Set("check-error", nil, &auth.UserPermissions{AllowedNamespaces: []string{"team"}})

	for _, route := range []string{"metadata", "inspect", "file"} {
		for _, tc := range []struct {
			name, query, user, message string
			status                     int
		}{
			{"missing namespace", "&pod=app", "denied", "namespace and pod", 400},
			{"missing pod", "&namespace=team", "denied", "namespace and pod", 400},
			{"get denied despite list allowed", "&namespace=team&pod=app", "denied", "rbac_denied: requires get pods in namespace team", 403},
			{"check failed", "&namespace=team&pod=app", "check-error", "Could not authorize get pods", 503},
		} {
			t.Run(route+"/"+tc.name, func(t *testing.T) {
				r := httptest.NewRequest(http.MethodGet, "/api/images/"+route+"?image=example.com/app:v1&path=/file.txt"+tc.query, nil)
				r.Header.Set("X-Forwarded-User", tc.user)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.message) {
					t.Fatalf("got %d %s, want %d %q", w.Code, w.Body.String(), tc.status, tc.message)
				}
			})
		}
	}
	s.permCache = nil
	for _, route := range []string{"metadata", "inspect", "file"} {
		r := httptest.NewRequest(http.MethodGet, "/api/images/"+route+"?image=example.com/app:v1&path=/file.txt&namespace=team&pod=app", nil)
		r.Header.Set("X-Forwarded-User", "alice")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("missing permission cache: got %d %s", w.Code, w.Body.String())
		}
	}
}
