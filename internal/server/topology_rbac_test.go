package server

import (
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/pkg/topology"
)

func TestApplyClusterScopedTopologyRBACFiltersNodeClassesByExactProvider(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	perms := &auth.UserPermissions{AllowedNamespaces: nil}
	perms.SetCanI("list", "eks.amazonaws.com", "nodeclasses", "", true)
	perms.SetCanI("list", "infra.example.io", "customnodeclasses", "", false)
	s.permCache.Set("alice", perms)
	r := requestWithUser("GET", "/api/topology", &auth.User{Username: "alice"})

	eksID := "nodeclass//shared/eks.amazonaws.com/nodeclass"
	customID := "nodeclass//shared/infra.example.io/customnodeclass"
	topo := &topology.Topology{
		Nodes: []topology.Node{
			{ID: eksID, Kind: topology.KindNodeClass, Name: "shared", Data: map[string]any{"apiVersion": "eks.amazonaws.com/v1", "resource": "nodeclasses"}},
			{ID: customID, Kind: topology.KindNodeClass, Name: "shared", Data: map[string]any{"apiVersion": "infra.example.io/v1", "resource": "customnodeclasses"}},
		},
		Edges: []topology.Edge{{Source: eksID, Target: customID, Type: topology.EdgeConfigures}},
	}

	s.applyClusterScopedTopologyRBAC(r, topo)
	if len(topo.Nodes) != 1 || topo.Nodes[0].ID != eksID {
		t.Fatalf("nodes = %+v, want authorized EKS NodeClass only", topo.Nodes)
	}
	if len(topo.Edges) != 0 {
		t.Fatalf("edge referencing denied custom NodeClass survived: %+v", topo.Edges)
	}
	if s.deniedClusterScopedTopoKinds(r)[topology.KindNodeClass] {
		t.Fatal("NodeClass re-entered the kind-level deny set")
	}
}
