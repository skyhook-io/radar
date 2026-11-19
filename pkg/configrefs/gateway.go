package configrefs

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/resourceid"
)

// Gateway API reference defaults: an omitted backendRef group/kind names a core
// Service, an omitted parentRef group/kind names a Gateway, and an omitted
// namespace is the route's own. An explicit group or kind always wins, so a
// mesh parentRef to a Service or a backendRef to a same-named custom kind must
// never be joined to a Gateway or core Service that happens to share the name.

const GatewayAPIGroup = "gateway.networking.k8s.io"

// GatewayBackendIsService reports whether a route backendRef names a core Service.
func GatewayBackendIsService(ref map[string]any) bool {
	group, _ := ref["group"].(string)
	kind, _ := ref["kind"].(string)
	return group == "" && (kind == "" || kind == "Service")
}

// GatewayParentIsGateway reports whether a route parentRef names a Gateway.
func GatewayParentIsGateway(ref map[string]any) bool {
	group, hasGroup := ref["group"].(string)
	kind, _ := ref["kind"].(string)
	return (!hasGroup || group == GatewayAPIGroup) && (kind == "" || kind == "Gateway")
}

// GatewayRefNamespace returns the reference's namespace, defaulting to the route's.
func GatewayRefNamespace(ref map[string]any, routeNamespace string) string {
	if ns, _ := ref["namespace"].(string); ns != "" {
		return ns
	}
	return routeNamespace
}

// GatewayParentRefInSpec reports whether a status.parents[].parentRef still
// names one of the route's spec.parentRefs, by exact reference identity after
// Gateway API defaulting: group, kind, namespace, name, sectionName and port
// must all agree. sectionName and port are part of that identity and are NOT
// wildcarded — an unset field matches only an unset field — so narrowing or
// widening a parentRef's scope retires the status entry recorded for the old
// scope. routeNamespace defaults an omitted namespace on EITHER side, resolved
// independently, so an omitted status namespace never inherits an explicit spec
// namespace. A status entry that matches no spec parentRef is stale and the
// caller should ignore it.
func GatewayParentRefInSpec(statusRef map[string]any, specParentRefs []any, routeNamespace string) bool {
	if len(statusRef) == 0 {
		return false
	}
	for _, sp := range specParentRefs {
		specRef, ok := sp.(map[string]any)
		if !ok {
			continue
		}
		if gatewayParentRefsEqual(statusRef, specRef, routeNamespace) {
			return true
		}
	}
	return false
}

func gatewayParentRefsEqual(a, b map[string]any, routeNamespace string) bool {
	if gatewayRefGroup(a) != gatewayRefGroup(b) {
		return false
	}
	if gatewayRefKind(a) != gatewayRefKind(b) {
		return false
	}
	if GatewayRefNamespace(a, routeNamespace) != GatewayRefNamespace(b, routeNamespace) {
		return false
	}
	an, _ := a["name"].(string)
	bn, _ := b["name"].(string)
	if an != bn {
		return false
	}
	as, _ := a["sectionName"].(string)
	bs, _ := b["sectionName"].(string)
	if as != bs {
		return false
	}
	return gatewayRefPortEqual(a, b)
}

// gatewayRefGroup returns the parentRef's API group, distinguishing an omitted
// group (defaults to the Gateway group) from an explicit empty group (the core
// group) — mirroring GatewayParentIsGateway, and deliberately NOT collapsing ""
// into the Gateway group the way the display-only detector helper does.
func gatewayRefGroup(ref map[string]any) string {
	if g, ok := ref["group"].(string); ok {
		return g
	}
	return GatewayAPIGroup
}

// gatewayRefKind returns the parentRef's kind. Kind defaults to Gateway
// independently of group; an absent or empty kind is Gateway.
func gatewayRefKind(ref map[string]any) string {
	if k, ok := ref["kind"].(string); ok && k != "" {
		return k
	}
	return "Gateway"
}

