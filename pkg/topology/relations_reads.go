package topology

import (
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// allowed reports whether the caller may read r, deciding each kind and
// namespace once per answer, so a failing check costs one review rather than
// one per matching object. As in the families' searches, a hidden related
// object narrows the answer and a check that couldn't be made leaves it
// incomplete; the gap names no namespace.
func (n *neighborhood) allowed(r resourceid.Ref) bool {
	switch n.decide(r) {
	case ownership.Allowed:
		return true
	case ownership.Undecided:
		n.cover("special", relations.Coverage{NotChecked: []ownership.Gap{{Kind: r.GroupKind(), Reason: readreason.UserPermissionCheckFailed}}})
	default:
		n.cover("special", relations.Coverage{Narrowed: true})
	}
	return false
}

// decide is the caller's access to r's kind in r's namespace, asked once per
// answer.
func (n *neighborhood) decide(r resourceid.Ref) ownership.Decision {
	key := decisionKey{r.GroupKind(), r.Namespace}
	d, seen := n.decided[key]
	if !seen {
		d = n.access.CanRead(r.GroupKind(), r.Namespace)
		if n.decided == nil {
			n.decided = map[decisionKey]ownership.Decision{}
		}
		n.decided[key] = d
	}
	return d
}

// namePodNode lists the Node a Pod's spec names when the caller may not read
// Nodes, marked, as the subject's own declared references are: the caller
// reads the name with the Pod. Nothing is read about the Node.
func (n *neighborhood) namePodNode(obj runtime.Object, rel *Relationships) {
	pod, ok := obj.(*corev1.Pod)
	if !ok || pod.Spec.NodeName == "" || rel.Node != nil {
		return
	}
	state := ResourceStateDenied
	switch n.decide(resourceid.NewRef("", "Node", "", pod.Spec.NodeName)) {
	case ownership.Allowed:
		return
	case ownership.Undecided:
		state = ResourceStateUnverified
	}
	rel.Node = &ResourceRef{Kind: "Node", Name: pod.Spec.NodeName, State: state}
}

type decisionKey struct {
	kind      resourceid.GroupKind
	namespace string
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

// objects reads gk in the caller's namespaces without authorizing each
// object: the relationship code checks only what it emits (its allow
// function), so a Node's Pods are checked once they match the Node, not
// every Pod in every namespace first.
func (p *relationDirectProvider) objects(gk resourceid.GroupKind) ([]runtime.Object, error) {
	if read, ok := p.n.seeded[gk]; ok {
		return read.objects, read.err
	}
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
		out = append(out, o.Object)
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

func (n *neighborhood) networkEntrypoints(s relations.Session, emit, traverse ownership.Access, subject resourceid.Ref, obj runtime.Object, istioDomain func() string, rel *Relationships) {
	services := []resourceid.Ref{}
	if subject.GroupKind() == (resourceid.GroupKind{Kind: "Service"}) {
		services = append(services, subject)
	} else {
		v := relations.NewView(s, traverse).WithLimit(0)
		selected := v.SelectedBy(relations.ServiceSelectors, subject, obj)
		selected.Coverage.Total = 0
		n.cover(relations.ServiceSelectors.Name, selected.Coverage)
		n.extras.cover("Entrypoints", selected.Coverage)
		services = selected.Matches
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
		v := relations.NewView(s, intermediateAccess{Access: emit, ref: svc, traverse: traverse}).WithLimit(n.limit)
		for _, family := range []*relations.Family{relations.IngressBackends, relations.GatewayBackends, relations.IstioServiceHostsIn(istioDomain), relations.TraefikBackends, relations.ContourBackends} {
			found := v.Referrers(family, svc, nil)
			n.cover(family.Name, found.Coverage)
			n.extras.cover("Entrypoints", found.Coverage)
			for _, r := range found.Referrers {
				// A DestinationRule configures traffic to the host; the
				// family's own projection files it under ConfigRefs.
				if r.Ref.Kind == "DestinationRule" && r.Ref.Group == "networking.istio.io" {
					continue
				}
				add(r.Ref)
			}
		}

		// Routes can be private hops to a readable Gateway.
		for _, composition := range []struct{ backends, parents *relations.Family }{
			{relations.GatewayBackends, relations.GatewayParents},
			{relations.IstioServiceHostsIn(istioDomain), relations.IstioGateways},
		} {
			routes := relations.NewView(s, traverse).WithLimit(0).Referrers(composition.backends, svc, nil)
			routes.Coverage.Total = 0
			n.cover(composition.parents.Name, routes.Coverage)
			n.extras.cover("Entrypoints", routes.Coverage)
			for _, route := range routes.Referrers {
				parents := relations.NewView(s, intermediateAccess{Access: emit, ref: route.Ref, traverse: traverse}).References(composition.parents, route.Ref, nil)
				n.cover(composition.parents.Name, parents.Coverage)
				n.extras.cover("Entrypoints", parents.Coverage)
				for _, parent := range parents.Declared {
					if parent.State == relations.TargetPresent {
						add(parent.Ref)
					}
				}
			}
		}
	}
}
