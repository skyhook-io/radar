package topology

import "testing"

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

// edgesEqual treats slice ordering as significant — IndexByResource preserves
// the original iteration order over topo.Edges, so an order-sensitive
// comparison catches accidental reordering that would alter "first wins"
// semantics in graphRelationships (e.g. rel.Owner).
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
