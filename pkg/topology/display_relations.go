package topology

import (
	"fmt"
	"log"

	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"k8s.io/apimachinery/pkg/runtime"
)

// declaring is an object a relation family reads declarations from, with the
// identity its node is drawn under. Typed informer objects carry no kind, so
// the ref is given, not derived.
type declaring struct {
	ref resourceid.Ref
	obj runtime.Object
}

// drawnNodes resolves a resource identity to the display node drawn for it.
// A node's identity costs an API-group resolution, so nodes are only grouped
// by kind as they are drawn, and a kind's identities are worked out when a
// family first looks one up; most kinds never are.
type drawnNodes struct {
	nodes   []Node
	indexed int
	pending map[string][]int // Kubernetes kind -> drawn nodes not yet keyed
	byKey   map[string]string
}

func newDrawnNodes() *drawnNodes {
	return &drawnNodes{pending: map[string][]int{}, byKey: map[string]string{}}
}

func (d *drawnNodes) update(nodes []Node) {
	for i := d.indexed; i < len(nodes); i++ {
		kind := KubernetesKindForNode(&nodes[i])
		d.pending[kind] = append(d.pending[kind], i)
	}
	d.nodes, d.indexed = nodes, len(nodes)
}

func (d *drawnNodes) id(r resourceid.Ref) (string, bool) {
	if pending, ok := d.pending[r.Kind]; ok {
		for _, i := range pending {
			for _, key := range nodeResourceKeys(&d.nodes[i]) {
				if _, ok := d.byKey[key]; !ok {
					d.byKey[key] = d.nodes[i].ID
				}
			}
		}
		delete(d.pending, r.Kind)
	}
	id, ok := d.byKey[r.Key()]
	return id, ok
}

// drawFamilies appends the edges families answer between nodes already drawn:
// each source's declared targets, typed and directed as the family's
// Projection says, under the builder's "<source>-to-<target>" edge IDs, source
// by source and, within a source, family by family. A source or target with
// no drawn node draws nothing, which keeps the display's namespace filter and
// include options in force.
func drawFamilies(edges []Edge, drawn *drawnNodes, nodes []Node, sources []declaring, families ...*relations.Family) []Edge {
	drawn.update(nodes)
	seen := map[string]bool{}
	for _, s := range sources {
		from, ok := drawn.id(s.ref)
		if !ok {
			continue
		}
		for _, f := range families {
			edges = drawDeclared(edges, drawn, f, s, from, seen)
		}
	}
	return edges
}

// drawDeclared draws what s, drawn as from, declares under f.
func drawDeclared(edges []Edge, drawn *drawnNodes, f *relations.Family, s declaring, from string, seen map[string]bool) []Edge {
	targets, err := f.DeclaredTargets(s.ref.GroupKind(), s.obj)
	if err != nil {
		log.Printf("WARNING [topology] %s: reading %s: %v", f.Name, s.ref.Key(), err)
	}
	for _, t := range targets {
		to, ok := drawn.id(t)
		if !ok || to == from {
			continue
		}
		e, ok := projectedEdge(f.Projection, s.ref, t)
		if !ok {
			continue
		}
		source, target := from, to
		if e.reverse {
			source, target = to, from
		}
		id := fmt.Sprintf("%s-to-%s", source, target)
		if seen[id] {
			continue
		}
		seen[id] = true
		edges = append(edges, Edge{ID: id, Source: source, Target: target, Type: e.edge, Label: e.label, Partial: e.partial})
	}
	return edges
}
