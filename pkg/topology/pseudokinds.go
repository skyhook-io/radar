package topology

import "strings"

// KindForGVK maps a (kind, group) pair to the topology-internal pseudo-kind
// the builder uses for node IDs. The topology builder synthesizes pseudo-kinds
// for a handful of CRDs whose Kind collides with a core kind under a different
// API group — these collisions would otherwise produce ambiguous node IDs.
//
// Callers that already hold the resource's apiVersion (i.e., obj.GVK) and want
// to look up the matching topology node MUST funnel kind through this helper,
// otherwise buildNodeID would resolve to the core node and return relationships
// for the wrong object.
//
// Today the cross-group collisions are:
//
//	serving.knative.dev/Service       → "knativeservice"
//	serving.knative.dev/Configuration → "knativeconfiguration"
//	serving.knative.dev/Revision      → "knativerevision"
//	serving.knative.dev/Route         → "knativeroute"
//	cluster.x-k8s.io/Cluster          → "capicluster"
//	networking.istio.io/Gateway       → "istiogateway"
//	projectcalico.org/NetworkPolicy   → "caliconetworkpolicy"
//	projectcalico.org/GlobalNetworkPolicy → "calicoglobalnetworkpolicy"
//	projectcalico.org/StagedNetworkPolicy → "calicostagednetworkpolicy"
//	projectcalico.org/StagedGlobalNetworkPolicy → "calicostagedglobalnetworkpolicy"
//
// For any other (kind, group) pair — including core kinds with group=="" and
// non-colliding CRDs — KindForGVK returns kind unchanged. buildNodeID's own
// kindMap then handles URL-plural-to-singular flattening.
func KindForGVK(kind, group string) string {
	switch strings.ToLower(group) {
	case "serving.knative.dev":
		switch strings.ToLower(kind) {
		case "service", "services":
			return "knativeservice"
		case "configuration", "configurations":
			return "knativeconfiguration"
		case "revision", "revisions":
			return "knativerevision"
		case "route", "routes":
			return "knativeroute"
		}
	case "cluster.x-k8s.io":
		if strings.EqualFold(kind, "Cluster") || strings.EqualFold(kind, "Clusters") {
			return "capicluster"
		}
	case "networking.istio.io":
		if strings.EqualFold(kind, "Gateway") || strings.EqualFold(kind, "Gateways") {
			return "istiogateway"
		}
	case "projectcalico.org", "crd.projectcalico.org":
		switch strings.ToLower(kind) {
		case "networkpolicy", "networkpolicies", "caliconetworkpolicy":
			return "caliconetworkpolicy"
		case "globalnetworkpolicy", "globalnetworkpolicies", "calicoglobalnetworkpolicy":
			return "calicoglobalnetworkpolicy"
		case "stagednetworkpolicy", "stagednetworkpolicies", "calicostagednetworkpolicy":
			return "calicostagednetworkpolicy"
		case "stagedglobalnetworkpolicy", "stagedglobalnetworkpolicies", "calicostagedglobalnetworkpolicy":
			return "calicostagedglobalnetworkpolicy"
		case "stagedkubernetesnetworkpolicy", "stagedkubernetesnetworkpolicies", "calicostagedkubernetesnetworkpolicy":
			return "calicostagedkubernetesnetworkpolicy"
		}
	}
	return kind
}
