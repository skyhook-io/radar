package relations

import (
	"strings"

	"github.com/skyhook-io/radar/pkg/resourceid"
)

// Disposition declares how a family contributes to topology.Relationships.
// Rules use declaration direction (or selector to selected); DirectRead and
// NotProjected explain why a family has no projected edge. Management fields
// describe residual non-owner links; actual ownership is answered separately.
type Disposition struct {
	Rules        []ProjectionRule
	DirectRead   string
	NotProjected string
}

// ProjectionRule records the graph encoding and the resulting fields at both
// endpoints. A nonempty Source or Target Kind narrows a rule; first match wins.
// Dotted fields name nested Relationships fields, such as Reflection.Source.
type ProjectionRule struct {
	Source, Target        resourceid.GroupKind
	Edge                  string
	Reverse               bool
	Label                 string
	Partial               bool
	References, Referrers []string
}

// Matches reports whether the rule applies to these declaring/declared kinds.
func (r ProjectionRule) Matches(source, target resourceid.GroupKind) bool {
	return (r.Source.Kind == "" || r.Source == source) && (r.Target.Kind == "" || r.Target == target)
}

func project(edge string, reverse bool, references, referrers string) Disposition {
	return Disposition{Rules: []ProjectionRule{{Edge: edge, Reverse: reverse, References: strings.Split(references, ","), Referrers: strings.Split(referrers, ",")}}}
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

func (d Disposition) partial() Disposition {
	for i := range d.Rules {
		d.Rules[i].Partial = true
	}
	return d
}
