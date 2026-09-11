package topology

import (
	"strings"

	"github.com/skyhook-io/radar/pkg/resourceid"
)

// collisionKindToK8sKind maps the disambiguated NodeKind labels Radar uses for
// CRDs whose Kind collides with a core (or another) kind back to the real
// Kubernetes Kind. The audit suite keys findings by the real Kind, so without
// this remap a node like KindIstioGateway ("IstioGateway") would never match a
// finding on "Gateway". For every non-collision node the NodeKind already IS the
// K8s Kind. None of these collision kinds are audited today; this keeps topology
// badges correct if one ever gains a check — the single place that needs to know.
var collisionKindToK8sKind = map[NodeKind]string{
	KindIstioGateway:                        "Gateway",
	KindKnativeService:                      "Service",
	KindKnativeConfiguration:                "Configuration",
	KindKnativeRevision:                     "Revision",
	KindKnativeRoute:                        "Route",
	KindCAPICluster:                         "Cluster",
	KindCalicoNetworkPolicy:                 "NetworkPolicy",
	KindCalicoGlobalNetworkPolicy:           "GlobalNetworkPolicy",
	KindCalicoStagedNetworkPolicy:           "StagedNetworkPolicy",
	KindCalicoStagedGlobalNetworkPolicy:     "StagedGlobalNetworkPolicy",
	KindCalicoStagedKubernetesNetworkPolicy: "StagedKubernetesNetworkPolicy",
}

// KubernetesKindForNode returns the Kubernetes Kind represented by a topology
// node, undoing Radar's collision and provider-specific pseudo-kinds.
func KubernetesKindForNode(node *Node) string {
	if node == nil {
		return ""
	}
	if node.Kind == KindNodeClass {
		if kind, ok := node.Data["resourceKind"].(string); ok && kind != "" {
			return kind
		}
	}
	if kind, ok := collisionKindToK8sKind[node.Kind]; ok {
		return kind
	}
	return string(node.Kind)
}

// nodeResourceKeys returns every exact Kubernetes identity a topology node can
// be addressed through. Most nodes have one identity; Calico policies may be
// served through both their native and CRD API groups while remaining one
// logical resource.
func nodeResourceKeys(node *Node) []string {
	if node == nil || node.Name == "" {
		return nil
	}
	kind := KubernetesKindForNode(node)
	namespace := nodeNamespaceFromData(node)
	if namespace == "" {
		parts := strings.Split(node.ID, "/")
		if len(parts) >= 3 {
			namespace = parts[1]
		}
	}
	group := nodeAPIGroupFromData(node)
	if group == "" {
		group = resourceid.GroupForBuiltinKind(kind)
	}
	keys := []string{resourceid.ResourceKey(group, kind, namespace, node.Name)}
	if !IsCalicoPolicyKind(node.Kind) {
		return keys
	}
	for _, servedGroup := range calicoNodeAPIGroups(node) {
		if servedGroup != "" && servedGroup != group {
			keys = append(keys, resourceid.ResourceKey(servedGroup, kind, namespace, node.Name))
		}
	}
	return keys
}

// stampAuditKeys annotates every node with the resource-identity key the audit
// suite emits findings under (audit.ResourceKey == pkg/resourceid.ResourceKey),
// so the frontend can join Cluster Audit findings onto topology nodes with a
// single string lookup instead of re-deriving identity from apiVersion/kind
// (which is fragile across the collision pseudo-kinds above). Group follows the
// audit convention exactly: built-ins → their group, everything else → "".
func stampAuditKeys(nodes []Node) []Node {
	for i := range nodes {
		k8sKind := KubernetesKindForNode(&nodes[i])
		if nodes[i].Data == nil {
			nodes[i].Data = map[string]any{}
		}
		if k8sKind != string(nodes[i].Kind) {
			nodes[i].Data["resourceKind"] = k8sKind
		}
		ns, _ := nodes[i].Data["namespace"].(string)
		group := resourceid.GroupForBuiltinKind(k8sKind)
		if nodeGroup := nodeAPIGroupFromData(&nodes[i]); nodeGroup != "" && nodeGroup != group {
			group = ""
		}
		nodes[i].Data["auditKey"] = resourceid.ResourceKey(
			group, k8sKind, ns, nodes[i].Name)
	}
	return nodes
}
