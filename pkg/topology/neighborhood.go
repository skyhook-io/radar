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

// ResolveProfile normalizes a user-supplied profile string to a Profile
// constant. Empty, whitespace, or unrecognized values fall back to
// ProfileAuto — agents often forget the field and "auto" is the right
// default; case-insensitive matching prevents `profile=Management` or
// similar from silently falling through edgeTypesForProfile's default
// branch and expanding to all edge types. Used by both REST and MCP
// surface handlers so they normalize identically.
func ResolveProfile(s string) Profile {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", string(ProfileAuto):
		return ProfileAuto
	case string(ProfileManagement):
		return ProfileManagement
	case string(ProfileNetworking):
		return ProfileNetworking
	case string(ProfilePolicy):
		return ProfilePolicy
	case string(ProfileSecurity):
		return ProfileSecurity
	case string(ProfileAll):
		return ProfileAll
	default:
		return ProfileAuto
	}
}

// NeighborhoodOptions configures a BuildNeighborhood call. Zero values are
// replaced with sensible defaults: Profile=auto, Hops=1, MaxNodes=25.
type NeighborhoodOptions struct {
	Profile  Profile
	Hops     int
	MaxNodes int

	// Allow gates each candidate node during BFS expansion. nil means
	// allow all. When non-nil, BFS skips nodes for which Allow returns
	// false — they don't consume the MaxNodes budget, can't appear as
	// path-fragments between two allowed nodes, and bump
	// Subgraph.RBACDenied so callers can report omissions to the user.
	// The root node is exempt — callers verify root access separately
	// before calling Build.
	//
	// This is the security boundary: forbidden nodes must not influence
	// the visible graph shape. Post-hoc filtering would let an
	// unauthorized node act as a bridge in the BFS frontier (or consume
	// truncation budget) before being dropped, leaking its existence
	// indirectly.
	Allow func(*Node) bool
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

	// RBACDenied counts nodes skipped during BFS because Allow returned
	// false. Reported as a single aggregated omission by the caller; we
	// intentionally do NOT track which specific nodes were denied, since
	// surfacing those names would re-introduce the existence-leak the
	// Allow gate exists to prevent.
	RBACDenied int `json:"-"`
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
//
// Thin shim over BuildNeighborhoodWithIndex(nil, nil) — every per-step neighbor
// enumeration falls back to a linear scan over t.Edges, and root resolution
// uses the lowercase-kind ID heuristic without CRD plural lookup. Hot callers
// that already hold a RelationshipsIndex + DynamicProvider (REST + MCP
// handlers) should call BuildNeighborhoodWithIndex directly to skip the index
// rebuild and to enable group-aware root ID construction for CRDs.
func BuildNeighborhood(t *Topology, root ResourceRef, opts NeighborhoodOptions) *Subgraph {
	return BuildNeighborhoodWithIndex(t, root, opts, nil, nil)
}

// BuildNeighborhoodWithIndex is the indexed variant of BuildNeighborhood.
// When idx is non-nil, per-node edge lookups go through idx.EdgesFor in O(1)
// (well, O(degree)) instead of pre-computing an adjacency map from t.Edges.
// When idx is nil, behavior matches BuildNeighborhood exactly: the BFS pays
// a single O(E) adjacency build up front, which is fine for one-shot library
// callers and tests.
//
// dp threads the topology's DynamicProvider into root ID construction so
// CRD plurals resolve correctly (e.g. "certificaterequests" → "certificaterequest").
// Without dp, buildNodeID's static kindMap covers the built-in kinds only —
// fine for tests but insufficient for ambiguous CRDs (CAPI vs CNPG Cluster).
// When root.Group is non-empty it's also used by findNodeByRef to disambiguate
// kind-collisions across API groups.
//
// Hot callers — REST handleAINeighborhood, MCP handleGetNeighborhood — fetch
// idx from Memoizer.GetIndex so the topology-wide inverted index is shared
// with GetRelationshipsWithIndex / SynthesizeManagedBy on the same request,
// and pass the same dp they used to build the topology.
func BuildNeighborhoodWithIndex(t *Topology, root ResourceRef, opts NeighborhoodOptions, idx *RelationshipsIndex, dp DynamicProvider) *Subgraph {
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

	// Use the same dp the topology was built with so CRD plurals resolve
	// (e.g. "certificaterequests" → "certificaterequest"). Without it,
	// buildNodeID's static map can't construct the right ID for arbitrary CRDs.
	rootID := buildNodeID(root.Kind, root.Namespace, root.Name, dp)
	rootNode, ok := nodeByID[rootID]
	// When root.Group is set, the rootID match alone isn't enough: two CRDs
	// sharing the same lowercase plural collide on the ID (e.g. CAPI
	// cluster.x-k8s.io/Cluster vs CNPG postgresql.cnpg.io/Cluster — whichever
	// was inserted last wins in nodeByID). Verify the candidate's apiGroup
	// matches; otherwise fall through to findNodeByRef which does the
	// group-aware tuple match.
	if ok && root.Group != "" && nodeAPIGroupFromData(rootNode) != root.Group {
		ok = false
	}
	if !ok {
		// Fallback: try matching by (kind, namespace, name [+ group]) tuple.
		// Mostly for CRDs whose topology node ID uses a different prefix than
		// the lowercase kind (e.g. "knativeservice/"). When root.Group is set,
		// findNodeByRef also disambiguates kind-collisions across API groups
		// (CAPI cluster.x-k8s.io/Cluster vs Fleet cluster.fleet.io/Cluster).
		rootNode = findNodeByRef(t.Nodes, root)
		if rootNode == nil {
			return sub
		}
		rootID = rootNode.ID
	}

	// Apply the Allow gate to the root itself. Callers' upfront RBAC checks
	// (REST: kind+namespace, MCP: kind+namespace) authorize the kind class but
	// don't catch per-kind tightening inside an allowed namespace — a Secret
	// root in a namespace the user can list still needs `get secrets` for
	// THAT namespace. Without this gate the root Secret leaks (name+status+
	// data preview) even though the matching neighbor-Secret would be hidden.
	//
	// Empty subgraph + RBACDenied=1 lets callers translate to 404 (same as
	// "root not in topology") so the response doesn't act as an existence
	// oracle: a user with namespace-list access but no `get secrets` can't
	// distinguish "Secret doesn't exist" from "Secret exists, you can't read".
	if opts.Allow != nil && !opts.Allow(rootNode) {
		sub.RBACDenied = 1
		return sub
	}

	allowedEdges := edgeTypesForProfile(opts.Profile, rootNode.Kind)

	// neighbors yields every edge touching id that passes the profile filter,
	// preferring the precomputed index when supplied. The undirected walk
	// matters: a Pod is the target of Service→Pod (exposes), but the agent
	// wants to find the Service from the Pod just as much as vice versa.
	//
	// Without idx we walk t.Edges once per BFS step. That's O(E) per step
	// instead of O(degree), but the index-free path is reserved for tests
	// and library callers without a Memoizer — hot REST/MCP traffic always
	// supplies idx.
	neighbors := func(id string) []Edge {
		if idx != nil {
			incoming, outgoing := idx.EdgesFor(id)
			// Filter both sides by allowed edge types. Concatenate into a
			// fresh slice so the caller can't accidentally mutate the
			// index's internal storage.
			out := make([]Edge, 0, len(incoming)+len(outgoing))
			for _, e := range outgoing {
				if allowedEdges[e.Type] {
					out = append(out, e)
				}
			}
			for _, e := range incoming {
				if allowedEdges[e.Type] {
					out = append(out, e)
				}
			}
			return out
		}
		var out []Edge
		for _, e := range t.Edges {
			if !allowedEdges[e.Type] {
				continue
			}
			if e.Source == id || e.Target == id {
				out = append(out, e)
			}
		}
		return out
	}

	// BFS by hop level. visited[id] = hop at which the node entered the
	// frontier, so we can stop when we'd exceed hops. RBAC-denied nodes
	// are skipped here (not post-filtered) so they neither consume budget
	// nor act as path-fragments to allowed nodes downstream.
	included := map[string]bool{rootID: true}
	order := []string{rootID}
	frontier := []string{rootID}
	truncated := false
	rbacDenied := 0
	deniedIDs := make(map[string]bool) // dedupe — same denied node may surface via multiple edges

	for hop := 0; hop < hops; hop++ {
		var next []string
		for _, id := range frontier {
			for _, e := range neighbors(id) {
				other := e.Source
				if other == id {
					other = e.Target
				}
				if included[other] {
					continue
				}
				candidate, exists := nodeByID[other]
				if !exists {
					// Edge dangles off a node that isn't in the topology.
					continue
				}
				if opts.Allow != nil && !opts.Allow(candidate) {
					if !deniedIDs[other] {
						deniedIDs[other] = true
						rbacDenied++
					}
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
	sub.RBACDenied = rbacDenied

	// Materialize nodes in BFS order so the root is always first and the
	// rest follow predictable expansion order.
	sub.Nodes = make([]Node, 0, len(order))
	for _, id := range order {
		if n, ok := nodeByID[id]; ok {
			sub.Nodes = append(sub.Nodes, *n)
		}
	}

	// Edges: include only edges whose type is allowed AND both endpoints
	// are in the included set. Walking t.Edges once here keeps the output
	// edge order stable regardless of whether we used the index or not —
	// tests and clients depend on that ordering.
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
//
// Empty and unrecognized profile values BOTH normalize to ProfileAuto.
// Returning ProfileAll for an unknown agent-supplied string would
// silently expand traversal to every edge type — far too aggressive for
// a typo. Auto picks a sensible profile based on the root kind instead.
// Callers wanting the broadest expansion must pass ProfileAll explicitly.
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
	case ProfileAll:
		return allEdgeTypes()
	case ProfileAuto, "":
		// Empty falls through here so direct lib callers that leave
		// Profile unset get the documented default (auto), matching what
		// the REST and MCP handlers already produce.
		return edgeTypesForAuto(rootKind)
	default:
		// Unknown profile string (typo, future profile name from an old
		// client): pick auto rather than the broadest possible expansion.
		return edgeTypesForAuto(rootKind)
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

// findNodeByRef looks up a node by (kind, namespace, name [+ group]). Used as
// a fallback when buildNodeID's lowercase-kind heuristic produces an ID that
// doesn't match a CRD-prefixed node ID (e.g. "knativeservice/").
//
// When ref.Group is non-empty, the candidate node's apiGroup (derived from
// Data["apiVersion"] via apiVersionGroupFromData) must match — this is how
// callers disambiguate kind-collisions across API groups, e.g. asking for the
// CAPI cluster.x-k8s.io/Cluster vs the KubeFleet cluster.fleet.io/Cluster
// when both share the same kind+namespace+name. When ref.Group is empty
// the group check is skipped (back-compat: callers that don't supply a group
// get the first matching node, same as before).
//
// Pseudo-kinds: some CRDs are stored in the topology under a synthesized kind
// distinct from their API kind (KnativeService for serving.knative.dev/Service,
// CAPICluster for cluster.x-k8s.io/Cluster, …). We map (ref.Kind, ref.Group)
// through pseudoKindFor BEFORE the direct kind comparison so a caller asking
// for kind=Service&group=serving.knative.dev finds the KnativeService topology
// node. Without this, the comparison Node.Kind="KnativeService" vs
// ref.Kind="Service" never matches and the root lookup silently fails.
func findNodeByRef(nodes []Node, ref ResourceRef) *Node {
	// Resolve the caller-facing (kind, group) tuple to the kind the topology
	// builder uses on Node.Kind. Falls back to ref.Kind unchanged when there's
	// no pseudo-kind mapping for this (kind, group).
	wantKind := pseudoKindFor(ref.Kind, ref.Group)
	for i := range nodes {
		n := &nodes[i]
		// Compare against the resolved pseudo-kind first; if that misses fall
		// back to the original ref.Kind so the back-compat path (callers that
		// don't supply a group, or kinds that aren't pseudo-mapped) still works.
		if !strings.EqualFold(string(n.Kind), wantKind) && !strings.EqualFold(string(n.Kind), ref.Kind) {
			continue
		}
		if n.Name != ref.Name {
			continue
		}
		ns := nodeNamespaceFromData(n)
		if ns != ref.Namespace {
			continue
		}
		if ref.Group != "" {
			if nodeAPIGroupFromData(n) != ref.Group {
				continue
			}
		}
		return n
	}
	return nil
}

// pseudoKindFor maps an (API kind, API group) to the synthesized topology
// kind the builder assigns when both differ. For (kind, group) pairs that
// aren't pseudo-mapped (or when group is empty), returns kind unchanged.
//
// Examples:
//
//	("Service",       "serving.knative.dev") → "KnativeService"
//	("Configuration", "serving.knative.dev") → "KnativeConfiguration"
//	("Cluster",       "cluster.x-k8s.io")    → "CAPICluster"
//	("Cluster",       "postgresql.cnpg.io")  → "Cluster"  (no pseudo-mapping)
//
// Keep this table in sync with the kinds the topology builder synthesizes —
// see the NodeKind constants prefixed Knative*/CAPI* in types.go. Missing an
// entry here means the caller-supplied API kind never matches the topology
// node, so the neighborhood root lookup silently returns "not found."
func pseudoKindFor(kind, group string) string {
	if group == "" {
		return kind
	}
	// Accept both singular and plural lowercase forms because REST hits
	// this with URL-path kinds (plural — "services") after normalizeKind
	// lowercases them, while MCP arrives in Pascal-case singular
	// ("Service"). Without case + plurality insensitivity, REST root
	// lookups silently 404 for any cross-group CRD whose plural collides
	// with a built-in (e.g. serving.knative.dev/Service vs core/v1
	// Service, cluster.x-k8s.io/Cluster vs other CRDs).
	switch group {
	case "serving.knative.dev":
		switch strings.ToLower(kind) {
		case "service", "services":
			return string(KindKnativeService)
		case "configuration", "configurations":
			return string(KindKnativeConfiguration)
		case "revision", "revisions":
			return string(KindKnativeRevision)
		case "route", "routes":
			return string(KindKnativeRoute)
		}
	case "cluster.x-k8s.io":
		switch strings.ToLower(kind) {
		case "cluster", "clusters":
			return string(KindCAPICluster)
		}
	case "networking.istio.io":
		// Istio Gateway lives under IstioGateway to avoid collision with
		// gateway.networking.k8s.io/Gateway.
		switch strings.ToLower(kind) {
		case "gateway", "gateways":
			return string(KindIstioGateway)
		}
	}
	return kind
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

// nodeAPIGroupFromData extracts the API group from a Node's Data map by
// splitting the apiVersion (e.g. "apps/v1" → "apps", "v1" → ""). Returns ""
// when no apiVersion is set on the node.
func nodeAPIGroupFromData(n *Node) string {
	if n.Data == nil {
		return ""
	}
	v, ok := n.Data["apiVersion"].(string)
	if !ok {
		return ""
	}
	for i := 0; i < len(v); i++ {
		if v[i] == '/' {
			return v[:i]
		}
	}
	return ""
}
