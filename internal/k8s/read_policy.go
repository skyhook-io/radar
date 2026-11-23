package k8s

import (
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"

	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
)

// RequiresExactNamespaceAccess reports whether namespace visibility alone is
// insufficient to read the resolved resource. Trivy findings can quote secrets,
// a MetalLB BGPPeer can hold its session password inline, and a kagent
// RemoteMCPServer or ModelConfig can hold request headers inline.
func RequiresExactNamespaceAccess(gvr schema.GroupVersionResource) bool {
	return gvr.Group == aicontext.TrivyGroup ||
		gvr.Group == metalLBGroup && gvr.Resource == "bgppeers" ||
		isKagentCredentialResource(gvr) ||
		gvr.Group == "" && (gvr.Resource == "secrets" || gvr.Resource == "limitranges") ||
		gvr.Group == "rbac.authorization.k8s.io" && (gvr.Resource == "roles" || gvr.Resource == "rolebindings")
}

// ExactNamespaceAccessResource resolves the resource needing the caller's own
// permission beyond namespace visibility. Only resource == "" means no exact gate; an empty group names the core API.
func ExactNamespaceAccessResource(kind, requestedGroup string) (group, resource string) {
	gvr, ok := BuiltinGVRAnyGroup(kind)
	ok = ok && (requestedGroup == "" || requestedGroup == gvr.Group)
	if !ok {
		gvr, ok = ExactAccessCRDGVR(kind, requestedGroup)
	}
	if ok && RequiresExactNamespaceAccess(gvr) {
		return gvr.Group, gvr.Resource
	}
	return "", ""
}

// ExactNamespaceAccessNode is ExactNamespaceAccessResource for a namespaced
// neighborhood node, read from the served resource the walk records on it.
// resolved is false when the node's kind may need that permission but the node
// records no resource to check it against; the caller must deny the node.
func ExactNamespaceAccessNode(n *topology.Node) (group, resource string, resolved bool) {
	apiVersion, _ := n.Data["apiVersion"].(string)
	resource, _ = n.Data["resource"].(string)
	group = resourceid.GroupFromAPIVersion(apiVersion)
	if resource == "" {
		kind, _ := n.Data["resourceKind"].(string)
		b, builtin := resourceid.BuiltinForKind(kind)
		exact := group == aicontext.TrivyGroup || group == metalLBGroup && strings.EqualFold(kind, "BGPPeer") || isKagentCredentialKind(group, kind) || builtin && b.Group == group && RequiresExactNamespaceAccess(schema.GroupVersionResource{Group: b.Group, Resource: b.Resource})
		return "", "", !exact
	}
	if RequiresExactNamespaceAccess(schema.GroupVersionResource{Group: group, Resource: resource}) {
		return group, resource, true
	}
	return "", "", true
}
