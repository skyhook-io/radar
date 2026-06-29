package topology

import "testing"

func TestStripNodeKinds_DropsKindAndOrphanEdges(t *testing.T) {
	// Verify the load-bearing claim of StripNodeKinds: nodes whose Kind is
	// in the deny set are dropped, AND every edge that references one of
	// those node IDs is dropped along with them. Nodes / edges not
	// referencing dropped IDs survive untouched.
	topo := &Topology{
		Nodes: []Node{
			{ID: "node/cp-1", Kind: KindNode, Name: "cp-1"},
			{ID: "nodepool/np-1", Kind: KindNodePool, Name: "np-1"},
			{ID: "nodeclass/ec2-default", Kind: KindNodeClass, Name: "ec2-default"},
			{ID: "clusternetworkpolicy/anp-egress", Kind: KindClusterNetworkPolicy, Name: "anp-egress"},
			{ID: "deployment/alpha/web", Kind: KindDeployment, Name: "web"},
			{ID: "pod/alpha/web-abc", Kind: KindPod, Name: "web-abc"},
		},
		Edges: []Edge{
			{Source: "nodepool/np-1", Target: "nodeclass/ec2-default", Type: EdgeConfigures},
			{Source: "node/cp-1", Target: "pod/alpha/web-abc", Type: EdgeManages},
			{Source: "deployment/alpha/web", Target: "pod/alpha/web-abc", Type: EdgeManages},
			{Source: "clusternetworkpolicy/anp-egress", Target: "pod/alpha/web-abc", Type: EdgeProtects},
		},
	}

	topo.StripNodeKinds(map[NodeKind]bool{
		KindNodeClass:            true,
		KindClusterNetworkPolicy: true,
	})

	gotKinds := make(map[NodeKind]bool, len(topo.Nodes))
	for _, n := range topo.Nodes {
		gotKinds[n.Kind] = true
	}
	if gotKinds[KindNodeClass] || gotKinds[KindClusterNetworkPolicy] {
		t.Errorf("denied kinds still present: %v", gotKinds)
	}
	for _, expected := range []NodeKind{KindNode, KindNodePool, KindDeployment, KindPod} {
		if !gotKinds[expected] {
			t.Errorf("non-denied kind %s was dropped", expected)
		}
	}

	// Edges referencing a dropped node must be gone; others must remain.
	for _, e := range topo.Edges {
		if e.Source == "nodeclass/ec2-default" || e.Target == "nodeclass/ec2-default" {
			t.Errorf("orphan edge survived: %+v", e)
		}
		if e.Source == "clusternetworkpolicy/anp-egress" || e.Target == "clusternetworkpolicy/anp-egress" {
			t.Errorf("orphan edge survived: %+v", e)
		}
	}
	// node/cp-1 → pod/alpha/web-abc and deployment/alpha/web → pod/alpha/web-abc
	// reference no dropped nodes; both must remain.
	if len(topo.Edges) != 2 {
		t.Errorf("expected 2 surviving edges (node→pod, deployment→pod), got %d: %+v", len(topo.Edges), topo.Edges)
	}
}

func TestStripNodeKinds_NoOpOnEmptyDeny(t *testing.T) {
	// Sanity: empty deny set must not mutate the topology.
	topo := &Topology{
		Nodes: []Node{{ID: "x", Kind: KindNode}},
		Edges: []Edge{{Source: "x", Target: "y"}},
	}
	topo.StripNodeKinds(nil)
	if len(topo.Nodes) != 1 || len(topo.Edges) != 1 {
		t.Errorf("nil deny mutated topology: nodes=%d edges=%d", len(topo.Nodes), len(topo.Edges))
	}
	topo.StripNodeKinds(map[NodeKind]bool{})
	if len(topo.Nodes) != 1 || len(topo.Edges) != 1 {
		t.Errorf("empty deny mutated topology: nodes=%d edges=%d", len(topo.Nodes), len(topo.Edges))
	}
}

func TestStripNodeKinds_NilTopology(t *testing.T) {
	// Must not panic on nil receiver — callers may pass through a nil
	// topo from an upstream error path.
	var topo *Topology
	topo.StripNodeKinds(map[NodeKind]bool{KindNode: true}) // no panic
}
