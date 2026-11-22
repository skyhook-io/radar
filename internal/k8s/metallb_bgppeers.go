package k8s

import (
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

const metalLBGroup = "metallb.io"

// MetalLBBGPPeerGVR resolves a read of kind (with an optional API group) the
// way the dynamic list and get paths do, and reports whether it reads MetalLB's
// BGPPeers. A peer's spec can hold the BGP session password inline, so read
// paths authorize them per kind, like Secrets. Calico's BGPPeer shares the kind
// in another group and is not matched.
func MetalLBBGPPeerGVR(kind, group string) (schema.GroupVersionResource, bool) {
	metalLB := strings.EqualFold(group, metalLBGroup)
	if group != "" && !metalLB {
		return schema.GroupVersionResource{}, false
	}
	gvr, err := resolveDynamicGVR(kind, group)
	if err != nil {
		if metalLB && (strings.EqualFold(kind, "BGPPeer") || strings.EqualFold(kind, "bgppeers")) {
			// Not discovered yet: authorize the read rather than skip the check.
			return schema.GroupVersionResource{Group: metalLBGroup, Resource: "bgppeers"}, true
		}
		return schema.GroupVersionResource{}, false
	}
	if gvr.Group != metalLBGroup || gvr.Resource != "bgppeers" {
		return schema.GroupVersionResource{}, false
	}
	return gvr, true
}

// ExactAccessCRDGVR reports whether a read resolves to a custom resource that
// read paths authorize per kind: Trivy Operator reports, MetalLB BGPPeers and
// kagent's RemoteMCPServers and ModelConfigs.
func ExactAccessCRDGVR(kind, group string) (schema.GroupVersionResource, bool) {
	if gvr, ok := TrivyReportGVR(kind, group); ok {
		return gvr, true
	}
	if gvr, ok := KagentCredentialGVR(kind, group); ok {
		return gvr, true
	}
	return MetalLBBGPPeerGVR(kind, group)
}
