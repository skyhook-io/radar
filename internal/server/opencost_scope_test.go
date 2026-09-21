package server

import (
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
)

func TestOpenCostRouteScopeUsesActiveNamespaceView(t *testing.T) {
	previousContext := k8s.SetTestContextName("test-ctx")
	t.Cleanup(func() { k8s.SetTestContextName(previousContext) })
	s := &Server{}
	req := httptest.NewRequest("GET", "/api/opencost/summary", nil)
	s.setActiveNamespaceForUser(req, []string{"default"})

	got := s.openCostRouteScope().AllowedNamespaces(req, nil)
	if !slices.Equal(got, []string{"default"}) {
		t.Fatalf("cost route namespaces = %v, want active view", got)
	}

	previousForce := k8s.ForceNamespaceScope
	k8s.ForceNamespaceScope = true
	k8s.SetNamespaceScopeOverride("default")
	t.Cleanup(func() {
		k8s.ForceNamespaceScope = previousForce
		k8s.ClearNamespaceScopeOverride()
	})
	requested := s.openCostRouteScope().AllowedNamespaces(req, []string{"other"})
	if len(requested) != 0 {
		t.Fatalf("explicit cost route namespaces = %v, want denied outside active view", requested)
	}
}
