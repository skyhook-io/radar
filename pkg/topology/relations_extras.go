package topology

import (
	"fmt"
	"slices"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	"k8s.io/apimachinery/pkg/runtime"
	"math"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// RelationshipExtras is request-local evidence for context readers, never a
// wire payload. Fields uses Relationships field names and retains coverage
// even when no reference was emitted; "Uses" and "ReferencedBy" are the
// coverage of Declarations and Consumers themselves. Declarations are
// everything the subject's spec names, including targets the caller may not
// read; Consumers are what declares the subject as a configuration reference
// and, for a Node, the Pods placed on it.
type RelationshipExtras struct {
	Subject        resourceid.Ref                    `json:"-"`
	Coverage       map[string]relations.Coverage     `json:"-"`
	Fields         map[string]relations.Coverage     `json:"-"`
	Consumers      []relations.Referrer              `json:"-"`
	Declarations   []relations.Declared              `json:"-"`
	ScalerManagers map[resourceid.Ref]resourceid.Ref `json:"-"`
}

// RefusedRelationships is the answer for a subject the caller's access
// refuses by decision, which isn't Allowed: no relationships, and ownership
// unavailable because the caller may not read the subject's kind there, or
// because that couldn't be checked. Its extras say the same of every field
// an answer fills.
func RefusedRelationships(subject resourceid.Ref, decision ownership.Decision) (*Relationships, *RelationshipExtras) {
	rel := &Relationships{}
	reason := readreason.UserForbidden
	c := relations.Coverage{Narrowed: true}
	if decision == ownership.Undecided {
		reason = readreason.UserPermissionCheckFailed
		c = relations.Coverage{NotChecked: []ownership.Gap{{Kind: subject.GroupKind(), Namespace: subject.Namespace, Reason: reason}}}
	}
	OwnershipUnavailable(rel, string(reason))
	return rel, unanswered(c)
}

// UnansweredRelationships are the extras when Radar couldn't answer a
// subject's relationships at all, for reason: every field an answer fills is
// unavailable. A host's nil extras instead means it supplied none.
func UnansweredRelationships(reason readreason.Code) *RelationshipExtras {
	return unanswered(relations.Coverage{NotChecked: []ownership.Gap{{Reason: reason}}})
}

func unanswered(c relations.Coverage) *RelationshipExtras {
	return &RelationshipExtras{Coverage: map[string]relations.Coverage{"answer": c}, Fields: map[string]relations.Coverage{"answer": c}}
}

// declarationCoverage is what reading the subject's own declarations
// couldn't cover: gaps on the subject's kind, such as a spec that couldn't be
// decoded, or the subject withheld. A declaration lists its target whether or
// not Radar holds it, so what was read about the targets says nothing about
// the list.
func declarationCoverage(a relations.ReferencesAnswer, subject resourceid.GroupKind) relations.Coverage {
	c := relations.Coverage{Narrowed: a.Coverage.Narrowed && len(a.Declared) == 0}
	for _, gap := range a.Coverage.NotChecked {
		if gap.Kind == subject {
			c.NotChecked = append(c.NotChecked, gap)
		}
	}
	return c
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
	if e == nil {
		return
	}
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
		for _, field := range d.Rules[i].ReferenceFields {
			if field != "" {
				e.cover(field, relations.Coverage{Narrowed: true})
			}
		}
	}
}

func (e *RelationshipExtras) projectCoverage(d relations.Disposition, subject resourceid.GroupKind, forward bool, c relations.Coverage) {
	if e == nil {
		return
	}
	for _, rule := range d.Rules {
		fields, kind := rule.ReferrerFields, rule.Target
		if forward {
			fields, kind = rule.ReferenceFields, rule.Source
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
			n.extras.cover("ScalerManagers", relations.Coverage{NotChecked: []ownership.Gap{{Kind: manager.GroupKind(), Namespace: manager.Namespace, Reason: readreason.UserPermissionCheckFailed}}})
		}
	}
}

