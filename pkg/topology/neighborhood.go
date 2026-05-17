package topology

import (
	"strings"
)

// Profile is the agent-friendly preset for which edge types to traverse when
// expanding a neighborhood. Each profile collapses a small set of edge types
// that share a coherent semantic (management chain, network flow, policy
// attachments, …) so an agent can ask for "the slice of the graph relevant
// to this question" without needing to enumerate edge types directly.
type Profile string

const (
	// ProfileManagement walks owner / controller edges (Deployment → RS → Pod,
	// Application → workloads).
	ProfileManagement Profile = "management"
	// ProfileNetworking walks routing + exposure edges (Ingress → Service → Pod,
	// Gateway → HTTPRoute → Service).
	ProfileNetworking Profile = "networking"
	// ProfilePolicy walks PDB / NetworkPolicy / MachineHealthCheck attachments.
	ProfilePolicy Profile = "policy"
	// ProfileSecurity is reserved for synthesized RBAC / image / SA relations.
	// v1 produces no edges — populated when those are wired in.
	ProfileSecurity Profile = "security"
	// ProfileAll walks every edge type.
	ProfileAll Profile = "all"
	// ProfileAuto picks an edge-type set based on the root kind. See
	// edgeTypesForAuto for the per-kind mapping.
	ProfileAuto Profile = "auto"
)

// NeighborhoodOptions configures a BuildNeighborhood call. Zero values are
// replaced with sensible defaults: Profile=auto, Hops=1, MaxNodes=25.
type NeighborhoodOptions struct {
	Profile  Profile
	Hops     int
	MaxNodes int
}

// Subgraph is the BFS-expanded neighborhood of a root resource. Nodes are
// the existing topology Node shape (already summary-minified — id / kind /
// name / status / small data map). Edges are filtered by the profile and
// only included when both endpoints are present in Nodes.
type Subgraph struct {
	Root      ResourceRef `json:"root"`
	Nodes     []Node      `json:"nodes"`
	Edges     []Edge      `json:"edges"`
	Truncated bool        `json:"truncated,omitempty"`
}

// neighborhoodMaxHops is the hard ceiling on BFS depth. Two hops is enough
// to reach grandparents (Pod → ReplicaSet → Deployment) without exploding
// into the whole namespace.
const neighborhoodMaxHops = 2

// neighborhoodDefaultHops is the default BFS depth when Hops is unset.
const neighborhoodDefaultHops = 1

// neighborhoodDefaultMaxNodes is the default node-budget when MaxNodes is
// unset. 25 fits roughly a workload + its owners + selecting services +
// attached policies without spilling into cluster-level dependencies.
const neighborhoodDefaultMaxNodes = 25

// BuildNeighborhood returns the BFS expansion of root in t, filtered by
// opts.Profile. Returns a non-nil Subgraph even when the root is missing
// from the topology — callers can check len(s.Nodes) == 0 for that case.
func BuildNeighborhood(t *Topology, root ResourceRef, opts NeighborhoodOptions) *Subgraph {
	hops := opts.Hops
	if hops <= 0 {
		hops = neighborhoodDefaultHops
	}
	if hops > neighborhoodMaxHops {
		hops = neighborhoodMaxHops
	}
	maxNodes := opts.MaxNodes
	if maxNodes <= 0 {
		maxNodes = neighborhoodDefaultMaxNodes
	}

	sub := &Subgraph{
		Root:  root,
		Nodes: []Node{},
		Edges: []Edge{},
	}
	if t == nil {
		return sub
	}

	nodeByID := make(map[string]*Node, len(t.Nodes))
	for i := range t.Nodes {
		nodeByID[t.Nodes[i].ID] = &t.Nodes[i]
	}

	rootID := buildNodeID(root.Kind, root.Namespace, root.Name, nil)
	rootNode, ok := nodeByID[rootID]
	if !ok {
		// Fallback: try matching by (kind, namespace, name) tuple. Mostly
		// for CRDs whose topology node ID uses a different prefix than the
		// lowercase kind (e.g. "knativeservice/"). Cheap because we already
		// have the node map.
		rootNode = findNodeByRef(t.Nodes, root)
		if rootNode == nil {
			return sub
		}
		rootID = rootNode.ID
	}

	allowedEdges := edgeTypesForProfile(opts.Profile, rootNode.Kind)

	// Group edges by node ID with type filter applied. We walk the graph
	// undirected — a Pod is the target of Service→Pod (exposes), but the
	// agent wants to find the Service from the Pod just as much as vice
	// versa.
	adjacency := make(map[string][]Edge, len(t.Nodes))
	for _, e := range t.Edges {
		if !allowedEdges[e.Type] {
			continue
		}
		adjacency[e.Source] = append(adjacency[e.Source], e)
		adjacency[e.Target] = append(adjacency[e.Target], e)
	}

	// BFS by hop level. visited[id] = hop at which the node entered the
	// frontier, so we can stop when we'd exceed hops.
	included := map[string]bool{rootID: true}
	order := []string{rootID}
	frontier := []string{rootID}
	truncated := false

	for hop := 0; hop < hops; hop++ {
		var next []string
		for _, id := range frontier {
			for _, e := range adjacency[id] {
				other := e.Source
				if other == id {
					other = e.Target
				}
				if included[other] {
					continue
				}
				if _, exists := nodeByID[other]; !exists {
					// Edge dangles off a node that isn't in the topology.
					continue
				}
				if len(included) >= maxNodes {
					truncated = true
					continue
				}
				included[other] = true
				order = append(order, other)
				next = append(next, other)
			}
			if truncated {
				break
			}
		}
		if truncated || len(next) == 0 {
			break
		}
		frontier = next
	}

	sub.Truncated = truncated

	// Materialize nodes in BFS order so the root is always first and the
	// rest follow predictable expansion order.
	sub.Nodes = make([]Node, 0, len(order))
	for _, id := range order {
		if n, ok := nodeByID[id]; ok {
			sub.Nodes = append(sub.Nodes, *n)
		}
	}

	// Edges: include only edges whose type is allowed AND both endpoints
	// are in the included set.
	for _, e := range t.Edges {
		if !allowedEdges[e.Type] {
			continue
		}
		if !included[e.Source] || !included[e.Target] {
			continue
		}
		sub.Edges = append(sub.Edges, e)
	}

	return sub
}

