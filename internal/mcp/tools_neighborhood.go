package mcp

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	"github.com/skyhook-io/radar/pkg/topology"
)

// Neighborhood tool input.
type getNeighborhoodInput struct {
	Kind      string `json:"kind" jsonschema:"resource kind: pod, deployment, service, application, etc."`
	Group     string `json:"group,omitempty" jsonschema:"API group when the kind is ambiguous (e.g. cluster.x-k8s.io for CAPI Cluster vs CNPG Cluster)"`
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
	buildOpts := topology.DefaultBuildOptions()
	buildOpts.IncludeReplicaSets = true
	buildOpts.ForRelationshipCache = true
	topo, err := topology.NewBuilder(k8s.NewTopologyResourceProvider(cache)).
		WithDynamic(k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery())).
		Build(buildOpts)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to build topology: %w", err)
	}

	root := topology.ResourceRef{
		Kind:      displayKindForMCP(input.Kind),
		Namespace: input.Namespace,
		Name:      input.Name,
		Group:     input.Group,
	}
	sub := topology.BuildNeighborhood(topo, root, opts)
	if len(sub.Nodes) == 0 {
		return nil, nil, fmt.Errorf("resource not found in topology: %s/%s/%s", input.Kind, input.Namespace, input.Name)
	}

	kept, omitted := filterNeighborhoodForUserMCP(ctx, sub)

	result := neighborhoodResult{
		Root: root,
		Subgraph: neighborhoodSubgraphMCP{
			Nodes: kept.Nodes,
			Edges: kept.Edges,
		},
		Truncated: sub.Truncated,
		Omitted:   omitted,
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

// filterNeighborhoodForUserMCP is the MCP-side per-node RBAC sweep. Mirrors
// the REST helper, but uses the MCP RBAC helpers (canReadClusterScopedKind,
// checkNamespaceAccess) so SARs are cached on the right per-user cache.
//
// Root is always retained: the upstream RBAC check authorized it. Per-node
// drops are recorded in the returned []OmittedField using the locked
// "subgraph.nodes[i]" path convention.
func filterNeighborhoodForUserMCP(ctx context.Context, sub *topology.Subgraph) (*topology.Subgraph, []resourcecontext.OmittedField) {
	if sub == nil || len(sub.Nodes) == 0 {
		return sub, nil
	}

	keptIDs := make(map[string]bool, len(sub.Nodes))
	kept := &topology.Subgraph{
		Root:      sub.Root,
		Truncated: sub.Truncated,
		Nodes:     make([]topology.Node, 0, len(sub.Nodes)),
	}
	var omitted []resourcecontext.OmittedField

	for i, n := range sub.Nodes {
		if i == 0 {
			kept.Nodes = append(kept.Nodes, n)
			keptIDs[n.ID] = true
			continue
		}
		if canReadNeighborhoodNodeMCP(ctx, &n) {
			kept.Nodes = append(kept.Nodes, n)
			keptIDs[n.ID] = true
			continue
		}
		omitted = append(omitted, resourcecontext.OmittedField{
			Field:  "subgraph.nodes[" + strconv.Itoa(i) + "]",
			Reason: resourcecontext.OmittedRBACDenied,
		})
	}

	kept.Edges = make([]topology.Edge, 0, len(sub.Edges))
	for _, e := range sub.Edges {
		if keptIDs[e.Source] && keptIDs[e.Target] {
			kept.Edges = append(kept.Edges, e)
		}
	}
	return kept, omitted
}

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
		return checkNamespaceAccess(ctx, ns)
	}
	clusterScoped, gvrGroup, gvrResource := k8s.ClassifyKindScope(string(n.Kind), group)
	if !clusterScoped {
		// Unclassified node with no namespace — let it through; we'd rather
		// over-include than silently drop a kind we forgot to register.
		return true
	}
	return canReadClusterScopedKind(ctx, gvrResource, gvrGroup, "get")
}

func apiVersionGroupMCP(apiVersion string) string {
	for i := 0; i < len(apiVersion); i++ {
		if apiVersion[i] == '/' {
			return apiVersion[:i]
		}
	}
	return ""
}
