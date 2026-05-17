package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	"github.com/skyhook-io/radar/pkg/topology"
)

// Neighborhood tool input.
type getNeighborhoodInput struct {
	Kind      string `json:"kind" jsonschema:"resource kind: pod, deployment, service, application, etc."`
	Group     string `json:"group,omitempty" jsonschema:"API group required to disambiguate kinds that collide across groups. Examples: serving.knative.dev for KNative Service (vs core/v1 Service), cluster.x-k8s.io for CAPI Cluster (vs CNPG Cluster), networking.istio.io for Istio Gateway (vs gateway.networking.k8s.io Gateway). Omit for kinds with no known collisions."`
	Namespace string `json:"namespace,omitempty" jsonschema:"resource namespace; omit for cluster-scoped kinds"`
	Name      string `json:"name" jsonschema:"resource name"`
	Profile   string `json:"profile,omitempty" jsonschema:"edge-type preset: management, networking, policy, security, all, or auto. Default: auto (picks based on root kind)."`
	Hops      int    `json:"hops,omitempty" jsonschema:"BFS depth. Default 1, max 2."`
	MaxNodes  int    `json:"max_nodes,omitempty" jsonschema:"node-budget cap. Default 25. When the cap is hit mid-expansion, truncated=true is set and the partial subgraph is returned."`
}

// neighborhoodResult is the MCP wire shape. Matches the REST envelope so
// agents that consume both surfaces parse identically.
type neighborhoodResult struct {
	Root      topology.ResourceRef           `json:"root"`
	Subgraph  neighborhoodSubgraphMCP        `json:"subgraph"`
	Truncated bool                           `json:"truncated"`
	Omitted   []resourcecontext.OmittedField `json:"omitted,omitempty"`
}

type neighborhoodSubgraphMCP struct {
	Nodes []topology.Node `json:"nodes"`
	Edges []topology.Edge `json:"edges"`
}

