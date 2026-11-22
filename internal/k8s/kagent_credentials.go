package k8s

import (
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/kagent"
)

// kagentCredentialResources are the kagent kinds whose spec can hold a
// credential inline: a RemoteMCPServer's spec.headersFrom[].value and a
// ModelConfig's spec.defaultHeaders (and, in kagent 1.0.x,
// spec.azureOpenAI.azureAdToken). Read paths authorize them per kind, like
// Secrets.
var kagentCredentialResources = map[string]string{
	"remotemcpservers": "remotemcpservers", "remotemcpserver": "remotemcpservers",
	"modelconfigs": "modelconfigs", "modelconfig": "modelconfigs",
}

func isKagentCredentialResource(gvr schema.GroupVersionResource) bool {
	return gvr.Group == kagent.Group && (gvr.Resource == "remotemcpservers" || gvr.Resource == "modelconfigs")
}

// isKagentCredentialKind matches a node or reference that names the kind
// rather than its resource.
func isKagentCredentialKind(group, kind string) bool {
	return group == kagent.Group && (strings.EqualFold(kind, kagent.KindRemoteMCPServer) || strings.EqualFold(kind, kagent.KindModelConfig))
}

// KagentCredentialGVR resolves a read of kind (with an optional API group)
// the way the dynamic list and get paths do, and reports whether it reads
// kagent's RemoteMCPServers or ModelConfigs.
func KagentCredentialGVR(kind, group string) (schema.GroupVersionResource, bool) {
	kagentGroup := strings.EqualFold(group, kagent.Group)
	if group != "" && !kagentGroup {
		return schema.GroupVersionResource{}, false
	}
	gvr, err := resolveDynamicGVR(kind, group)
	if err != nil {
		if resource, ok := kagentCredentialResources[strings.ToLower(kind)]; ok && kagentGroup {
			// Not discovered yet: authorize the read rather than skip the check.
			return schema.GroupVersionResource{Group: kagent.Group, Resource: resource}, true
		}
		return schema.GroupVersionResource{}, false
	}
	if !isKagentCredentialResource(gvr) {
		return schema.GroupVersionResource{}, false
	}
	return gvr, true
}
