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
	Profile   string `json:"profile,omitempty" jsonschema:"neighborhood breadth: auto or all. Default: auto (picks a bounded edge set from the root kind)."`
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

	// RBAC for the root. Topology pseudo-kinds (NodeClass, NodePool, NodeClaim,
	// …) FIRST: ClassifyKindScope doesn't recognize them ("nodeclass" isn't a
	// real K8s kind — the variants are EC2NodeClass / AKSNodeClass / GCPNodeClass).
	// Without this branch we fall into the namespaced arm below and reject as
	// "namespace is required" even though the agent sees these kinds in
	// get_topology output. topology.RBACTuplesForKind returns the per-variant
	// SAR tuples — we iterate through canReadInNamespace and allow on any
	// pass, matching the per-node gate's first-success semantics.
	if pseudoTuples, tracked, fallthroughAllow := topology.RBACTuplesForKind(input.Kind, input.Group, pseudoKindDiscoveryLookupMCP()); tracked {
		if !allowPseudoKindTuplesMCP(ctx, pseudoTuples, fallthroughAllow) {
			return nil, nil, fmt.Errorf("forbidden: %s requires explicit cluster-scoped RBAC", input.Kind)
		}
	} else if clusterScoped, gvrGroup, gvrResource := k8s.ClassifyKindScope(input.Kind, input.Group); clusterScoped {
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
	// Top-end clamps symmetric with REST. BFS clamps Hops internally
	// (neighborhoodMaxHops) but doing it here too means opts.Hops is
	// correct if anything inspects/logs it before BFS.
	if opts.Hops > 2 {
		opts.Hops = 2
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
	if sub.AmbiguousRoot {
		return nil, nil, fmt.Errorf("resource kind is ambiguous for %s/%s/%s; provide group", input.Kind, input.Namespace, input.Name)
	}
	if len(sub.Nodes) == 0 {
		return nil, nil, fmt.Errorf("resource not found in topology: %s/%s/%s", input.Kind, input.Namespace, input.Name)
	}

	// Use the resolved root node's Kind for the response. displayKindForMCP
	// only normalizes built-in kinds (Pod, Deployment, …); pseudo-kinds
	// like "nodeclass"/"nodepool" pass through lowercase while subgraph
	// nodes carry display-form NodeKind ("NodeClass"). Without this rewrite
	// the response's root.kind would diverge from subgraph.nodes[0].kind
	// within the same payload. Matches the REST handler's identical fix.
	rootResp := root
	rootResp.Kind = string(sub.Nodes[0].Kind)

	result := neighborhoodResult{
		Root: rootResp,
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
	return toJSONResult(result)
}

// resolveProfile is retained as a thin shim around topology.ResolveProfile
// so the local call sites in this file don't need updating. New callers
// should use topology.ResolveProfile directly.
func resolveProfile(s string) topology.Profile {
	return topology.ResolveProfile(s)
}

// displayKindForMCP normalizes a lowercased / plural kind into the
// display-form used by topology nodes. MCP inputs are lowercase by
// convention; the topology graph uses display forms (Pod, Deployment, …).
func displayKindForMCP(kind string) string {
	return normalizeDisplayKind(strings.ToLower(kind))
}

// canReadNeighborhoodNodeMCP is the MCP-side per-node RBAC gate. Mirrors
// the REST canReadNeighborhoodNode — same decision tree, different per-user
// check function. Tuple-selection logic lives in topology.RBACTuplesForNode
// so both surfaces stay in lockstep when the pseudo-kind table or Secret-
// tightening rules evolve.
//
// See REST canReadNeighborhoodNode for the Secret SAR rationale — namespace
// access alone leaks Secrets when the cache SA has cluster-wide secrets RBAC
// the calling user doesn't.
func canReadNeighborhoodNodeMCP(ctx context.Context, n *topology.Node) bool {
	// Namespace-list gate is protocol-specific; apply it here for namespaced
	// nodes BEFORE consulting the shared helper.
	if n != nil && n.Data != nil {
		if ns, ok := n.Data["namespace"].(string); ok && ns != "" {
			if !checkNamespaceAccess(ctx, ns) {
				return false
			}
		}
	}

	decision, tuples := topology.RBACTuplesForNode(n, pseudoKindDiscoveryLookupMCP())
	switch decision {
	case topology.NodeRBACAllow:
		return true
	case topology.NodeRBACDeny:
		return false
	case topology.NodeRBACCheckTuples:
		for _, t := range tuples {
			if canReadInNamespace(ctx, t.Group, t.Resource, t.Namespace, "get") {
				return true
			}
		}
		return false
	case topology.NodeRBACConsultClassifyKindScope:
		// Cluster-scoped node that isn't a tracked pseudo-kind. Fall back to
		// the regular static-catalogue / discovery path. Unclassified kinds
		// allow-through: the topology graph wouldn't have surfaced the node
		// for an unprivileged SA either.
		group := ""
		if n.Data != nil {
			if v, ok := n.Data["apiVersion"].(string); ok {
				group = topology.APIVersionGroup(v)
			}
		}
		clusterScoped, gvrGroup, gvrResource := k8s.ClassifyKindScope(string(n.Kind), group)
		if !clusterScoped {
			return true
		}
		return canReadClusterScopedKind(ctx, gvrResource, gvrGroup, "get")
	default:
		// New decision values must be handled explicitly; default-deny.
		return false
	}
}

// pseudoKindDiscoveryLookupMCP returns the function form of the discovery
// singleton that topology.RBACTuplesForKind / RBACTuplesForNode expect, or
// nil when discovery isn't initialised (test envs). See the doc on
// topology.PseudoKindDiscoveryLookup for why this is a function rather than
// an interface (typed-nil-into-interface gotcha).
func pseudoKindDiscoveryLookupMCP() topology.PseudoKindDiscoveryLookup {
	disc := k8s.GetResourceDiscovery()
	if disc == nil {
		return nil
	}
	return disc.GetResourceWithGroup
}

// allowPseudoKindTuplesMCP authorizes a list of per-variant SAR tuples
// returned by topology.RBACTuplesForKind for the root-preflight path.
// Iterates each tuple through canReadInNamespace and allows on the first
// pass; if the helper returned zero tuples + fallthroughAllow=true (every
// variant was filtered out by discovery), allow — matches the pre-existing
// "over-include on absent provider variants" behavior.
//
// We use canReadInNamespace(group, resource, "", "get") directly rather than
// canReadClusterScopedKind: canReadClusterScopedKind re-resolves the
// resource via ClassifyKindScope's discovery, which over-broadens
// (passthrough-allow) when the CRD is missing. The table is the source of
// truth for "this is cluster-scoped" — no need for discovery to re-confirm.
func allowPseudoKindTuplesMCP(ctx context.Context, tuples []topology.SARTuple, fallthroughAllow bool) bool {
	if len(tuples) == 0 {
		return fallthroughAllow
	}
	for _, t := range tuples {
		if canReadInNamespace(ctx, t.Group, t.Resource, t.Namespace, "get") {
			return true
		}
	}
	return false
}
