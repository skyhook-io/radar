package relations

import (
	"strings"

	"github.com/skyhook-io/radar/pkg/resourceid"
)

// Disposition declares how a family contributes to topology.Relationships.
// Rules use declaration direction (or selector to selected); DirectRead
// explains why a family has no projected edge. Management fields
// describe residual non-owner links; actual ownership is answered separately.
type Disposition struct {
	Rules      []ProjectionRule
	DirectRead string
}

// ProjectionRule records the graph encoding and the resulting fields at both
// endpoints. A nonempty Source or Target Kind narrows a rule; first match wins.
type ProjectionRule struct {
	Source, Target resourceid.GroupKind
	// Edge is the Topology edge type.
	Edge string
	// Reverse draws the edge from the declared object to the declaring one,
	// as a HelmRelease's manages edge points at the workload whose labels
	// name it.
	Reverse bool
	Label   string
	// Staged marks the edge as a preview of a policy that doesn't enforce
	// yet, as a Calico staged policy's is; it is the edge's partial flag.
	Staged bool
	// ReferenceFields are the topology.Relationships fields the declared
	// object fills on the declaring object's answer, and ReferrerFields the
	// fields the declaring object fills on the declared one's. Dotted
	// names are nested fields, such as Reflection.Source.
	ReferenceFields, ReferrerFields []string
}

// Matches reports whether the rule applies to these declaring/declared kinds.
func (r ProjectionRule) Matches(source, target resourceid.GroupKind) bool {
	return (r.Source.Kind == "" || r.Source == source) && (r.Target.Kind == "" || r.Target == target)
}

func project(edge string, reverse bool, references, referrers string) Disposition {
	return Disposition{Rules: []ProjectionRule{{Edge: edge, Reverse: reverse, ReferenceFields: strings.Split(references, ","), ReferrerFields: strings.Split(referrers, ",")}}}
}
func (d Disposition) whenSource(source resourceid.GroupKind, edge string, reverse bool, references, referrers string) Disposition {
	r := project(edge, reverse, references, referrers).Rules[0]
	r.Source = source
	d.Rules = append([]ProjectionRule{r}, d.Rules...)
	return d
}
func (d Disposition) whenTarget(target resourceid.GroupKind, edge string, reverse bool, references, referrers string) Disposition {
	r := project(edge, reverse, references, referrers).Rules[0]
	r.Target = target
	d.Rules = append([]ProjectionRule{r}, d.Rules...)
	return d
}
func (d Disposition) labelled(label string) Disposition {
	for i := range d.Rules {
		d.Rules[i].Label = label
	}
	return d
}

func (d Disposition) staged() Disposition {
	for i := range d.Rules {
		d.Rules[i].Staged = true
	}
	return d
}
