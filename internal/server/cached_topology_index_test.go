package server

import (
	"testing"

	topology "github.com/skyhook-io/radar/pkg/topology"
)

func topoWithEdge(src, dst string) *topology.Topology {
	return &topology.Topology{
		Nodes: []topology.Node{{ID: src}, {ID: dst}},
		Edges: []topology.Edge{{ID: src + "->" + dst, Source: src, Target: dst, Type: topology.EdgeManages}},
	}
}

// GetCachedTopologyWithIndex must return a consistent (topo, index) pair, memoize
// the index across calls for the same topology, and rebuild it when the cached
// topology is replaced. These are the invariants the drawer-relationship
// optimization relies on.
func TestGetCachedTopologyWithIndex(t *testing.T) {
	b := NewSSEBroadcaster()

	// No topology cached yet → (nil, nil). (No k8s cache in tests, so
	// GetCachedTopology won't attempt a rebuild.)
	if topo, idx := b.GetCachedTopologyWithIndex(); topo != nil || idx != nil {
		t.Fatalf("expected (nil, nil) with no cached topology, got (%v, %v)", topo, idx)
	}

	topoA := topoWithEdge("deployment/ns/web", "replicaset/ns/web-1")
	b.updateCachedTopology(topoA)

	gotTopo, idxA := b.GetCachedTopologyWithIndex()
	if gotTopo != topoA {
		t.Fatalf("expected the cached topology pointer back")
	}
	if idxA == nil {
		t.Fatal("expected a non-nil index for a cached topology")
	}
	// The index reflects topoA's edge.
	if _, outgoing := idxA.EdgesFor("deployment/ns/web"); len(outgoing) != 1 {
		t.Fatalf("expected 1 outgoing edge for the deployment node, got %d", len(outgoing))
	}

	// Repeat call memoizes — same index pointer, no rebuild.
	_, idxA2 := b.GetCachedTopologyWithIndex()
	if idxA2 != idxA {
		t.Fatal("expected the memoized index pointer to be reused")
	}

	// Replacing the topology invalidates the index; the next call builds a fresh one.
	topoB := topoWithEdge("statefulset/ns/db", "pod/ns/db-0")
	b.updateCachedTopology(topoB)
	gotTopoB, idxB := b.GetCachedTopologyWithIndex()
	if gotTopoB != topoB {
		t.Fatalf("expected the replaced topology pointer back")
	}
	if idxB == nil || idxB == idxA {
		t.Fatal("expected a fresh index after the topology was replaced")
	}
	if _, outgoing := idxB.EdgesFor("statefulset/ns/db"); len(outgoing) != 1 {
		t.Fatalf("expected 1 outgoing edge in the new index, got %d", len(outgoing))
	}
	// The new index does not carry the old topology's nodes.
	if in, out := idxB.EdgesFor("deployment/ns/web"); len(in) != 0 || len(out) != 0 {
		t.Fatal("stale edge surfaced from a replaced topology's index")
	}

	// Clearing the topology (context-switch semantics) returns (nil, nil).
	b.updateCachedTopology(nil)
	if topo, idx := b.GetCachedTopologyWithIndex(); topo != nil || idx != nil {
		t.Fatalf("expected (nil, nil) after clearing topology, got (%v, %v)", topo, idx)
	}
}
