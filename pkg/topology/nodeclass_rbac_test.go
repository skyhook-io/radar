package topology

import "testing"

func nodeClassForRBAC(id, group, resource string) Node {
	return Node{
		ID:   id,
		Kind: KindNodeClass,
		Name: "shared",
		Data: map[string]any{
			"namespace":  "",
			"apiVersion": group + "/v1",
			"resource":   resource,
		},
	}
}

func TestRBACTuplesForNodeUsesExactNodeClassProviderResource(t *testing.T) {
	tests := []struct {
		name     string
		group    string
		resource string
	}{
		{name: "EKS Auto Mode", group: "eks.amazonaws.com", resource: "nodeclasses"},
		{name: "custom provider", group: "infra.example.io", resource: "customnodeclasses"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := nodeClassForRBAC("nodeclass//shared/"+tt.group+"/nodeclass", tt.group, tt.resource)
			decision, tuples := RBACTuplesForNode(&n, nil)
			if decision != NodeRBACCheckTuples {
				t.Fatalf("decision = %v, want NodeRBACCheckTuples", decision)
			}
			want := SARTuple{Group: tt.group, Resource: tt.resource, Namespace: ""}
			if len(tuples) != 1 || tuples[0] != want {
				t.Fatalf("tuples = %+v, want [%+v]", tuples, want)
			}
		})
	}
}

func TestRBACTuplesForNodeRejectsNodeClassWithoutExactResource(t *testing.T) {
	n := nodeClassForRBAC("nodeclass//shared/infra.example.io/customnodeclass", "infra.example.io", "")
	decision, tuples := RBACTuplesForNode(&n, nil)
	if decision != NodeRBACDeny || len(tuples) != 0 {
		t.Fatalf("decision=%v tuples=%+v, want fail-closed deny", decision, tuples)
	}
}

func TestRBACTuplesForKindDefersOpenEndedNodeClassRoots(t *testing.T) {
	for _, group := range []string{"", "eks.amazonaws.com", "infra.example.io"} {
		tuples, tracked, fallthroughAllow := RBACTuplesForKind("nodeclass", group, nil)
		if !tracked || len(tuples) != 0 || !fallthroughAllow {
			t.Fatalf("group=%q: tuples=%+v tracked=%v fallthroughAllow=%v, want deferred per-node authorization", group, tuples, tracked, fallthroughAllow)
		}
	}
}

func TestStripNodeClassesExceptKeepsAuthorizedProviderOnly(t *testing.T) {
	eks := nodeClassForRBAC("nodeclass//shared/eks.amazonaws.com/nodeclass", "eks.amazonaws.com", "nodeclasses")
	custom := nodeClassForRBAC("nodeclass//shared/infra.example.io/customnodeclass", "infra.example.io", "customnodeclasses")
	pool := Node{ID: "nodepool//default", Kind: KindNodePool, Name: "default"}
	topo := &Topology{
		Nodes: []Node{pool, eks, custom},
		Edges: []Edge{
			{ID: "pool-eks", Source: pool.ID, Target: eks.ID, Type: EdgeConfigures},
			{ID: "pool-custom", Source: pool.ID, Target: custom.ID, Type: EdgeConfigures},
		},
	}

	topo.StripNodeClassesExcept(map[SARTuple]bool{
		{Group: "eks.amazonaws.com", Resource: "nodeclasses"}: true,
	})

	if findNode(topo, eks.ID) == nil {
		t.Fatal("authorized EKS NodeClass was stripped")
	}
	if findNode(topo, custom.ID) != nil {
		t.Fatal("unauthorized custom NodeClass survived exact-resource strip")
	}
	if len(topo.Edges) != 1 || topo.Edges[0].Target != eks.ID {
		t.Fatalf("incident edges were not stripped with the denied NodeClass: %+v", topo.Edges)
	}
}
