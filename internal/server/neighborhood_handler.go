package server

import (
	"log"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	"github.com/skyhook-io/radar/pkg/topology"
)

// neighborhoodResponse is the wire shape returned by GET /api/ai/neighborhood.
// It deliberately differs from topology.Subgraph in two ways:
//
//   - root + truncated are lifted to top-level for easy parsing
//   - omitted carries resourcecontext-style RBAC drop records so agents can
//     tell when authorized context is incomplete
type neighborhoodResponse struct {
	Root      topology.ResourceRef           `json:"root"`
	Subgraph  neighborhoodSubgraph           `json:"subgraph"`
	Truncated bool                           `json:"truncated"`
	Omitted   []resourcecontext.OmittedField `json:"omitted,omitempty"`
}

type neighborhoodSubgraph struct {
	Nodes []topology.Node `json:"nodes"`
	Edges []topology.Edge `json:"edges"`
}

// handleAINeighborhood returns the BFS-expanded neighborhood of a root
// resource. See pkg/topology.BuildNeighborhood for the graph semantics.
//
// GET /api/ai/neighborhood/{kind}/{namespace}/{name}
//
//	?profile=auto|all  (default: auto)
//	?hops=1|2          (default: 1)
//	?max_nodes=25      (default: 25)
//
// Cluster-scoped roots use "_" as the namespace placeholder (same convention
// as handleAIGetResource).
func (s *Server) handleAINeighborhood(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}

	rawKind := chi.URLParam(r, "kind")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	if namespace == "_" {
		namespace = ""
	}
	if rawKind == "" || name == "" {
		s.writeError(w, http.StatusBadRequest, "kind and name are required")
		return
	}

	// RBAC for the root.
	group := r.URL.Query().Get("group")
	// Topology pseudo-kinds (NodeClass, NodePool, NodeClaim, …) FIRST: these
	// are synthesized labels that ClassifyKindScope doesn't recognize ("nodeclass"
	// isn't a real K8s kind — the variants are EC2NodeClass / AKSNodeClass /
	// GCPNodeClass). Without this branch the call falls into the namespaced
	// arm below and 400s with "namespace is required" even though "_" was
	// supplied (URL → namespace == ""). topology.RBACTuplesForKind returns the
	// per-variant SAR tuples — we iterate through s.canRead and allow on any
	// pass, matching the per-node gate's first-success semantics.
	if pseudoTuples, tracked, fallthroughAllow := topology.RBACTuplesForKind(rawKind, group, pseudoKindDiscoveryLookup()); tracked {
		if !s.allowPseudoKindTuples(r, pseudoTuples, fallthroughAllow) {
			s.writeError(w, http.StatusForbidden, "insufficient permissions for cluster-scoped "+rawKind)
			return
		}
	} else if rootClusterScoped, gvrGroup, gvrResource := k8s.ClassifyKindScope(rawKind, group); rootClusterScoped {
		if !s.canRead(r, gvrGroup, gvrResource, "", "get") {
			s.writeError(w, http.StatusForbidden, "insufficient permissions for cluster-scoped "+rawKind)
			return
		}
	} else {
		if namespace == "" {
			s.writeError(w, http.StatusBadRequest, "namespace is required for namespaced kinds (use '_' for cluster-scoped)")
			return
		}
		allowed := s.getUserNamespaces(r, []string{namespace})
		if noNamespaceAccess(allowed) {
			s.writeError(w, http.StatusForbidden, "no access to namespace "+namespace)
			return
		}
	}

	opts := parseNeighborhoodOptions(r)

	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}

	// Build the full topology (memoized) and let BuildNeighborhood do the
	// BFS slice. Cheaper than reaching into builder internals; topoMemo
	// dedupes concurrent calls. We also fetch the cached RelationshipsIndex
	// so the BFS expansion uses O(degree) edge lookups instead of paying
	// an O(E) adjacency-build per request.
	//
	// dp is captured once and threaded into both Builder and BuildNeighborhoodWithIndex
	// so root-ID construction can resolve CRD plurals correctly (without it,
	// buildNodeID falls back to the static kindMap which only covers built-in kinds).
	dp := k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery())
	buildOpts := topology.DefaultBuildOptions()
	buildOpts.IncludeReplicaSets = true
	buildOpts.ForRelationshipCache = true
	// Override the DefaultBuildOptions Secret-elision: without this a request
	// for kind=secret resolves to an empty subgraph (root missing in topology)
	// and 404s even for authorized users. The Allow gate below applies the
	// per-namespace `get secrets` SAR per node, so unauthorized users still
	// get a 404 via the empty-subgraph path — existence-hiding preserved.
	//
	// The Memoizer keys on a hash that includes IncludeSecrets (see
	// pkg/topology/memo.go memoKey), so this lives in a separate cache slot
	// from the IncludeSecrets=false topology used elsewhere.
	buildOpts.IncludeSecrets = true
	build := func() (*topology.Topology, error) {
		return topology.NewBuilder(k8s.NewTopologyResourceProvider(cache)).
			WithDynamic(dp).
			Build(buildOpts)
	}
	topo, err := s.topoMemo.Get(buildOpts, build)
	if err != nil {
		log.Printf("[neighborhood] Failed to build topology for %s %s/%s: %v", rawKind, namespace, name, err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// GetIndex piggybacks on the memo entry just populated by Get above —
	// the index is computed once per topology refresh and reused across
	// requests, matching the relationships hot path.
	idx, err := s.topoMemo.GetIndex(buildOpts, build)
	if err != nil {
		log.Printf("[neighborhood] Failed to fetch topology index for %s %s/%s: %v", rawKind, namespace, name, err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	root := topology.ResourceRef{
		Kind:      normalizeKind(rawKind),
		Namespace: namespace,
		Name:      name,
		Group:     group,
	}

	// Push RBAC into the BFS expansion itself. If we filtered post-hoc,
	// a forbidden node could still influence which allowed nodes surface
	// (acting as a path-fragment between two readable endpoints) and
	// could consume the MaxNodes truncation budget before being dropped.
	// Skipping during traversal keeps the visible graph independent of
	// hidden nodes — both for security and for predictable truncation.
	opts.Allow = func(n *topology.Node) bool {
		return s.canReadNeighborhoodNode(r, n)
	}

	sub := topology.BuildNeighborhoodWithIndex(topo, root, opts, idx, dp)
	if sub.AmbiguousRoot {
		s.writeError(w, http.StatusBadRequest, "resource kind is ambiguous; provide group")
		return
	}
	if len(sub.Nodes) == 0 {
		s.writeError(w, http.StatusNotFound, "resource not found in topology")
		return
	}

	// Use the resolved root node's Kind for the response, not the
	// URL-derived (lowercase) form. Subgraph nodes carry display-form
	// NodeKind values ("Pod", "KnativeService") — without this rewrite,
	// the response's root.kind would be lowercase while
	// subgraph.nodes[0].kind is display-form, breaking case-sensitive
	// within-response matching and diverging from MCP's shape despite
	// the header comment claiming both surfaces "parse identically".
	rootResp := root
	rootResp.Kind = string(sub.Nodes[0].Kind)

	resp := neighborhoodResponse{
		Root: rootResp,
		Subgraph: neighborhoodSubgraph{
			Nodes: sub.Nodes,
			Edges: sub.Edges,
		},
		Truncated: sub.Truncated,
	}
	if sub.RBACDenied > 0 {
		// Single aggregated omission rather than per-node entries —
		// surfacing the specific names of denied nodes would defeat the
		// existence-hiding guarantee the pre-filter provides.
		resp.Omitted = append(resp.Omitted, resourcecontext.OmittedField{
			Field:  "subgraph.nodes",
			Reason: resourcecontext.OmittedRBACDenied,
		})
	}

	s.writeJSON(w, resp)
}

// parseNeighborhoodOptions reads the query string into NeighborhoodOptions
// with defaults (auto, hops=1, max_nodes=25) and clamps (hops max 2, max_nodes
// floor 1 / ceiling 200) applied.
func parseNeighborhoodOptions(r *http.Request) topology.NeighborhoodOptions {
	q := r.URL.Query()
	opts := topology.NeighborhoodOptions{
		Profile:  topology.ProfileAuto,
		Hops:     1,
		MaxNodes: 25,
	}
	if p := q.Get("profile"); p != "" {
		// Mirror MCP via topology.ResolveProfile so both surfaces normalize
		// identically. Unknown values fall back to auto instead of silently
		// broadening traversal to all edges.
		opts.Profile = topology.ResolveProfile(p)
	}
	if h := q.Get("hops"); h != "" {
		if n, err := strconv.Atoi(h); err == nil && n > 0 {
			opts.Hops = n
		}
	}
	if m := q.Get("max_nodes"); m != "" {
		if n, err := strconv.Atoi(m); err == nil && n > 0 {
			opts.MaxNodes = n
		}
	}
	// Top-end clamps on both Hops and MaxNodes — keep responses bounded for
	// agent contexts regardless of what the caller asks for. BFS also clamps
	// internally (neighborhoodMaxHops), but doing it here too matches the
	// doc above, keeps the two budget fields symmetric, and means
	// opts.Hops is correct if anything inspects/logs it before BFS.
	if opts.Hops > 2 {
		opts.Hops = 2
	}
	if opts.MaxNodes > 200 {
		opts.MaxNodes = 200
	}
	return opts
}

// canReadNeighborhoodNode is the REST-side per-node RBAC gate. Mirrors the
// MCP equivalent canReadNeighborhoodNodeMCP — same decision tree, different
// per-user check function. The tuple-selection logic itself lives in
// topology.RBACTuplesForNode so the two surfaces stay in lockstep when the
// pseudo-kind table or Secret-tightening rules evolve.
//
// Decision dispatch (see topology.NodeRBACDecision for the four cases):
//
//   - Namespaced node: run protocol-specific namespace-list gate first, then
//     dispatch on the topology decision. Allow + CheckTuples both require
//     the namespace gate to have passed; Deny short-circuits before it.
//   - Cluster-scoped node: namespace-list gate doesn't apply; dispatch
//     directly.
//
// Secret nodes get an additional per-kind SAR inside the namespace: namespace
// access (e.g. "user can list pods in team-a") is NOT a sufficient signal for
// reading Secrets, because the SA the cache runs under may have cluster-wide
// secrets RBAC (Helm release visibility) while the user does not. This mirrors
// the same gate handleGetResource applies — without it, the neighborhood graph
// would leak Secret existence + names to users who can't fetch them directly.
func (s *Server) canReadNeighborhoodNode(r *http.Request, n *topology.Node) bool {
	// Namespace-list gate is protocol-specific (per-user state); apply it
	// here for namespaced nodes BEFORE consulting the shared helper.
	if n != nil && n.Data != nil {
		if ns, ok := n.Data["namespace"].(string); ok && ns != "" {
			allowed := s.getUserNamespaces(r, []string{ns})
			if noNamespaceAccess(allowed) {
				return false
			}
		}
	}

	decision, tuples := topology.RBACTuplesForNode(n, pseudoKindDiscoveryLookup())
	switch decision {
	case topology.NodeRBACAllow:
		return true
	case topology.NodeRBACDeny:
		return false
	case topology.NodeRBACCheckTuples:
		for _, t := range tuples {
			if s.canRead(r, t.Group, t.Resource, t.Namespace, "get") {
				return true
			}
		}
		return false
	case topology.NodeRBACConsultClassifyKindScope:
		// Cluster-scoped node that isn't a tracked pseudo-kind. Fall back
		// to the regular static-catalogue / discovery path. Unclassified
		// kinds (returns false) allow-through: the topology graph wouldn't
		// have surfaced the node for an unprivileged SA either.
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
		return s.canRead(r, gvrGroup, gvrResource, "", "get")
	default:
		// New decision values must be handled explicitly; default-deny is
		// the security-safe fallthrough.
		return false
	}
}

// pseudoKindDiscoveryLookup returns the function form of the discovery
// singleton that topology.RBACTuplesForKind / RBACTuplesForNode expect, or
// nil when discovery isn't initialised (test envs). Centralised here to
// sidestep the typed-nil-into-interface gotcha — see the doc on
// topology.PseudoKindDiscoveryLookup for the full rationale.
func pseudoKindDiscoveryLookup() topology.PseudoKindDiscoveryLookup {
	disc := k8s.GetResourceDiscovery()
	if disc == nil {
		return nil
	}
	return disc.GetResourceWithGroup
}

// allowPseudoKindTuples authorizes a list of per-variant SAR tuples returned
// by topology.RBACTuplesForKind for the root-preflight path. Iterates each
// tuple through s.canRead and allows on the first pass; if the helper
// returned zero tuples + fallthroughAllow=true (every variant was filtered
// out by discovery), allow — matches the pre-existing "over-include on
// absent provider variants" behavior.
func (s *Server) allowPseudoKindTuples(r *http.Request, tuples []topology.SARTuple, fallthroughAllow bool) bool {
	if len(tuples) == 0 {
		return fallthroughAllow
	}
	for _, t := range tuples {
		if s.canRead(r, t.Group, t.Resource, t.Namespace, "get") {
			return true
		}
	}
	return false
}
