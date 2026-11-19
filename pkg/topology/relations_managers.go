package topology

import (
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"k8s.io/apimachinery/pkg/runtime"
)

// RelationSpecGenerators answers only observed spec generators. It searches
// the managing families rather than building unrelated routing and policy data.
func RelationSpecGenerators(session relations.Session, access ownership.Access, v *ownership.View, subject resourceid.Ref, obj runtime.Object) ([]ResourceRef, bool) {
	n := &neighborhood{nodes: map[resourceid.Ref]bool{}, edges: map[string]bool{}, coverage: map[string]relations.Coverage{}, access: access}
	n.node(subject)
	rv := relations.NewView(session, access)
	incomplete := false
	for _, family := range relations.Families("") {
		e, ok := familyEdges[family.Name]
		if !ok || !family.Covers(subject.GroupKind()) {
			continue
		}
		for _, rule := range e.Rules {
			if rule.Edge != string(EdgeManages) {
				continue
			}
			edge := relationEdge{edge: EdgeManages, reverse: rule.Reverse}
			if rule.Reverse {
				answer := rv.References(family, subject, obj)
				incomplete = incomplete || !answer.Coverage.Complete()
				for _, declared := range answer.Declared {
					if declared.State == relations.TargetPresent {
						if rule.Matches(subject.GroupKind(), declared.Ref.GroupKind()) {
							n.edge(subject, declared.Ref, edge)
						}
					}
				}
			} else {
				answer := rv.Referrers(family, subject, obj)
				incomplete = incomplete || !answer.Coverage.Complete()
				for _, referrer := range answer.Referrers {
					if rule.Matches(referrer.Ref.GroupKind(), subject.GroupKind()) {
						n.edge(referrer.Ref, subject, edge)
					}
				}
			}
		}
	}
	n.crossplane(session, v, subject, obj)
	if pool, ok := n.nodePool(session, subject); ok {
		n.edge(pool, subject, relationEdge{edge: EdgeManages})
	}
	var sources []ResourceRef
	for _, edge := range n.topo.Edges {
		if edge.Target == familyNodeID(subject) {
			for ref := range n.nodes {
				if familyNodeID(ref) == edge.Source {
					sources = append(sources, refOf(ref))
				}
			}
		}
	}
	out, unchecked, _ := SpecGenerators(v, subject, sources)
	return out, incomplete || unchecked || len(n.coverage) > 0
}
