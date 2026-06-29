package topology

import "github.com/skyhook-io/radar/pkg/resourceid"

// collisionKindToK8sKind maps the disambiguated NodeKind labels Radar uses for
// CRDs whose Kind collides with a core (or another) kind back to the real
// Kubernetes Kind. The audit suite keys findings by the real Kind, so without
// this remap a node like KindIstioGateway ("IstioGateway") would never match a
// finding on "Gateway". For every non-collision node the NodeKind already IS the
// K8s Kind. None of these collision kinds are audited today; this keeps topology
// badges correct if one ever gains a check — the single place that needs to know.
var collisionKindToK8sKind = map[NodeKind]string{
	KindIstioGateway:         "Gateway",
	KindKnativeService:       "Service",
	KindKnativeConfiguration: "Configuration",
	KindKnativeRevision:      "Revision",
	KindKnativeRoute:         "Route",
	KindCAPICluster:          "Cluster",
}

// stampAuditKeys annotates every node with the resource-identity key the audit
// suite emits findings under (audit.ResourceKey == pkg/resourceid.ResourceKey),
// so the frontend can join Cluster Audit findings onto topology nodes with a
// single string lookup instead of re-deriving identity from apiVersion/kind
// (which is fragile across the collision pseudo-kinds above). Group follows the
// audit convention exactly: built-ins → their group, everything else → "".
func stampAuditKeys(nodes []Node) []Node {
	for i := range nodes {
		k8sKind := string(nodes[i].Kind)
		if real, ok := collisionKindToK8sKind[nodes[i].Kind]; ok {
			k8sKind = real
		}
		if nodes[i].Data == nil {
			nodes[i].Data = map[string]any{}
		}
		ns, _ := nodes[i].Data["namespace"].(string)
		nodes[i].Data["auditKey"] = resourceid.ResourceKey(
			resourceid.GroupForBuiltinKind(k8sKind), k8sKind, ns, nodes[i].Name)
	}
	return nodes
}
