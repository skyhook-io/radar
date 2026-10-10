package topology

import "github.com/skyhook-io/radar/pkg/resourceid"

// addObservedOwnerEdges closes metadata ownership across already observed nodes.
// It performs no provider reads and creates neither missing owners nor children.
func addObservedOwnerEdges(nodes []Node, edges []Edge) []Edge {
	index := IndexByResource(&Topology{Nodes: nodes})
	type pair struct{ source, target string }
	valid := map[pair]bool{}
	for i := range nodes {
		child := &nodes[i]
		namespace, _ := child.Data["namespace"].(string)
		for _, owner := range child.ownerReferences {
			if owner.APIVersion == "" || owner.Kind == "" || owner.Name == "" {
				continue
			}
			parent, matches := index.ResolveObservedOwner(resourceid.OwnerReference(owner.APIVersion, owner.Kind, owner.Name, string(owner.UID), namespace))
			if parent == nil || !matches || !parent.observed && parent.uid == "" || parent.ID == child.ID {
				continue
			}
			key := pair{parent.ID, child.ID}
			valid[key] = valid[key] || owner.Controller != nil && *owner.Controller
		}
	}
	existing := map[pair]bool{}
	kept := edges[:0]
	for _, edge := range edges {
		if edge.Type == EdgeManages {
			key := pair{edge.Source, edge.Target}
			controller, matches := valid[key]
			child := index.nodesByID[edge.Target]
			// Synthetic PodGroups have no single object owner incarnation. Their
			// aggregate paths are retained, as are independent declarations/labels.
			if edge.metadataOwner && child != nil && (child.observed || child.uid != "") && !matches {
				continue
			}
			edge.OwnerController = nil
			if matches {
				edge.OwnerController = &controller
			}
			existing[key] = true
		}
		kept = append(kept, edge)
	}
	for i := range nodes {
		child := &nodes[i]
		namespace, _ := child.Data["namespace"].(string)
		for _, owner := range child.ownerReferences {
			parent, matches := index.ResolveObservedOwner(resourceid.OwnerReference(owner.APIVersion, owner.Kind, owner.Name, string(owner.UID), namespace))
			if parent == nil || !matches {
				continue
			}
			key := pair{parent.ID, child.ID}
			controller, validOwner := valid[key]
			if !validOwner || existing[key] {
				continue
			}
			kept = append(kept, Edge{metadataOwner: true, ID: parent.ID + "-to-" + child.ID + "-owner", Source: parent.ID, Target: child.ID, Type: EdgeManages, OwnerController: &controller})
			existing[key] = true
		}
	}
	return kept
}

// verifiedOwner reports whether edge is backed by an observed owner
// reference. Other manages edges (GitOps inventory, class bindings, label
// inference, display shortcuts) are management, not ownership, and Kubernetes
// garbage collection never follows them.
func (e Edge) verifiedOwner() bool {
	return e.Type == EdgeManages && e.OwnerController != nil
}

// preferredOwnerEdge chooses the observed controller owner, otherwise the
// first observed non-controller owner in graph order.
func preferredOwnerEdge(edges []Edge) *Edge {
	var first *Edge
	for i := range edges {
		edge := &edges[i]
		if !edge.verifiedOwner() {
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
