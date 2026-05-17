package topology

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
//
// For any other (kind, group) pair — including core kinds with group=="" and
// non-colliding CRDs — KindForGVK returns kind unchanged. buildNodeID's own
// kindMap then handles URL-plural-to-singular flattening.
func KindForGVK(kind, group string) string {
	switch group {
	case "serving.knative.dev":
		switch kind {
		case "Service":
			return "knativeservice"
		case "Configuration":
			return "knativeconfiguration"
		case "Revision":
			return "knativerevision"
		case "Route":
			return "knativeroute"
		}
	case "cluster.x-k8s.io":
		if kind == "Cluster" {
			return "capicluster"
		}
	case "networking.istio.io":
		if kind == "Gateway" {
			return "istiogateway"
		}
	}
	return kind
}