// gatewayRefPortEqual compares the optional port of two parentRefs. Presence is
// compared separately from value: both absent is equal, one absent is not, and
// a present-but-unparseable port never compares equal (so schema-invalid data
// cannot accidentally match).
func gatewayRefPortEqual(a, b map[string]any) bool {
	av, aPresent := a["port"]
	bv, bPresent := b["port"]
	if aPresent != bPresent {
		return false
	}
	if !aPresent {
		return true
	}
	an, aok := gatewayPortValue(av)
	bn, bok := gatewayPortValue(bv)
	if !aok || !bok {
		return false
	}
	return an == bn
}

func gatewayPortValue(v any) (int64, bool) {
	switch t := v.(type) {
	case int64:
		return t, true
	case int32:
		return int64(t), true
	case int:
		return int64(t), true
	case float64:
		return int64(t), true
	default:
		return 0, false
	}
}

// GatewayRouteKinds are the Gateway API route kinds.
var GatewayRouteKinds = []string{"HTTPRoute", "GRPCRoute", "TCPRoute", "TLSRoute", "UDPRoute"}

// GatewayRouteParents returns the Gateways a route's parentRefs attach it to,
// in their namespaces.
func GatewayRouteParents(route *unstructured.Unstructured) []PathRef {
	var refs []PathRef
	VisitGatewayRouteParents(route, func(ref PathRef) { refs = append(refs, ref) })
	return refs
}

// VisitGatewayRouteParents visits references without retaining a slice.
func VisitGatewayRouteParents(route *unstructured.Unstructured, visit func(PathRef)) {
	parents, _, _ := borrowedSlice(route.Object, "spec", "parentRefs")
	for _, p := range parents {
		m, ok := p.(map[string]any)
		name, _ := m["name"].(string)
		if !ok || name == "" || !GatewayParentIsGateway(m) {
			continue
		}
		visit(PathRef{Ref: resourceid.Ref{Group: GatewayAPIGroup, Kind: "Gateway", Namespace: GatewayRefNamespace(m, route.GetNamespace()), Name: name}, Path: "spec.parentRefs[]"})
	}
}

// GatewayRouteBackendServices returns the Services a route's rules send
// traffic to, in their namespaces. A backend in another namespace is listed
// whether or not a ReferenceGrant permits it.
func GatewayRouteBackendServices(route *unstructured.Unstructured) []PathRef {
	var refs []PathRef
	VisitGatewayRouteBackendServices(route, func(ref PathRef) { refs = append(refs, ref) })
	return refs
}

// VisitGatewayRouteBackendServices visits references without retaining a slice.
func VisitGatewayRouteBackendServices(route *unstructured.Unstructured, visit func(PathRef)) {
	rules, _, _ := borrowedSlice(route.Object, "spec", "rules")
	for _, rule := range rules {
		r, _ := rule.(map[string]any)
		backends, _, _ := borrowedSlice(r, "backendRefs")
		for _, b := range backends {
			m, ok := b.(map[string]any)
			name, _ := m["name"].(string)
			if !ok || name == "" || !GatewayBackendIsService(m) {
				continue
			}
			visit(PathRef{Ref: resourceid.Ref{Kind: "Service", Namespace: GatewayRefNamespace(m, route.GetNamespace()), Name: name}, Path: "spec.rules[].backendRefs[]"})
		}
	}
}

// GatewayClassOf returns the GatewayClass a Gateway names.
func GatewayClassOf(gateway *unstructured.Unstructured) (PathRef, bool) {
	name, _, _ := unstructured.NestedString(gateway.Object, "spec", "gatewayClassName")
	if name == "" {
		return PathRef{}, false
	}
	return PathRef{Ref: resourceid.Ref{Group: GatewayAPIGroup, Kind: "GatewayClass", Name: name}, Path: "spec.gatewayClassName"}, true
}
