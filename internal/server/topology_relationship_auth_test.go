package server

import (
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/pkg/topology"
	"net/http/httptest"
	"testing"
)

func TestRelationshipTopologyHidesUnreadableEndpointsWithoutMutatingCache(t *testing.T) {
	env := newAuthTestServer(t)
	env.srv.permCache.Set("rel-reader", nil, &auth.UserPermissions{AllowedNamespaces: []string{"default"}})
	seedServerSecretListCanI(t, env, "rel-reader", nil, []string{"default"})
	shared := &topology.Topology{
		Nodes: []topology.Node{
			{ID: "configmap/default/mirror", Kind: topology.KindConfigMap, Data: map[string]any{"namespace": "default"}},
			{ID: "configmap/hidden/source", Kind: topology.KindConfigMap, Data: map[string]any{"namespace": "hidden"}},
			{ID: "secret/default/source", Kind: topology.KindSecret, Data: map[string]any{"namespace": "default"}},
			{ID: "secret/default/mirror", Kind: topology.KindSecret, Data: map[string]any{"namespace": "default"}},
		},
		Edges: []topology.Edge{
			{Source: "configmap/hidden/source", Target: "configmap/default/mirror", Type: topology.EdgeConfigures},
			{Source: "secret/default/source", Target: "secret/default/mirror", Type: topology.EdgeConfigures},
		},
	}
	req := httptest.NewRequest("GET", "/", nil)
	req = req.WithContext(auth.ContextWithUser(req.Context(), &auth.User{Username: "rel-reader"}))
	filtered := env.srv.relationshipTopologyForUser(req, shared)
	if len(filtered.Nodes) != 1 || filtered.Nodes[0].ID != "configmap/default/mirror" || len(filtered.Edges) != 0 {
		t.Fatalf("unreadable relationship survived: %+v", filtered)
	}
	if len(shared.Nodes) != 4 || len(shared.Edges) != 2 {
		t.Fatal("shared topology mutated")
	}
}
