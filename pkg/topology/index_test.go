package topology

import (
	"reflect"
	"testing"
)

// TestIndexByResource_EquivalentToLinearScan pins the contract that the
// inverted index returns the same set of edges as an inline linear scan over
// topo.Edges. This is the load-bearing invariant for the perf path: T6 and
// T12 will pass the index instead of nil, and any divergence between the two
// paths would surface as wrong relationships.
func TestIndexByResource_EquivalentToLinearScan(t *testing.T) {
	topo := buildSyntheticTopology(500)
	idx := IndexByResource(topo)

	for _, n := range topo.Nodes {
		incIdx, outIdx := idx.EdgesFor(n.ID)
		var incLin, outLin []Edge
		for _, e := range topo.Edges {
			if e.Source == n.ID {
				outLin = append(outLin, e)
			}
			if e.Target == n.ID {
				incLin = append(incLin, e)
			}
		}
		if !edgesEqual(incIdx, incLin) {
			t.Errorf("node %s: incoming edges differ; index=%v linear=%v", n.ID, incIdx, incLin)
		}
		if !edgesEqual(outIdx, outLin) {
			t.Errorf("node %s: outgoing edges differ; index=%v linear=%v", n.ID, outIdx, outLin)
		}
	}
}

func TestIndexByResource_NilTopologySafe(t *testing.T) {
	idx := IndexByResource(nil)
	if idx == nil {
		t.Fatal("IndexByResource(nil) returned nil; want non-nil empty index")
	}
	inc, out := idx.EdgesFor("anything")
	if inc != nil || out != nil {
		t.Errorf("EdgesFor on empty index should return nil/nil, got inc=%v out=%v", inc, out)
	}
}

func TestRelationshipsIndex_NilReceiver(t *testing.T) {
	var idx *RelationshipsIndex
	inc, out := idx.EdgesFor("any")
	if inc != nil || out != nil {
		t.Errorf("nil receiver EdgesFor should return nil/nil, got inc=%v out=%v", inc, out)
	}
}

// TestGetRelationshipsWithIndex_MatchesUnindexed pins the back-compat
// contract: passing a non-nil index must yield identical output to the inline
// scan. If this drifts, T6/T12 callers (using the index) will silently see
// different relationships than REST users (using GetRelationships).
func TestGetRelationshipsWithIndex_MatchesUnindexed(t *testing.T) {
	topo := buildSyntheticTopology(500)
	idx := IndexByResource(topo)

	cases := []struct {
		kind, ns, name string
	}{
		{"Deployment", "ns-0", "app-0"},
		{"Pod", "ns-0", "app-0-pod-0"},
		{"Service", "ns-0", "app-0-svc"},
		{"ConfigMap", "ns-0", "app-0-config"},
		{"PodDisruptionBudget", "ns-0", "app-0-pdb"},
	}

	for _, c := range cases {
		t.Run(c.kind, func(t *testing.T) {
			plain := GetRelationships(c.kind, c.ns, c.name, topo, nil, nil)
			indexed := GetRelationshipsWithIndex(c.kind, c.ns, c.name, topo, nil, nil, idx)
			if !reflect.DeepEqual(plain, indexed) {
				t.Errorf("%s/%s/%s: indexed result differs from unindexed\n plain:   %+v\n indexed: %+v",
					c.kind, c.ns, c.name, plain, indexed)
			}
		})
	}
}

// edgesEqual treats slice ordering as significant — IndexByResource preserves
// the original iteration order over topo.Edges, so an order-sensitive
// comparison catches accidental reordering that would alter "first wins"
// semantics in GetRelationships (e.g. rel.Owner).
func edgesEqual(a, b []Edge) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