// contextDirectEvidence reads what the context lists of a Node's Pods and a
// volume's or StorageClass's storage, with its coverage. A Node's Pods and a
// StorageClass's volumes and claims are whole inventories, so the answer's
// direct lists take the objects this read matched (seedDirect) rather than
// reading the inventory again, and both describe one read.
func (n *neighborhood) contextDirectEvidence(session relations.Session, v *relations.View, subject resourceid.Ref, obj runtime.Object) {
	v.WithLimit(math.MaxInt)
	if relations.PodPlacement.Covers(subject.GroupKind()) {
		refs := v.References(relations.PodPlacement, subject, obj)
		n.extras.cover("Node", refs.Coverage)
		consumers := v.Referrers(relations.PodPlacement, subject, obj)
		n.extras.Consumers = append(n.extras.Consumers, consumers.Referrers...)
		n.extras.cover("ReferencedBy", consumers.Coverage)
		n.seedDirect(session, consumers, relations.PodPlacement.Sources)
	}
	if relations.Storage.Covers(subject.GroupKind()) {
		refs := v.References(relations.Storage, subject, obj)
		if subject.GroupKind() == (resourceid.GroupKind{Kind: "PersistentVolume"}) {
			// A volume's claim is listed among its dependents, its class
			// among its dependencies.
			claim := resourceid.GroupKind{Kind: "PersistentVolumeClaim"}
			n.extras.cover("Dependents", targetCoverage(refs, func(gk resourceid.GroupKind) bool { return gk == claim }))
			n.extras.cover("Dependencies", targetCoverage(refs, func(gk resourceid.GroupKind) bool { return gk != claim }))
		} else {
			n.extras.cover("Dependencies", refs.Coverage)
		}
		// The direct projection lists the entire inventory, independently of the
		// per-family output cap. Its coverage must describe that same inventory.
		consumers := v.Referrers(relations.Storage, subject, obj)
		n.extras.cover("Dependents", consumers.Coverage)
		if subject.GroupKind() == (resourceid.GroupKind{Group: "storage.k8s.io", Kind: "StorageClass"}) {
			n.seedDirect(session, consumers, relations.Storage.Sources)
		}
	}
}

// directRead is what a direct list reads of one kind's inventory.
type directRead struct {
	objects []runtime.Object
	err     error
}

// seedDirect makes a referrers answer the answer's direct read of each source
// kind: the objects it matched, and an error where it couldn't read the kind.
func (n *neighborhood) seedDirect(session relations.Session, a relations.ReferrersAnswer, sources []relations.Source) {
	if n.seeded == nil {
		n.seeded = map[resourceid.GroupKind]directRead{}
	}
	for _, source := range sources {
		read := directRead{}
		for _, gap := range a.Coverage.NotChecked {
			if gap.Kind == source.Kind || gap.Kind == (resourceid.GroupKind{}) {
				read.err = fmt.Errorf("%s: %s", source.Kind.Kind, gap.Reason)
				n.cover("direct", relations.Coverage{NotChecked: []ownership.Gap{gap}})
				break
			}
		}
		n.seeded[source.Kind] = read
	}
	for _, r := range a.Referrers {
		read, ok := n.seeded[r.Ref.GroupKind()]
		if !ok {
			continue
		}
		if obj, got := session.Borrow(r.Ref); got.State == ownership.Present {
			read.objects = append(read.objects, obj)
			n.seeded[r.Ref.GroupKind()] = read
		}
	}
}

// targetCoverage is the part of a references answer's coverage that concerns
// the targets of the kinds keep selects. Gaps naming no kind concern them all.
func targetCoverage(a relations.ReferencesAnswer, keep func(resourceid.GroupKind) bool) relations.Coverage {
	c := relations.Coverage{SubjectState: a.Coverage.SubjectState, Truncated: a.Coverage.Truncated, Total: a.Coverage.Total}
	for _, gap := range a.Coverage.NotChecked {
		if gap.Kind == (resourceid.GroupKind{}) || keep(gap.Kind) {
			c.NotChecked = append(c.NotChecked, gap)
		}
	}
	c.Narrowed = slices.ContainsFunc(a.Declared, func(d relations.Declared) bool {
		return d.State == relations.TargetWithheld && keep(d.Ref.GroupKind())
	})
	return c
}