func handleGetNeighborhood(ctx context.Context, req *mcp.CallToolRequest, input getNeighborhoodInput) (*mcp.CallToolResult, any, error) {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil, nil, fmt.Errorf("not connected to cluster")
	}
	if input.Kind == "" || input.Name == "" {
		return nil, nil, fmt.Errorf("kind and name are required")
	}

	// RBAC for the root. Cluster-scoped kinds go through SAR; namespaced
	// reads go through the per-user namespace filter.
	clusterScoped, gvrGroup, gvrResource := k8s.ClassifyKindScope(input.Kind, input.Group)
	if clusterScoped {
		if !canReadClusterScopedKind(ctx, gvrResource, gvrGroup, "get") {
			return nil, nil, fmt.Errorf("forbidden: %s requires explicit cluster-scoped RBAC", input.Kind)
		}
	} else {
		if input.Namespace == "" {
			return nil, nil, fmt.Errorf("namespace is required for namespaced kinds")
		}
		if !checkNamespaceAccess(ctx, input.Namespace) {
			return nil, nil, fmt.Errorf("forbidden: no access to namespace %q", input.Namespace)
		}
	}

	opts := topology.NeighborhoodOptions{
		Profile:  resolveProfile(input.Profile),
		Hops:     input.Hops,
		MaxNodes: input.MaxNodes,
	}
	if opts.Hops <= 0 {
		opts.Hops = 1
	}
	if opts.MaxNodes <= 0 {
		opts.MaxNodes = 25
	}
	if opts.MaxNodes > 200 {
		opts.MaxNodes = 200
	}

	// Build the full topology and slice via BFS. The MCP server doesn't own
	// a topology memoizer (the REST server does), so we accept the per-call
	// rebuild cost here — neighborhood is a low-frequency tool.
	//
	// dp is captured once and threaded into both Builder and BuildNeighborhoodWithIndex
	// so root-ID construction can resolve CRD plurals correctly (without it,
	// buildNodeID falls back to the static kindMap which only covers built-in kinds).
	dp := k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery())
	buildOpts := topology.DefaultBuildOptions()
	buildOpts.IncludeReplicaSets = true
	buildOpts.ForRelationshipCache = true
	// Override DefaultBuildOptions' Secret-elision: with IncludeSecrets=false,
	// root lookup for kind=secret produces an empty subgraph and the handler
	// returns "resource not found" even for authorized users. The Allow gate
	// below applies the per-namespace `get secrets` SAR per node, so
	// unauthorized users still get the same "not found" via the empty-subgraph
	// path — existence-hiding preserved.
	buildOpts.IncludeSecrets = true
	topo, err := topology.NewBuilder(k8s.NewTopologyResourceProvider(cache)).
		WithDynamic(dp).
		Build(buildOpts)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to build topology: %w", err)
	}
	// Build the inverted index once and reuse it across BFS expansion plus
	// any per-resource relationship lookups downstream. Without a memoizer
	// the cost is paid every call, but it's still cheaper than scanning
	// topo.Edges from inside the BFS loop (O(E) per hop level).
	idx := topology.IndexByResource(topo)

	root := topology.ResourceRef{
		Kind:      displayKindForMCP(input.Kind),
		Namespace: input.Namespace,
		Name:      input.Name,
		Group:     input.Group,
	}
	// Pre-filter RBAC into BFS so forbidden nodes can't shape the visible
	// graph (path-fragment effects, budget consumption). See the matching
	// REST handler for the rationale.
	opts.Allow = func(n *topology.Node) bool {
		return canReadNeighborhoodNodeMCP(ctx, n)
	}

	sub := topology.BuildNeighborhoodWithIndex(topo, root, opts, idx, dp)
	if len(sub.Nodes) == 0 {
		return nil, nil, fmt.Errorf("resource not found in topology: %s/%s/%s", input.Kind, input.Namespace, input.Name)
	}

	result := neighborhoodResult{
		Root: root,
		Subgraph: neighborhoodSubgraphMCP{
			Nodes: sub.Nodes,
			Edges: sub.Edges,
		},
		Truncated: sub.Truncated,
	}
	if sub.RBACDenied > 0 {
		// Aggregated rather than per-node — denied node refs would
		// re-leak existence info the Allow gate exists to hide.
		result.Omitted = append(result.Omitted, resourcecontext.OmittedField{
			Field:  "subgraph.nodes",
			Reason: resourcecontext.OmittedRBACDenied,
		})
	}
	if sub.Truncated {
		result.Omitted = append(result.Omitted, resourcecontext.OmittedField{
			Field:  "subgraph.nodes",
			Reason: resourcecontext.OmittedBudgetExceeded,
		})
	}
	return toJSONResult(result)
}

// resolveProfile maps a user-supplied profile string to a topology.Profile
// constant. Empty or unrecognized values fall back to ProfileAuto — agents
// often forget the field and "auto" is the right default.
func resolveProfile(s string) topology.Profile {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", string(topology.ProfileAuto):
		return topology.ProfileAuto
	case string(topology.ProfileManagement):
		return topology.ProfileManagement
	case string(topology.ProfileNetworking):
		return topology.ProfileNetworking
	case string(topology.ProfilePolicy):
		return topology.ProfilePolicy
	case string(topology.ProfileSecurity):
		return topology.ProfileSecurity
	case string(topology.ProfileAll):
		return topology.ProfileAll
	default:
		return topology.ProfileAuto
	}
}

// displayKindForMCP normalizes a lowercased / plural kind into the
// display-form used by topology nodes. MCP inputs are lowercase by
// convention; the topology graph uses display forms (Pod, Deployment, …).
func displayKindForMCP(kind string) string {
	return normalizeDisplayKind(strings.ToLower(kind))
}

