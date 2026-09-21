package server

import (
	"net/http"
	"slices"

	"github.com/skyhook-io/radar/pkg/topology"
)

func (s *Server) relationshipTopologyForUser(r *http.Request, shared *topology.Topology) *topology.Topology {
	filtered := cloneTopology(shared)
	namespaces := s.getUserNamespaces(r, nil)
	if namespaces != nil {
		denied := map[string]bool{}
		for _, node := range filtered.Nodes {
			ns, _ := node.Data["namespace"].(string)
			if ns != "" && !slices.Contains(namespaces, ns) {
				denied[node.ID] = true
			}
		}
		filtered.StripNodeIDs(denied)
	}
	s.applyClusterScopedTopologyRBAC(r, filtered)
	return filtered
}
