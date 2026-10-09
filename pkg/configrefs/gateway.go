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