// canReadNeighborhoodNodeMCP is the MCP-side per-node RBAC gate. Mirrors
// the REST canReadNeighborhoodNode. See that function for the Secret SAR
// rationale — namespace access alone leaks Secrets when the cache SA has
// cluster-wide secrets RBAC.
func canReadNeighborhoodNodeMCP(ctx context.Context, n *topology.Node) bool {
	ns := ""
	group := ""
	if n.Data != nil {
		if v, ok := n.Data["namespace"].(string); ok {
			ns = v
		}
		if v, ok := n.Data["apiVersion"].(string); ok {
			group = apiVersionGroupMCP(v)
		}
	}
	if ns != "" {
		if !checkNamespaceAccess(ctx, ns) {
			return false
		}
		// Per-kind tightening inside the namespace. v1 covers Secret only —
		// reuse canReadInNamespace exactly as handleGetResource does.
		if n.Kind == topology.KindSecret {
			return canReadInNamespace(ctx, "", "secrets", ns, "get")
		}
		return true
	}
	// Cluster-scoped: try the topology pseudo-kind table FIRST. NodeClass
	// and friends synthesize a topology-only label that ClassifyKindScope
	// doesn't recognize ("NodeClass" isn't a real K8s kind — the variants
	// are EC2NodeClass / AKSNodeClass / GCPNodeClass). Without this branch
	// pseudo-kind nodes hit the unclassified+empty-namespace fallback below
	// and leak unconditionally to users without provider-specific RBAC.
	if hit, ok := canReadClusterScopedTopoKindMCP(ctx, n); ok {
		return hit
	}
	clusterScoped, gvrGroup, gvrResource := k8s.ClassifyKindScope(string(n.Kind), group)
	if !clusterScoped {
		// Unclassified node with no namespace — let it through; we'd rather
		// over-include than silently drop a kind we forgot to register.
		return true
	}
	return canReadClusterScopedKind(ctx, gvrResource, gvrGroup, "get")
}

// canReadClusterScopedTopoKindMCP authorizes a topology cluster-scoped
// pseudo-kind against the SPECIFIC provider variant the node represents.
// The node's apiVersion (set by the topology builder) carries the provider
// group — e.g. "karpenter.k8s.aws/v1" for an EC2 NodeClass. We look up the
// matching (Kind, Group) row in topology.ClusterScopedKinds (centralized
// table — see pkg/topology/cluster_scoped_kinds.go) and SAR that single
// row, so a user with EC2 RBAC sees EC2 NodeClass nodes only — not AKS or
// GCP variants on a multi-provider cluster.
//
// Returns (allowed, true) when n.Kind is a tracked pseudo-kind, or
// (_, false) so the caller falls through to ClassifyKindScope.
//
// For single-entry kinds (NodePool, GatewayClass, PV, StorageClass, …) this
// collapses to a plain SAR on that one entry. Mirrors REST
// canReadClusterScopedTopoKind.
func canReadClusterScopedTopoKindMCP(ctx context.Context, n *topology.Node) (allowed, matched bool) {
	hasEntry := false
	for _, ck := range topology.ClusterScopedKinds {
		if ck.Kind == n.Kind {
			hasEntry = true
			break
		}
	}
	if !hasEntry {
		return false, false
	}

	group := ""
	if n.Data != nil {
		if v, ok := n.Data["apiVersion"].(string); ok {
			group = apiVersionGroupMCP(v)
		}
	}

	disc := k8s.GetResourceDiscovery()
	for _, ck := range topology.ClusterScopedKinds {
		if ck.Kind != n.Kind || ck.Group != group {
			continue
		}
		if ck.Group != "" && disc != nil {
			if _, ok := disc.GetResourceWithGroup(ck.Resource, ck.Group); !ok {
				// CRD removed mid-build — match REST behavior and allow,
				// since the topology builder wouldn't have surfaced this
				// node for an unprivileged SA either.
				return true, true
			}
		}
		// SAR cluster-scoped (namespace="") on the per-variant resource
		// directly. We deliberately skip canReadClusterScopedKind here: its
		// internal ClassifyKindScope re-resolves the resource via discovery,
		// which over-broadens (passthrough-allow) when discovery is missing
		// the CRD. The table is the source of truth for "this is cluster-
		// scoped" — we don't need discovery to re-confirm that.
		return canReadInNamespace(ctx, ck.Group, ck.Resource, "", "get"), true
	}
	// Tracked kind but the node's apiVersion-group doesn't match any entry.
	// Deny rather than allow-through — the per-variant gate exists to stop
	// unmapped variants from leaking.
	return false, true
}

func apiVersionGroupMCP(apiVersion string) string {
	for i := 0; i < len(apiVersion); i++ {
		if apiVersion[i] == '/' {
			return apiVersion[:i]
		}
	}
	return ""
}
