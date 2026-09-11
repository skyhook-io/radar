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
	s.permCache.Set("alice", nil, perms)
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

func TestApplyClusterScopedTopologyRBACFiltersCalicoByExactGroup(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	perms := &auth.UserPermissions{AllowedNamespaces: nil}
	perms.SetCanI("list", "projectcalico.org", "globalnetworkpolicies", "", true)
	perms.SetCanI("list", "crd.projectcalico.org", "globalnetworkpolicies", "", false)
	s.permCache.Set("alice", nil, perms)
	r := requestWithUser("GET", "/api/topology", &auth.User{Username: "alice"})

	projectID := "calicoglobalnetworkpolicy//shared/projectcalico.org"
	legacyID := "calicoglobalnetworkpolicy//shared/crd.projectcalico.org"
	nativeID := "networkpolicy/demo/native"
	topo := &topology.Topology{
		Nodes: []topology.Node{
			{ID: projectID, Kind: topology.KindCalicoGlobalNetworkPolicy, Name: "shared", Data: map[string]any{"apiVersion": "projectcalico.org/v3"}},
			{ID: legacyID, Kind: topology.KindCalicoGlobalNetworkPolicy, Name: "shared", Data: map[string]any{"apiVersion": "crd.projectcalico.org/v1"}},
			{ID: nativeID, Kind: topology.KindNetworkPolicy, Name: "native", Data: map[string]any{"namespace": "demo", "apiVersion": "networking.k8s.io/v1"}},
		},
	}

	s.applyClusterScopedTopologyRBAC(r, topo)
	if len(topo.Nodes) != 2 {
		t.Fatalf("nodes = %+v, want project Calico policy and native NetworkPolicy", topo.Nodes)
	}
	for _, node := range topo.Nodes {
		if node.ID == legacyID {
			t.Fatal("crd.projectcalico.org policy survived exact REST topology filtering")
		}
	}
}