// AnswerFields are the Relationships fields, and the RelationshipExtras.Fields
// entries, a context answer about a subject of kind gk can fill: what its
// families, selector families and compositions project for that kind, as
// the answer projects them, and the answer's own reads (its network
// entrypoints, a Pod's Node, storage, configuration references and Pod
// placement, scaler managers, Cluster API health checks and Knative
// Serving's Services). A host can tell from them which fields a kind has.
func AnswerFields(gk resourceid.GroupKind) map[string]bool {
	out := map[string]bool{}
	add := func(d relations.Disposition, forward bool) {
		for _, rule := range d.Rules {
			fields, kind := rule.ReferrerFields, rule.Target
			if forward {
				fields, kind = rule.ReferenceFields, rule.Source
			}
			if kind.Kind != "" && kind != gk {
				continue
			}
			for _, field := range fields {
				if field != "" {
					out[field] = true
				}
			}
		}
	}
	declares := func(sources []relations.Source) bool {
		return slices.ContainsFunc(sources, func(s relations.Source) bool { return s.Kind == gk })
	}
	declared := func(f *relations.Family) bool {
		return f.IsTarget != nil && f.IsTarget(gk) || f.IsTarget == nil && slices.Contains(f.Targets, gk)
	}
	for _, f := range relations.Families("") {
		if declares(f.Sources) {
			add(f.Projection, true)
		}
		if declared(f) {
			add(f.Projection, false)
		}
	}
	for _, f := range relations.SelectorFamilies() {
		if slices.ContainsFunc(f.Selectors, func(s relations.Selector) bool { return s.Kind == gk }) {
			add(f.Projection, true)
		}
		if slices.ContainsFunc(f.Selectables, func(s relations.Selectable) bool { return s.Kind == gk }) {
			add(f.Projection, false)
		}
	}
	for _, f := range relations.Compositions() {
		if declares(f.Producer.Sources) {
			add(f.Projection, true)
		}
		for _, consumer := range f.Consumers {
			if (len(f.ConsumerKinds) == 0 || slices.Contains(f.ConsumerKinds, gk)) && declares(consumer.Sources) {
				add(f.Projection, false)
			}
		}
	}
	set := func(on bool, fields ...string) {
		for _, field := range fields {
			out[field] = out[field] || on
		}
	}
	set(gk == resourceid.GroupKind{Kind: "Service"} || slices.ContainsFunc(relations.ServiceSelectors.Selectables, func(s relations.Selectable) bool { return s.Kind == gk }), "Entrypoints", "Ingresses", "Gateways", "Routes", "RoutedFrom")
	set(declares(relations.ConfigReferences.Sources), "Uses")
	set(declared(relations.ConfigReferences) || declared(relations.PodPlacement), "ReferencedBy")
	set(declares(relations.PodPlacement.Sources), "Node")
	set(declares(relations.Storage.Sources), "Dependencies")
	set(declared(relations.Storage), "Dependents")
	set(out["Scalers"], "ScalerManagers")
	set(gk == resourceid.GroupKind{Group: "cluster.x-k8s.io", Kind: "Cluster"}, "HealthChecks")
	set(gk == resourceid.GroupKind{Group: "cluster.x-k8s.io", Kind: "MachineHealthCheck"}, "Dependencies")
	set(gk == resourceid.GroupKind{Group: "serving.knative.dev", Kind: "Service"} || gk == resourceid.GroupKind{Kind: "Service"}, "Services", "Backends")
	return out
}

// ReadSubject is access reading subject's kind in subject's namespace as
// allowed, for a host that read subject for the caller through a resource
// read gate, which decides every object of a kind in one namespace alike:
// what it allowed for subject, it allows for them. A caller who reads subject
// by namespace access, without a get grant on its kind, then still gets its
// relationships; what they name of other kinds is read as access reads it.
func ReadSubject(access ownership.Access, subject resourceid.Ref) ownership.Access {
	return readSubject{Access: access, subject: subject}
}

type readSubject struct {
	ownership.Access
	subject resourceid.Ref
}

func (a readSubject) CanRead(kind resourceid.GroupKind, namespace string) ownership.Decision {
	if kind == a.subject.GroupKind() && namespace == a.subject.Namespace {
		return ownership.Allowed
	}
	return a.Access.CanRead(kind, namespace)
}
