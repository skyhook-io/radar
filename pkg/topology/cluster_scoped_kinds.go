package topology

import "strings"

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

// LookupClusterScopedTopoKind returns every ClusterScopedKinds row whose Kind
// matches the caller-supplied (kind, group) tuple after pseudo-kind resolution.
// Empty slice when no row matches — caller should fall back to the regular
// ClassifyKindScope path.
//
// Match semantics:
//
//   - Kind is compared case-insensitively against the row's NodeKind, so URL-
//     path kinds ("nodeclass", "nodepool" — already lowercased by REST's
//     normalizeKind) line up with the Pascal-cased NodeKind constants used in
//     the table. The MCP side feeds lowercase too via displayKindForMCP, so the
//     same path covers both surfaces.
//   - The caller-supplied (kind, group) is first resolved via pseudoKindFor so
//     namespaced pseudo-kinds (KnativeService for serving.knative.dev/Service,
//     CAPICluster for cluster.x-k8s.io/Cluster, …) don't false-match against
//     the cluster-scoped table. ClusterScopedKinds contains only cluster-scoped
//     entries, so if (kind, group) resolves to a namespaced pseudo-kind the
//     lookup correctly returns no rows.
//   - When `group` is supplied, ONLY the row matching that group is returned
//     (single-variant disambiguation for kinds like NodeClass where the table
//     has one row per provider). Empty group returns ALL rows under that kind
//     so callers can SAR each (used by root preflight, which often gets
//     kind=nodeclass without an explicit provider group).
//
// The returned slice is a fresh copy — callers may mutate freely. Reads only:
// callers want to SAR each entry and allow on any pass, the standard pattern
// the topology-strip and neighborhood-RBAC paths already use.
func LookupClusterScopedTopoKind(kind, group string) []ClusterScopedKindEntry {
	resolved := pseudoKindFor(kind, group)
	out := make([]ClusterScopedKindEntry, 0, 3)
	for _, ck := range ClusterScopedKinds {
		if !strings.EqualFold(string(ck.Kind), resolved) {
			continue
		}
		if group != "" && ck.Group != group {
			continue
		}
		out = append(out, ck)
	}
	return out
}
