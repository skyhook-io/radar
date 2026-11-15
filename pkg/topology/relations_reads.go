package topology

import (
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

func (n *neighborhood) allowed(r resourceid.Ref) bool {
	return n.access.CanRead(r.GroupKind(), r.Namespace) == ownership.Allowed
}

func (n *neighborhood) allowedRef(r ResourceRef) bool { return n.allowed(refIdentity(r)) }

func (n *neighborhood) borrow(s relations.Session, r resourceid.Ref) (runtime.Object, ownership.ObjectRead) {
	if !n.allowed(r) {
		return nil, ownership.ObjectRead{}
	}
	obj, read := s.Borrow(r)
	if read.State == ownership.Unavailable {
		n.cover("special", relations.Coverage{NotChecked: []ownership.Gap{read.Gap}})
	}
	return obj, read
}

func (n *neighborhood) present(s relations.Session, r resourceid.Ref) bool {
	_, read := n.borrow(s, r)
	return read.State == ownership.Present
}

// The projection's direct lists use the same session and readable scopes as
// families; no raw provider list can reintroduce a hidden object.
type relationDirectProvider struct {
	ResourceProvider
	session relations.Session
	n       *neighborhood
}

func (p *relationDirectProvider) objects(gk resourceid.GroupKind) ([]runtime.Object, error) {
	namespaces, all := p.n.access.Namespaces()
	scope := ownership.SearchScope{Namespaces: namespaces, AllNamespaces: all, IncludeClusterScoped: true}
	index, keys := "", []string(nil)
	if !all {
		index, keys = cache.NamespaceIndex, append(slices.Clone(namespaces), "")
	}
	if p.n.access.CanRead(gk, "") == ownership.Denied {
		return nil, nil
	}
	read := p.session.Indexed(gk, index, keys, scope)
	var out []runtime.Object
	for _, o := range read.Objects {
		if p.n.allowed(o.Ref) {
			out = append(out, o.Object)
		}
	}
	for _, gap := range read.Gaps {
		if ownership.GapCoversReadable(p.n.access, p.session.Catalog(), gap, scope) {
			p.n.cover("direct", relations.Coverage{NotChecked: []ownership.Gap{gap}})
			return out, fmt.Errorf("%s: %s", gk.Kind, gap.Reason)
		}
	}
	return out, nil
}

func (p *relationDirectProvider) Pods() ([]*corev1.Pod, error) {
	objs, err := p.objects(resourceid.GroupKind{Kind: "Pod"})
	var out []*corev1.Pod
	for _, o := range objs {
		if pod, ok := o.(*corev1.Pod); ok {
			out = append(out, pod)
		}
	}
	return out, err
}
func (p *relationDirectProvider) PersistentVolumeClaims() ([]*corev1.PersistentVolumeClaim, error) {
	objs, err := p.objects(resourceid.GroupKind{Kind: "PersistentVolumeClaim"})
	var out []*corev1.PersistentVolumeClaim
	for _, o := range objs {
		if pvc, ok := o.(*corev1.PersistentVolumeClaim); ok {
			out = append(out, pvc)
		}
	}
	return out, err
}
func (p *relationDirectProvider) PersistentVolumes() ([]*corev1.PersistentVolume, error) {
	objs, err := p.objects(resourceid.GroupKind{Kind: "PersistentVolume"})
	var out []*corev1.PersistentVolume
	for _, o := range objs {
		if pv, ok := o.(*corev1.PersistentVolume); ok {
			out = append(out, pv)
		}
	}
	return out, err
}

// Only the named intermediate uses Traverse; every result uses Emit.
type intermediateAccess struct {
	ownership.Access
	ref      resourceid.Ref
	traverse ownership.Access
}

func (a intermediateAccess) CanRead(gk resourceid.GroupKind, ns string) ownership.Decision {
	if gk == a.ref.GroupKind() && ns == a.ref.Namespace {
		return a.traverse.CanRead(gk, ns)
	}
	return a.Access.CanRead(gk, ns)
}

func (n *neighborhood) networkEntrypoints(s relations.Session, emit, traverse ownership.Access, subject resourceid.Ref, obj runtime.Object, istioDomain string, rel *Relationships) {
	services := []resourceid.Ref{}
	if subject.GroupKind() == (resourceid.GroupKind{Kind: "Service"}) {
		services = append(services, subject)
	} else {
		v := relations.NewView(s, traverse)
		services = v.SelectedBy(relations.ServiceSelectors, subject, obj).Matches
	}
	add := func(r resourceid.Ref) {
		if !n.allowed(r) {
			return
		}
		ref := refOf(r)
		switch {
		case r.Group == "networking.k8s.io" && r.Kind == "Ingress":
			rel.Ingresses = appendResourceRef(rel.Ingresses, ref)
		case r.Kind == "Gateway" && (r.Group == "gateway.networking.k8s.io" || r.Group == "networking.istio.io"):
			rel.Gateways = appendResourceRef(rel.Gateways, ref)
		case isServiceEntrypointRoute(&ref):
			rel.Routes = appendResourceRef(rel.Routes, ref)
		default:
			rel.RoutedFrom = appendResourceRef(rel.RoutedFrom, ref)
		}
	}
	for _, svc := range services {
		if traverse.CanRead(svc.GroupKind(), svc.Namespace) != ownership.Allowed {
			continue
		}
		v := relations.NewView(s, intermediateAccess{Access: emit, ref: svc, traverse: traverse})
		for _, family := range []*relations.Family{relations.IngressBackends, relations.GatewayBackends, relations.IstioServiceHosts(istioDomain), relations.TraefikBackends, relations.ContourBackends} {
			found := v.Referrers(family, svc, nil)
			n.cover(family.Name, found.Coverage)
			for _, r := range found.Referrers {
				add(r.Ref)
			}
		}

		// Routes can be private hops to a readable Gateway.
		for _, composition := range []struct{ backends, parents *relations.Family }{
			{relations.GatewayBackends, relations.GatewayParents},
			{relations.IstioServiceHosts(istioDomain), relations.IstioGateways},
		} {
			routes := relations.NewView(s, traverse).Referrers(composition.backends, svc, nil)
			for _, route := range routes.Referrers {
				parents := relations.NewView(s, intermediateAccess{Access: emit, ref: route.Ref, traverse: traverse}).References(composition.parents, route.Ref, nil)
				n.cover(composition.parents.Name, parents.Coverage)
				for _, parent := range parents.Declared {
					if parent.State == relations.TargetPresent {
						add(parent.Ref)
					}
				}
			}
		}
	}
}
