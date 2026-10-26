package server

import (
	"sync/atomic"
	"testing"
	"time"

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
	// A rebuild that stores nothing, so only what the test caches is read.
	b.rebuildTopology = func() bool { return false }

	// No topology cached yet → (nil, nil).
	if topo, idx := b.GetCachedTopologyWithIndex(); topo != nil || idx != nil {
		t.Fatalf("expected (nil, nil) with no cached topology, got (%v, %v)", topo, idx)
	}

	topoA := topoWithEdge("deployment/ns/web", "replicaset/ns/web-1")
	b.updateCachedTopology(topoA, b.topoEpoch.Load())

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
	b.updateCachedTopology(topoB, b.topoEpoch.Load())
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
	b.updateCachedTopology(nil, b.topoEpoch.Load())
	if topo, idx := b.GetCachedTopologyWithIndex(); topo != nil || idx != nil {
		t.Fatalf("expected (nil, nil) after clearing topology, got (%v, %v)", topo, idx)
	}
}

// Readers that arrive while another reader builds the graph wait for that
// build, instead of answering from no graph or the one it replaces.
func TestReadersWaitForTheGraphAnotherReaderBuilds(t *testing.T) {
	for name, previous := range map[string]*topology.Topology{
		"first build": nil,
		"dirty graph": topoWithEdge("deployment/ns/old", "replicaset/ns/old-1"),
	} {
		t.Run(name, func(t *testing.T) {
			b := NewSSEBroadcaster()
			b.cachedTopology, b.cachedTopologyDirty = previous, previous != nil
			fresh := topoWithEdge("deployment/ns/web", "replicaset/ns/web-1")
			started, release := make(chan struct{}), make(chan struct{})
			var builds atomic.Int32
			b.rebuildTopology = func() bool {
				builds.Add(1)
				close(started)
				<-release
				return b.updateCachedTopology(fresh, b.topoEpoch.Load())
			}

			const readers = 8
			got := make(chan *topology.Topology, readers)
			go func() { got <- b.GetCachedTopology() }()
			<-started
			for range readers - 1 {
				go func() { got <- b.GetCachedTopology() }()
			}
			select {
			case topo := <-got:
				t.Fatalf("a reader answered before the build ended, with %v", topo)
			case <-time.After(100 * time.Millisecond):
			}
			close(release)
			for range readers {
				if topo := <-got; topo != fresh {
					t.Fatalf("reader got %v, want the graph the build stored", topo)
				}
			}
			if n := builds.Load(); n != 1 {
				t.Fatalf("%d builds, want one shared by every reader", n)
			}
		})
	}
}

// A build that stores nothing leaves the cache to be retried by the next read.
func TestAFailedBuildIsRetriedByTheNextRead(t *testing.T) {
	b := NewSSEBroadcaster()
	fresh := topoWithEdge("deployment/ns/web", "replicaset/ns/web-1")
	attempts := 0
	b.rebuildTopology = func() bool {
		attempts++
		return attempts > 1 && b.updateCachedTopology(fresh, b.topoEpoch.Load())
	}
	if topo := b.GetCachedTopology(); topo != nil {
		t.Fatalf("failed build returned %v", topo)
	}
	if topo := b.GetCachedTopology(); topo != fresh || attempts != 2 {
		t.Fatalf("second read got %v after %d attempts, want the retried build", topo, attempts)
	}
}
