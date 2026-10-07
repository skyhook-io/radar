package topology

import (
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// addObservedOwnerEdges closes metadata ownership across already observed nodes.
// It performs no provider reads and creates neither missing owners nor children.
func addObservedOwnerEdges(nodes []Node, edges []Edge) []Edge {
	index := IndexByResource(&Topology{Nodes: nodes})
	type pair struct{ source, target string }
	existing := map[pair][]int{}
	for i, edge := range edges {
		if edge.Type == EdgeManages {
			key := pair{edge.Source, edge.Target}
			existing[key] = append(existing[key], i)
		}
	}
	contradicted := map[pair]bool{}
	for i := range nodes {
		child := &nodes[i]
		namespace, _ := child.Data["namespace"].(string)
		for _, owner := range child.ownerReferences {
			if owner.APIVersion == "" || owner.Kind == "" || owner.Name == "" {
				continue
			}
			parent, matches := index.ResolveObservedOwner(resourceid.OwnerReference(owner.APIVersion, owner.Kind, owner.Name, string(owner.UID), namespace))
			if parent == nil || !parent.observed && parent.uid == "" {
				continue
			}
			if parent.ID == child.ID {
				contradicted[pair{parent.ID, child.ID}] = true
				continue
			}
			key := pair{parent.ID, child.ID}
			if !matches {
				contradicted[key] = true
				continue
			}
			controller := owner.Controller != nil && *owner.Controller
			if positions := existing[key]; len(positions) > 0 {
				for _, position := range positions {
					edges[position].OwnerController = &controller
				}
				continue
			}
			edges = append(edges, Edge{ID: parent.ID + "-to-" + child.ID + "-owner", Source: parent.ID, Target: child.ID, Type: EdgeManages, OwnerController: &controller})
			existing[key] = []int{len(edges) - 1}
		}
	}
	if len(contradicted) > 0 {
		kept := edges[:0]
		for _, edge := range edges {
			if edge.Type != EdgeManages || !contradicted[pair{edge.Source, edge.Target}] {
				kept = append(kept, edge)
			}
		}
		edges = kept
	}
	return edges
}

// preferredOwnerEdge preserves the old ordering for unclassified relationships,
// but a metadata controller takes precedence over a non-controller owner.
func preferredOwnerEdge(edges []Edge) *Edge {
	var first *Edge
	for i := range edges {
		edge := &edges[i]
		if edge.Type != EdgeManages {
			continue
		}
		if edge.OwnerController != nil && *edge.OwnerController {
			return edge
		}
		if first == nil {
			first = edge
		}
	}
	return first
}
