package configrefs

// Gateway API reference defaults: an omitted backendRef group/kind names a core
// Service, an omitted parentRef group/kind names a Gateway, and an omitted
// namespace is the route's own. An explicit group or kind always wins, so a
// mesh parentRef to a Service or a backendRef to a same-named custom kind must
// never be joined to a Gateway or core Service that happens to share the name.

const gatewayAPIGroup = "gateway.networking.k8s.io"

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
	return (!hasGroup || group == gatewayAPIGroup) && (kind == "" || kind == "Gateway")
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
// namespace. A status entry that matches no spec parentRef exactly is stale,
// unless GatewayParentRefExpandsSpec accepts it as a listener-scoped report for
// an unscoped spec parentRef.
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

// GatewayParentRefExpandsSpec reports whether a status.parents[].parentRef is a
// listener-scoped report (sectionName or port set) for a Gateway that one of the
// route's spec.parentRefs names without a sectionName or port. NGINX Gateway
// Fabric writes the listener into status this way when the Gateway has a single
// listener, so the exact match in GatewayParentRefInSpec misses its reports.
//
// This is identity only. Widening a scoped parentRef to an unscoped one leaves
// the old scoped report behind in the same shape, so the caller must also
// require the report's conditions to describe the route's current generation.
func GatewayParentRefExpandsSpec(statusRef map[string]any, specParentRefs []any, routeNamespace string) bool {
	if !gatewayRefScoped(statusRef) || !GatewayParentIsGateway(statusRef) {
		return false
	}
	for _, sp := range specParentRefs {
		specRef, ok := sp.(map[string]any)
		if !ok || gatewayRefScoped(specRef) {
			continue
		}
		if gatewaySameParent(statusRef, specRef, routeNamespace) {
			return true
		}
	}
	return false
}

func gatewayRefScoped(ref map[string]any) bool {
	section, _ := ref["sectionName"].(string)
	_, hasPort := ref["port"]
	return section != "" || hasPort
}

// gatewaySameParent compares the parent object two parentRefs name, ignoring
// the listener scope (sectionName and port).
func gatewaySameParent(a, b map[string]any, routeNamespace string) bool {
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
	return an == bn
}

func gatewayParentRefsEqual(a, b map[string]any, routeNamespace string) bool {
	if !gatewaySameParent(a, b, routeNamespace) {
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
	return gatewayAPIGroup
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
