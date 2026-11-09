package k8s

import (
	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// RequiresExactNamespaceAccess reports whether namespace visibility alone is
// insufficient to read the resolved resource. Trivy findings can quote secrets.
func RequiresExactNamespaceAccess(gvr schema.GroupVersionResource) bool {
	return gvr.Group == aicontext.TrivyGroup ||
		gvr.Group == "" && (gvr.Resource == "secrets" || gvr.Resource == "limitranges") ||
		gvr.Group == "rbac.authorization.k8s.io" && (gvr.Resource == "roles" || gvr.Resource == "rolebindings")
}

// ExactNamespaceAccessResource resolves the resource needing the caller's own
// permission beyond namespace visibility. Only resource == "" means no exact gate; an empty group names the core API.
func ExactNamespaceAccessResource(kind, requestedGroup string) (group, resource string) {
	gvr, ok := BuiltinGVRAnyGroup(kind)
	ok = ok && (requestedGroup == "" || requestedGroup == gvr.Group)
	if !ok {
		gvr, ok = TrivyReportGVR(kind, requestedGroup)
	}
	if ok && RequiresExactNamespaceAccess(gvr) {
		return gvr.Group, gvr.Resource
	}
	return "", ""
}
