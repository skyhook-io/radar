package relations

import (
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// Composition joins a producer's declarations to consumers' declarations of
// the same logical coordinates. The intermediate object is never read: the
// evidence is in the two readable endpoints, even before a Secret is created.
type Composition struct {
	Name          string
	Projection    Disposition
	Producer      *Family
	Consumers     []*Family
	ConsumerKinds []resourceid.GroupKind
}

// CertificateRoutes links Certificates to Traefik routes and Contour proxies
// that name their output Secret, as the display graph does.
var CertificateRoutes = &Composition{
	Name:          "certificateRoutes",
	Projection:    project("configures", false, "Consumers", "ConfigRefs"),
	Producer:      CertificateSecrets,
	Consumers:     []*Family{TraefikSecrets, ContourSecrets},
	ConsumerKinds: append(traefikKinds("IngressRoute", "IngressRouteTCP", "IngressRouteUDP"), kindHTTPProxy),
}

func (f *Composition) consumer(gk resourceid.GroupKind) bool {
	return len(f.ConsumerKinds) == 0 || slices.Contains(f.ConsumerKinds, gk)
}

// ComposedEdge is a producer and consumer joined by their declarations.
type ComposedEdge struct{ Producer, Consumer resourceid.Ref }

// CompositionAnswer lists joined endpoints and the readable scope not checked.
type CompositionAnswer struct {
	Edges    []ComposedEdge
	Coverage Coverage
}

// Composed answers both directions without Referrers' held-target condition.
// It authorizes endpoints before inspecting their declarations, and neither
// authorizes nor observes the object at the logical join coordinates.
func (v *View) Composed(f *Composition, subject resourceid.Ref, fetched runtime.Object) CompositionAnswer {
	var a CompositionAnswer
	producer := f.Producer.source(subject.GroupKind()) >= 0
	consumer := false
	for _, c := range f.Consumers {
		consumer = consumer || f.consumer(subject.GroupKind()) && c.source(subject.GroupKind()) >= 0
	}
	if !producer && !consumer {
		return a
	}
	obj, held := v.heldSubject(subject, fetched, &a.Coverage)
	if !held {
		return a
	}
	declarations := func(family *Family, o IndexedObject) []resourceid.Ref {
		var refs []resourceid.Ref
		err := family.Declared(o.Object, func(d Declaration) {
			if d.Target.GroupKind() == kindSecret && !slices.Contains(refs, d.Target) {
				refs = append(refs, d.Target)
			}
		})
		if err != nil {
			a.Coverage.addGap(ownership.Gap{Kind: o.Ref.GroupKind(), Namespace: o.Ref.Namespace, Reason: readreason.ReadError})
		}
		return refs
	}
	add := func(p, c resourceid.Ref) {
		e := ComposedEdge{p, c}
		if !slices.Contains(a.Edges, e) {
			a.Edges = append(a.Edges, e)
		}
	}
	self := IndexedObject{Ref: subject, Object: obj}
	if producer {
		keys := declarations(f.Producer, self)
		for _, c := range f.Consumers {
			if len(keys) == 0 {
				continue
			}
			search := v.newSearch(subject.Namespace, c.CrossNamespace, &a.Coverage)
			for _, source := range c.Sources {
				if !f.consumer(source.Kind) {
					continue
				}
				search.each(source.Kind, &a.Coverage, func(o IndexedObject) {
					for _, key := range declarations(c, o) {
						if slices.Contains(keys, key) {
							add(subject, o.Ref)
							break
						}
					}
				})
			}
		}
	}
	if consumer {
		for _, c := range f.Consumers {
			if c.source(subject.GroupKind()) < 0 {
				continue
			}
			keys := declarations(c, self)
			var namespaces []string
			for _, key := range keys {
				namespaces = append(namespaces, key.Namespace)
			}
			if len(namespaces) == 0 {
				continue
			}
			search := v.newSearchIn(namespaces, &a.Coverage)
			for _, source := range f.Producer.Sources {
				search.each(source.Kind, &a.Coverage, func(o IndexedObject) {
					for _, key := range declarations(f.Producer, o) {
						if slices.Contains(keys, key) {
							add(o.Ref, subject)
							break
						}
					}
				})
			}
		}
	}
	slices.SortFunc(a.Edges, func(a, b ComposedEdge) int {
		if n := strings.Compare(a.Producer.Key(), b.Producer.Key()); n != 0 {
			return n
		}
		return strings.Compare(a.Consumer.Key(), b.Consumer.Key())
	})
	if v.limit > 0 && len(a.Edges) > v.limit {
		a.Coverage.Truncated = true
		a.Coverage.Total = len(a.Edges)
		a.Edges = a.Edges[:v.limit]
	}
	return a
}

// SealedSecretConsumers joins the SealedSecret's own namespace/name to
// consumers declaring that Secret, including when unsealing has not succeeded.
var SealedSecretConsumers = &Composition{
	Name:       "sealedSecretConsumers",
	Projection: project("configures", false, "Consumers", "ConfigRefs"),
	Producer:   SealedSecrets,
	Consumers:  []*Family{ConfigReferences},
}

// Compositions are the explicit joins over logical coordinates.
func Compositions() []*Composition {
	return []*Composition{CertificateRoutes, SealedSecretConsumers}
}
