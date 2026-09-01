package server

import (
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
)

// The cost handlers key entirely off the nil-vs-empty contract of
// Scope.Namespaces, so pin what openCostScope produces for each identity.
func TestOpenCostScope_NoAuthIsUnrestricted(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "none"})

	scope := s.openCostScope(httptest.NewRequest("GET", "/opencost/summary", nil))
	if scope.Namespaces != nil {
		t.Errorf("Namespaces = %v, want nil (unrestricted)", scope.Namespaces)
	}
	if scope.Restricted() {
		t.Error("Restricted() = true, want false with auth disabled")
	}
	if !scope.CanReadNodes {
		t.Error("CanReadNodes = false, want true with auth disabled")
	}
}

func TestOpenCostScope_ClusterAdminIsUnrestricted(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	user := &auth.User{Username: "admin"}
	s.permCache.Set("admin", nil, &auth.UserPermissions{AllowedNamespaces: nil})

	scope := s.openCostScope(requestWithUser("GET", "/opencost/summary", user))
	if scope.Namespaces != nil {
		t.Errorf("Namespaces = %v, want nil (unrestricted)", scope.Namespaces)
	}
	if scope.Restricted() {
		t.Error("Restricted() = true, want false for a cluster admin")
	}
}

func TestOpenCostScope_RestrictedUserGetsTheirNamespaces(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	user := &auth.User{Username: "dev"}
	s.permCache.Set("dev", nil, &auth.UserPermissions{AllowedNamespaces: []string{"team-a", "team-b"}})

	scope := s.openCostScope(requestWithUser("GET", "/opencost/summary", user))
	if !slices.Equal(scope.Namespaces, []string{"team-a", "team-b"}) {
		t.Fatalf("Namespaces = %v, want [team-a team-b]", scope.Namespaces)
	}
	if !scope.Restricted() {
		t.Error("Restricted() = false, want true")
	}
	if scope.Allows("kube-system") {
		t.Error("Allows(kube-system) = true for a user restricted to team-a/team-b")
	}
	if !scope.Allows("team-a") {
		t.Error("Allows(team-a) = false for a user granted team-a")
	}
}

// A user with an explicitly empty allow-list must come back as empty non-nil,
// not nil — nil would read as "unrestricted" and serve them the whole cluster.
func TestOpenCostScope_UserWithNoNamespacesIsDeniedNotUnrestricted(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	user := &auth.User{Username: "nobody"}
	s.permCache.Set("nobody", nil, &auth.UserPermissions{AllowedNamespaces: []string{}})

	scope := s.openCostScope(requestWithUser("GET", "/opencost/summary", user))
	if scope.Namespaces == nil {
		t.Fatal("Namespaces = nil, want an empty non-nil slice (denied, not unrestricted)")
	}
	if len(scope.Namespaces) != 0 {
		t.Errorf("Namespaces = %v, want empty", scope.Namespaces)
	}
	if !scope.Restricted() {
		t.Error("Restricted() = false, want true")
	}
}
