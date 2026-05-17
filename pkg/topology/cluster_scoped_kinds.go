package topology

// ClusterScopedKindEntry maps a topology NodeKind for a cluster-scoped
// resource to the (group, resource) tuple a SubjectAccessReview needs to
// authorize a list/get.
type ClusterScopedKindEntry struct {
	Kind     NodeKind
	Group    string
	Resource string
}

// ClusterScopedKinds is the central denylist of cluster-scoped kinds the
// topology builder synthesizes. Both the topology-strip path (REST + MCP)
// and the neighborhood per-node RBAC gate iterate this table; centralising
// it removes the historical "update BOTH internal/server and internal/mcp"
// drift hazard that the checklist on NodeKind warned about.
//
// KindNamespace is intentionally excluded — handled by per-user filter
// upstream. KindNodeClass has one entry per cloud provider (EC2 / AKS /
// GCP) because the topology builder iterates them under the same NodeKind
// label; callers should treat ALL three as a single SAR-anchored gate so
// providers absent from cluster discovery don't over-deny.
var ClusterScopedKinds = []ClusterScopedKindEntry{
	{KindNode, "", "nodes"},
	{KindNodePool, "karpenter.sh", "nodepools"},
	{KindNodeClaim, "karpenter.sh", "nodeclaims"},
	{KindNodeClass, "karpenter.k8s.aws", "ec2nodeclasses"},
	{KindNodeClass, "karpenter.azure.com", "aksnodeclasses"},
	{KindNodeClass, "karpenter.k8s.gcp", "gcpnodeclasses"},
	{KindGatewayClass, "gateway.networking.k8s.io", "gatewayclasses"},
	{KindPV, "", "persistentvolumes"},
	{KindStorageClass, "storage.k8s.io", "storageclasses"},
	{KindCiliumClusterwideNetworkPolicy, "cilium.io", "ciliumclusterwidenetworkpolicies"},
	{KindClusterNetworkPolicy, "policy.networking.k8s.io", "clusternetworkpolicies"},
}