// edgeTypesForProfile returns the set of edge types a profile traverses.
// rootKind is used for ProfileAuto only.
func edgeTypesForProfile(p Profile, rootKind NodeKind) map[EdgeType]bool {
	switch p {
	case ProfileManagement:
		return map[EdgeType]bool{EdgeManages: true}
	case ProfileNetworking:
		return map[EdgeType]bool{EdgeRoutesTo: true, EdgeExposes: true}
	case ProfilePolicy:
		return map[EdgeType]bool{EdgeProtects: true}
	case ProfileSecurity:
		// v1: empty — synthesized edges live in a later phase.
		return map[EdgeType]bool{}
	case ProfileAll, "":
		return allEdgeTypes()
	case ProfileAuto:
		return edgeTypesForAuto(rootKind)
	default:
		return allEdgeTypes()
	}
}

// allEdgeTypes returns the universal set. Kept centralized so adding an
// EdgeType updates ProfileAll automatically.
func allEdgeTypes() map[EdgeType]bool {
	return map[EdgeType]bool{
		EdgeManages:    true,
		EdgeRoutesTo:   true,
		EdgeExposes:    true,
		EdgeUses:       true,
		EdgeProtects:   true,
		EdgeConfigures: true,
	}
}

// edgeTypesForAuto picks profile edge-types based on the root's kind. The
// goal is "the agent asked about a workload — show the management chain
// plus the network and policy attachments, not the whole graph."
func edgeTypesForAuto(rootKind NodeKind) map[EdgeType]bool {
	switch rootKind {
	// Workloads / pods: management chain + network exposure + protection.
	case KindPod, KindPodGroup, KindDeployment, KindStatefulSet, KindDaemonSet,
		KindReplicaSet, KindRollout, KindJob, KindCronJob,
		KindKnativeService, KindKnativeRevision, KindKnativeConfiguration:
		return map[EdgeType]bool{
			EdgeManages:  true,
			EdgeRoutesTo: true,
			EdgeExposes:  true,
			EdgeProtects: true,
		}
	// GitOps controllers: just the management chain (what they own).
	case KindApplication, KindKustomization, KindHelmRelease, KindGitRepository:
		return map[EdgeType]bool{EdgeManages: true}
	// Network-shaped resources: routing topology.
	case KindService, KindIngress, KindGateway, KindGatewayClass,
		KindHTTPRoute, KindGRPCRoute, KindTCPRoute, KindTLSRoute,
		KindVirtualService, KindIstioGateway, KindHTTPProxy,
		KindIngressRoute, KindIngressRouteTCP, KindIngressRouteUDP:
		return map[EdgeType]bool{EdgeRoutesTo: true, EdgeExposes: true}
	// Policies / protectors: who they attach to.
	case KindNetworkPolicy, KindCiliumNetworkPolicy,
		KindCiliumClusterwideNetworkPolicy, KindClusterNetworkPolicy,
		KindPDB, KindMachineHealthCheck,
		KindPeerAuthentication, KindAuthorizationPolicy:
		return map[EdgeType]bool{EdgeProtects: true}
	// Nodes / node pools: hosted workloads via management chain.
	case KindNode, KindNodePool, KindNodeClaim, KindNodeClass:
		return map[EdgeType]bool{EdgeManages: true}
	default:
		return allEdgeTypes()
	}
}

// findNodeByRef looks up a node by (kind, namespace, name). Used as a
// fallback when buildNodeID's lowercase-kind heuristic produces an ID that
// doesn't match a CRD-prefixed node ID (e.g. "knativeservice/").
func findNodeByRef(nodes []Node, ref ResourceRef) *Node {
	wantKind := strings.ToLower(string(ref.Kind))
	for i := range nodes {
		n := &nodes[i]
		if !strings.EqualFold(string(n.Kind), ref.Kind) {
			continue
		}
		if n.Name != ref.Name {
			continue
		}
		ns := nodeNamespaceFromData(n)
		if ns != ref.Namespace {
			continue
		}
		_ = wantKind
		return n
	}
	return nil
}

// nodeNamespaceFromData reads the namespace from a Node's Data map. Mirrors
// the convention used by the builder ("namespace" → string).
func nodeNamespaceFromData(n *Node) string {
	if n.Data == nil {
		return ""
	}
	if ns, ok := n.Data["namespace"].(string); ok {
		return ns
	}
	return ""
}
