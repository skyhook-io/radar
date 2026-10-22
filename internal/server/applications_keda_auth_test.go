package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/pkg/topology"
)

func TestApplicationAuthenticationReferencesAreFilteredWithoutChangingSharedRows(t *testing.T) {
	s := &Server{permCache: auth.NewPermissionCache()}
	references := []topology.ResourceRef{
		{Kind: "TriggerAuthentication", Group: "keda.sh", Namespace: "team", Name: "private-local"},
		{Kind: "ClusterTriggerAuthentication", Group: "keda.sh", Name: "private-cluster"},
	}
	for i := 0; i < 21; i++ {
		references = append(references, topology.ResourceRef{Kind: "ConfigMap", Namespace: "team", Name: fmt.Sprintf("config-%02d", i)})
	}
	shared := &applicationsResponse{Applications: []appRow{{Relationships: &appRelationships{Configs: len(references), ConfigRefs: sortedRefs(refsByKey(references), 20), configRefs: refsByKey(references)}}}}
	for _, allowed := range [][2]bool{{false, false}, {true, false}, {false, true}, {true, true}, {false, false}} {
		permissions := &auth.UserPermissions{AllowedNamespaces: []string{"team"}}
		permissions.SetCanI("get", "keda.sh", "triggerauthentications", "team", allowed[0])
		permissions.SetCanI("get", "keda.sh", "clustertriggerauthentications", "", allowed[1])
		s.permCache.Set("viewer", nil, permissions)
		r := httptest.NewRequest(http.MethodGet, "/api/applications", nil)
		r = r.WithContext(auth.ContextWithUser(r.Context(), &auth.User{Username: "viewer"}))
		response := s.filterApplicationAuthenticationRefs(r, shared)
		expected := 21
		for _, yes := range allowed {
			if yes {
				expected++
			}
		}
		rel := response.Applications[0].Relationships
		if rel.Configs != expected || len(rel.ConfigRefs) != 20 || len(rel.configRefs) != expected {
			t.Fatalf("allowed=%v count/ref leak: %+v", allowed, rel)
		}
		for _, ref := range rel.configRefs {
			if ref.Kind == "TriggerAuthentication" && !allowed[0] || ref.Kind == "ClusterTriggerAuthentication" && !allowed[1] {
				t.Fatalf("denied ref retained: %+v", ref)
			}
		}
		if shared.Applications[0].Relationships.Configs != 23 || len(shared.Applications[0].Relationships.configRefs) != 23 {
			t.Fatal("per-user projection mutated shared cache")
		}
	}
	anonymous := httptest.NewRequest(http.MethodGet, "/api/applications", nil)
	if s.filterApplicationAuthenticationRefs(anonymous, shared) != shared {
		t.Fatal("local no-auth response was copied or changed")
	}
}
