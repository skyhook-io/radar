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
	// Topology pseudo-kinds (NodeClass, NodePool, NodeClaim, …) FIRST: these
	// are synthesized labels that ClassifyKindScope doesn't recognize ("nodeclass"
	// isn't a real K8s kind — the variants are EC2NodeClass / AKSNodeClass /
	// GCPNodeClass). Without this branch the call falls into the namespaced
	// arm below and 400s with "namespace is required" even though "_" was
	// supplied (URL → namespace == ""). Match shape mirrors the per-node gate
	// in canReadClusterScopedTopoKind: SAR each table entry, allow on any pass.
	if entries := topology.LookupClusterScopedTopoKind(rawKind, group); len(entries) > 0 {
		if !s.canReadClusterScopedTopoKindByName(r, entries) {
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
		// Mirror MCP via topology.ResolveProfile so both surfaces normalize
		// identically. A direct cast would let `?profile=Management` or
		// `?profile=garbage` fall through edgeTypesForProfile's default
		// case (allEdgeTypes), silently exposing more topology edges than
		// the caller intended.
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
// MCP equivalent — splits on namespace presence: namespaced reads use the
// per-user namespace filter; cluster-scoped reads go through canRead with
// the kind classified via ClassifyKindScope OR (for synthesized pseudo-
// kinds like NodeClass) via the clusterScopedTopologyKinds table.
//
// Secret nodes get an additional per-kind SAR inside the namespace: namespace
// access (e.g. "user can list pods in team-a") is NOT a sufficient signal for
// reading Secrets, because the SA the cache runs under may have cluster-wide
// secrets RBAC (Helm release visibility) while the user does not. This mirrors
// the same gate handleGetResource applies — without it, the neighborhood graph
// would leak Secret existence + names to users who can't fetch them directly.
func (s *Server) canReadNeighborhoodNode(r *http.Request, n *topology.Node) bool {
	ns := ""
	group := ""
	if n.Data != nil {
		if v, ok := n.Data["namespace"].(string); ok {
			ns = v
		}
		if v, ok := n.Data["apiVersion"].(string); ok {
			group = topology.APIVersionGroup(v)
		}
	}
	if ns != "" {
		allowed := s.getUserNamespaces(r, []string{ns})
		if noNamespaceAccess(allowed) {
			return false
		}
		// Per-kind tightening inside the namespace. v1 covers Secret only —
		// other namespaced kinds (Pods, ConfigMaps, …) ride on the namespace
		// gate. Add new entries here when a kind's namespace-list discovery
		// stops being a sufficient signal for that kind's read permission.
		if n.Kind == topology.KindSecret {
			return s.canRead(r, "", "secrets", ns, "get")
		}
		return true
	}
	// Cluster-scoped: check the topology pseudo-kind table FIRST. Pseudo-kinds
	// like NodeClass (synthesized from EC2NodeClass / AKSNodeClass / GCPNodeClass)
	// don't classify under ClassifyKindScope — its argument is the real K8s
	// kind, and "NodeClass" is a topology-only label. Without this branch
	// pseudo-kind nodes hit the unclassified+empty-namespace fallback below
	// and are surfaced unconditionally, leaking cluster-scoped existence to
	// users who can't list any provider variant.
	if hit, ok := s.canReadClusterScopedTopoKind(r, n); ok {
		return hit
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

// canReadClusterScopedTopoKind authorizes a topology cluster-scoped pseudo-
// kind (NodeClass, NodePool, …) against the SPECIFIC provider variant the
// node represents. The node's apiVersion (set by the topology builder when
// synthesizing the node) carries the provider group — e.g.
// "karpenter.k8s.aws/v1" for an EC2 NodeClass. We look up the matching
// (Kind, Group) row in topology.ClusterScopedKinds and SAR that single
// row, so a user with EC2 RBAC sees EC2 NodeClass nodes only — not AKS or
// GCP variants on a multi-provider cluster.
//
// Returns (allowed, true) when n.Kind is a pseudo-kind tracked by the table,
// or (_, false) when n.Kind isn't known so the caller can fall back to the
// ClassifyKindScope path.
//
// For single-entry kinds (NodePool, GatewayClass, PV, StorageClass, etc.)
// this collapses to a plain SAR on that one entry — the group-match still
// works because there's exactly one row to find.
//
// Edge case: node's apiVersion-group doesn't match any entry under the
// pseudo-kind. The topology builder only emits these nodes for known
// providers, so this shouldn't happen in practice. We deny to avoid
// surfacing an un-SARed node (which would be the failure mode the
// per-variant fix exists to close).
func (s *Server) canReadClusterScopedTopoKind(r *http.Request, n *topology.Node) (allowed, matched bool) {
	// Find any entry under this NodeKind first so we can return matched=false
	// for non-tracked kinds (caller falls back to ClassifyKindScope).
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

	// Group is derived from the node's apiVersion (set by the topology
	// builder). For NodeClass nodes this is "karpenter.k8s.aws" /
	// "karpenter.azure.com" / "karpenter.k8s.gcp" — the discriminator that
	// picks the right table row.
	group := ""
	if n.Data != nil {
		if v, ok := n.Data["apiVersion"].(string); ok {
			group = topology.APIVersionGroup(v)
		}
	}

	disc := k8s.GetResourceDiscovery()
	for _, ck := range topology.ClusterScopedKinds {
		if ck.Kind != n.Kind || ck.Group != group {
			continue
		}
		if ck.Group != "" && disc != nil {
			if _, ok := disc.GetResourceWithGroup(ck.Resource, ck.Group); !ok {
				// Pseudo-kind tracked + apiVersion match but no provider
				// variant is in discovery (CRD removed mid-build). Allow:
				// the topology builder wouldn't have surfaced this node for
				// an unprivileged SA either, and over-denying would silently
				// hide a node the cluster admin can see.
				return true, true
			}
		}
		return s.canRead(r, ck.Group, ck.Resource, "", "get"), true
	}
	// Tracked kind but the node's apiVersion-group doesn't match any entry.
	// Deny rather than fall through to allow — the per-variant gate exists
	// precisely to stop unmapped variants from leaking.
	return false, true
}

// canReadClusterScopedTopoKindByName authorizes a topology cluster-scoped
// pseudo-kind at root preflight, BEFORE the topology graph has resolved a
// concrete node (so no node.apiVersion to pick a single provider variant).
// The caller supplies the URL kind + optional ?group= query param; the table
// lookup in topology.LookupClusterScopedTopoKind returns every row matching
// that (kind, group) tuple — one row when group is supplied, all rows under
// the kind when it isn't.
//
// We SAR each candidate row and allow on the FIRST pass. Matches the
// topology-strip semantics: "user with EC2 RBAC sees NodeClass nodes" must
// hold at the root level too — otherwise an agent calling
// /api/ai/neighborhood/nodeclass/_/default with EC2-only RBAC would 403
// at preflight even though the per-node Allow gate would let the same
// nodes through.
//
// Discovery filter: when the row has a non-empty Group and the resource is
// MISSING from discovery (CRD removed mid-build or not installed in this
// cluster), skip the row. This matches canReadClusterScopedTopoKind — over-
// denying on a provider absent from the cluster would silently hide nodes
// the admin can see.
//
// Edge case: all matching rows fail discovery (every provider variant absent
// from the cluster). The table-lookup hit means the kind IS tracked; we fall
// through to allow because the topology builder wouldn't have surfaced any
// node for an unprivileged SA either — matching the per-node behavior at
// canReadClusterScopedTopoKind's "CRD removed mid-build" branch.
func (s *Server) canReadClusterScopedTopoKindByName(r *http.Request, entries []topology.ClusterScopedKindEntry) bool {
	disc := k8s.GetResourceDiscovery()
	considered := 0
	for _, ck := range entries {
		if ck.Group != "" && disc != nil {
			if _, ok := disc.GetResourceWithGroup(ck.Resource, ck.Group); !ok {
				continue
			}
		}
		considered++
		if s.canRead(r, ck.Group, ck.Resource, "", "get") {
			return true
		}
	}
	// Tracked kind but every variant was filtered out by discovery → allow.
	// See edge-case rationale above.
	if considered == 0 {
		return true
	}
	return false
}

