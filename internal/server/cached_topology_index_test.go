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

// A switch to another cluster during a build lets the new cluster's readers,
// including those already waiting, build for it rather than wait out a build
// that can't store; the old build's end then disturbs nothing.
func TestASwitchMidBuildLetsTheNewClusterBuild(t *testing.T) {
	b := NewSSEBroadcaster()
	old := topoWithEdge("deployment/x/old", "replicaset/x/old-1")
	fresh := topoWithEdge("deployment/y/web", "replicaset/y/web-1")
	oldStarted, releaseOld := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	b.rebuildTopology = func() bool {
		if calls.Add(1) == 1 {
			epoch := b.topoEpoch.Load()
			close(oldStarted)
			<-releaseOld
			return b.updateCachedTopology(old, epoch)
		}
		return b.updateCachedTopology(fresh, b.topoEpoch.Load())
	}

	oldReader := make(chan *topology.Topology, 1)
	go func() { oldReader <- b.GetCachedTopology() }()
	<-oldStarted
	waiting := make(chan *topology.Topology, 1)
	go func() { waiting <- b.GetCachedTopology() }()
	time.Sleep(50 * time.Millisecond) // let it join the old build

	b.resetCachedTopology()
	select {
	case got := <-waiting:
		if got != fresh {
			t.Fatalf("waiting reader got %v, want the new cluster's graph", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiting reader kept waiting on the previous cluster's build")
	}
	if got := b.GetCachedTopology(); got != fresh {
		t.Fatalf("new reader got %v, want the new cluster's graph", got)
	}

	close(releaseOld)
	if got := <-oldReader; got != fresh {
		t.Fatalf("old build's reader got %v, want what is cached: the new cluster's graph", got)
	}
	b.cachedTopologyMu.RLock()
	defer b.cachedTopologyMu.RUnlock()
	if b.cachedTopology != fresh || b.cachedTopologyDirty || b.cachedTopologyFlight != nil {
		t.Fatal("the old build's end disturbed the new cluster's cache")
	}
}

// A build that ends just after a switch abandoned it is no answer for the
// new cluster, whichever its waiter sees first.
func TestAReaderOfAnAbandonedBuildThatEndedLooksAgain(t *testing.T) {
	b := NewSSEBroadcaster()
	fresh := topoWithEdge("deployment/y/web", "replicaset/y/web-1")
	f := &topologyFlight{done: make(chan struct{}), abandoned: make(chan struct{})}
	b.cachedTopologyFlight = f
	b.resetCachedTopology()
	close(f.done)
	b.rebuildTopology = func() bool { return b.updateCachedTopology(fresh, b.topoEpoch.Load()) }
	for range 50 {
		b.cachedTopologyMu.Lock()
		b.cachedTopology, b.cachedTopologyFlight = nil, f
		b.cachedTopologyMu.Unlock()
		// The flight is still installed, both its channels ready.
		got := make(chan *topology.Topology, 1)
		go func() { got <- b.GetCachedTopology() }()
		time.Sleep(time.Millisecond)
		b.cachedTopologyMu.Lock()
		if b.cachedTopologyFlight == f {
			b.cachedTopologyFlight = nil
		}
		b.cachedTopologyMu.Unlock()
		if topo := <-got; topo != fresh {
			t.Fatalf("reader got %v, want a build for the new cluster", topo)
		}
	}
}
