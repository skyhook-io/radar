package agentsandbox

import (
	"regexp"
	"strings"

	"github.com/skyhook-io/radar/pkg/scheduling"
)

// A next step names a specific cause only where the message the row carries
// shows it; otherwise the general step stands.
const (
	schedulerNextStep = "Free capacity on a node, or change the Pod template’s requests, node selector or tolerations"
	podLimitNextStep  = "Add nodes, raise the nodes’ max-pods limit, or remove Pods you no longer need"
	// registryDNSNextStep is mirrored in k8s-ui, which applies it to the pull
	// error a drawer reads from the Pod; testdata/image_pull_next_steps.json
	// pins both.
	registryDNSNextStep = "Check the registry hostname in the Pod template’s image, and that the nodes’ DNS resolves it"
)

// schedulerStep is the next step for a Pod the scheduler can't place.
func schedulerStep(message string) string {
	if podLimitOnly(message) {
		return podLimitNextStep
	}
	return schedulerNextStep
}

// podLimitOnly: the scheduler's message says nodes are at their Pod limit and
// gives no other reason, apart from nodes reserved by a node-role taint (the
// control plane), which no Pod template change is meant to reach.
func podLimitOnly(message string) bool {
	_, reasons := scheduling.ParseMessage(message)
	limit := false
	for _, r := range reasons {
		switch {
		case r.Class == scheduling.InsufficientResource && r.Resource == "pods":
			limit = true
		case r.Class == scheduling.NodeUnschedulable && strings.HasPrefix(r.TaintKey, "node-role.kubernetes.io/"):
		default:
			return false
		}
	}
	return limit
}

// registryDNSFailure is how Go's resolver, which containerd and the kubelet
// use, reports a name that didn't resolve: "dial tcp: lookup registry.example
// on 10.0.0.2:53: no such host", or "server misbehaving" from the DNS server.
var registryDNSFailure = regexp.MustCompile(`(?i)no such host|server misbehaving|\blookup \S+ on \S+`)

// imagePullNextStep is the next step an image pull failure's message gives
// evidence for, or "" when it names no cause Radar recognizes.
func imagePullNextStep(reason, message string) string {
	if (reason == "ImagePullBackOff" || reason == "ErrImagePull") && registryDNSFailure.MatchString(message) {
		return registryDNSNextStep
	}
	return ""
}
