package topology

import (
	"slices"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	"k8s.io/apimachinery/pkg/runtime"
	"math"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// RelationshipExtras is request-local evidence for context readers, never a
// wire payload. Fields uses Relationships field names and retains coverage
// even when no reference was emitted. Denied declarations are never retained.
type RelationshipExtras struct {
	Subject        resourceid.Ref                    `json:"-"`
	Coverage       map[string]relations.Coverage     `json:"-"`
	Fields         map[string]relations.Coverage     `json:"-"`
	Consumers      []relations.Referrer              `json:"-"`
	Declarations   []relations.Declared              `json:"-"`
	ScalerManagers map[resourceid.Ref]resourceid.Ref `json:"-"`
	// WithheldKinds names, per field, the kinds of the declared targets
	// the caller may not read, so a host can say which part was withheld.
	WithheldKinds map[string][]resourceid.GroupKind `json:"-"`
}

func (e *RelationshipExtras) cover(field string, c relations.Coverage) {
	if e == nil || c.Complete() && !c.Narrowed {
		return
	}
	prior := e.Fields[field]
	for _, gap := range c.NotChecked {
		if !slices.Contains(prior.NotChecked, gap) {
			prior.NotChecked = append(prior.NotChecked, gap)
		}
	}
	if c.SubjectState != "" {
		prior.SubjectState = c.SubjectState
	}
	prior.Narrowed = prior.Narrowed || c.Narrowed
	prior.Truncated = prior.Truncated || c.Truncated
	prior.Total = max(prior.Total, c.Total)
	e.Fields[field] = prior
}

// projectReferences projects what a subject declares onto the fields it
// fills. A withheld target narrows only the field its kind fills, not every
// field the family fills.
func (e *RelationshipExtras) projectReferences(d relations.Disposition, subject resourceid.GroupKind, a relations.ReferencesAnswer) {
	var withheld []resourceid.GroupKind
	for _, t := range a.Declared {
		if t.State == relations.TargetWithheld && !slices.Contains(withheld, t.Ref.GroupKind()) {
			withheld = append(withheld, t.Ref.GroupKind())
		}
	}
	c := a.Coverage
	if len(withheld) == 0 {
		e.projectCoverage(d, subject, true, c)
		return
	}
	c.Narrowed = false
	e.projectCoverage(d, subject, true, c)
	for _, kind := range withheld {
		i := slices.IndexFunc(d.Rules, func(r relations.ProjectionRule) bool { return r.Matches(subject, kind) })
		if i < 0 {
			continue
		}
		for _, field := range d.Rules[i].References {
			if field == "" {
				continue
			}
			e.cover(field, relations.Coverage{Narrowed: true})
			if e.WithheldKinds == nil {
				e.WithheldKinds = map[string][]resourceid.GroupKind{}
			}
			if !slices.Contains(e.WithheldKinds[field], kind) {
				e.WithheldKinds[field] = append(e.WithheldKinds[field], kind)
			}
		}
	}
}

func (e *RelationshipExtras) projectCoverage(d relations.Disposition, subject resourceid.GroupKind, forward bool, c relations.Coverage) {
	for _, rule := range d.Rules {
		fields, kind := rule.Referrers, rule.Target
		if forward {
			fields, kind = rule.References, rule.Source
		}
		if kind.Kind != "" && kind != subject {
			continue
		}
		for _, field := range fields {
			if field != "" {
				e.cover(field, c)
			}
		}
	}
}

func (n *neighborhood) scalerManagers(s relations.Session, rel *Relationships) {
	for _, scaler := range rel.Scalers {
		r := refIdentity(scaler)
		if r.GroupKind() != (resourceid.GroupKind{Group: "autoscaling", Kind: "HorizontalPodAutoscaler"}) {
			continue
		}
		obj, read := n.borrow(s, r)
		hpa, ok := obj.(*autoscalingv2.HorizontalPodAutoscaler)
		if read.State != ownership.Present || !ok {
			continue
		}
		name := ""
		for _, owner := range hpa.OwnerReferences {
			if owner.Kind == "ScaledObject" && resourceid.GroupFromAPIVersion(owner.APIVersion) == "keda.sh" {
				name = owner.Name
				break
			}
		}
		if name == "" {
			name = hpa.Labels["scaledobject.keda.sh/name"]
		}
		if name == "" {
			continue
		}
		manager := resourceid.NewRef("keda.sh", "ScaledObject", r.Namespace, name)
		switch n.access.CanRead(manager.GroupKind(), manager.Namespace) {
		case ownership.Allowed:
			n.extras.ScalerManagers[r] = manager
		case ownership.Denied:
			n.extras.cover("ScalerManagers", relations.Coverage{Narrowed: true})
		default:
			n.extras.cover("ScalerManagers", relations.Coverage{NotChecked: []ownership.Gap{{Kind: manager.GroupKind(), Namespace: manager.Namespace}}})
		}
	}
}

func (n *neighborhood) contextDirectEvidence(v *relations.View, subject resourceid.Ref, obj runtime.Object) {
	v.WithLimit(math.MaxInt)
	if relations.PodPlacement.Covers(subject.GroupKind()) {
		refs := v.References(relations.PodPlacement, subject, obj)
		n.extras.cover("Node", refs.Coverage)
		consumers := v.Referrers(relations.PodPlacement, subject, obj)
		n.extras.Consumers = append(n.extras.Consumers, consumers.Referrers...)
		n.extras.cover("Consumers", consumers.Coverage)
	}
	if relations.Storage.Covers(subject.GroupKind()) {
		refs := v.References(relations.Storage, subject, obj)
		field := "Dependencies"
		if subject.Kind == "PersistentVolume" {
			field = "Dependents"
		}
		n.extras.cover(field, refs.Coverage)
		n.extras.cover("Dependencies", refs.Coverage)
		// The direct projection lists the entire inventory, independently of the
		// per-family output cap. Its coverage must describe that same inventory.
		consumers := v.Referrers(relations.Storage, subject, obj)
		n.extras.cover("Dependents", consumers.Coverage)
	}
}
