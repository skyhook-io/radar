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
//   - omitted carries resourcecontext-style drop records (RBAC, budget) so
//     agents can tell why a neighbor isn't present
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
//	?profile=management|networking|policy|security|all|auto  (default: auto)
//	?hops=1|2                                                (default: 1)
//	?max_nodes=25                                            (default: 25)
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
	rootClusterScoped, gvrGroup, gvrResource := k8s.ClassifyKindScope(rawKind, group)
	if rootClusterScoped {
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
	buildOpts := topology.DefaultBuildOptions()
	buildOpts.IncludeReplicaSets = true
	buildOpts.ForRelationshipCache = true
	build := func() (*topology.Topology, error) {
		return topology.NewBuilder(k8s.NewTopologyResourceProvider(cache)).
			WithDynamic(k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery())).
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

	sub := topology.BuildNeighborhoodWithIndex(topo, root, opts, idx)
	if len(sub.Nodes) == 0 {
		s.writeError(w, http.StatusNotFound, "resource not found in topology")
		return
	}

	resp := neighborhoodResponse{
		Root: root,
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
	if sub.Truncated {
		resp.Omitted = append(resp.Omitted, resourcecontext.OmittedField{
			Field:  "subgraph.nodes",
			Reason: resourcecontext.OmittedBudgetExceeded,
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
		opts.Profile = topology.Profile(p)
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
	// Top-end clamp on MaxNodes — keep responses bounded for agent contexts
	// regardless of what the caller asks for.
	if opts.MaxNodes > 200 {
		opts.MaxNodes = 200
	}
	return opts
}

// canReadNeighborhoodNode is the REST-side per-node RBAC gate. Mirrors the
// MCP equivalent — splits on namespace presence: namespaced reads use the
// per-user namespace filter; cluster-scoped reads go through canRead with
// the kind classified via ClassifyKindScope.
func (s *Server) canReadNeighborhoodNode(r *http.Request, n *topology.Node) bool {
	ns := ""
	group := ""
	if n.Data != nil {
		if v, ok := n.Data["namespace"].(string); ok {
			ns = v
		}
		if v, ok := n.Data["apiVersion"].(string); ok {
			group = apiVersionGroup(v)
		}
	}
	if ns != "" {
		allowed := s.getUserNamespaces(r, []string{ns})
		return !noNamespaceAccess(allowed)
	}
	clusterScoped, gvrGroup, gvrResource := k8s.ClassifyKindScope(string(n.Kind), group)
	if !clusterScoped {
		// Unclassified node with no namespace — fall back to "allow" since
		// the topology graph wouldn't have surfaced it for an unprivileged
		// SA, and we don't want to silently drop legitimate kinds we forgot
		// to register.
		return true
	}
	return s.canRead(r, gvrGroup, gvrResource, "", "get")
}

// apiVersionGroup extracts the group from a Kubernetes apiVersion string.
// "v1" → "", "apps/v1" → "apps", "argoproj.io/v1alpha1" → "argoproj.io".
func apiVersionGroup(apiVersion string) string {
	for i := 0; i < len(apiVersion); i++ {
		if apiVersion[i] == '/' {
			return apiVersion[:i]
		}
	}
	return ""
}
