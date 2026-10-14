package server

import (
	"net/http"
	"slices"

	"github.com/skyhook-io/radar/internal/auth"
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

func (s *Server) filterStorageClaimDependentsForUser(r *http.Request, relationships *topology.Relationships) {
	if relationships == nil || auth.UserFromContext(r.Context()) == nil || !slices.ContainsFunc(relationships.Dependents, func(ref topology.ResourceRef) bool {
		return ref.Kind == "PersistentVolumeClaim" && ref.Group == ""
	}) {
		return
	}
	namespaces := s.getUserNamespaces(r, nil)
	if namespaces == nil {
		return
	}
	allowed := make(map[string]bool, len(namespaces))
	for _, namespace := range namespaces {
		allowed[namespace] = true
	}
	visible := make([]topology.ResourceRef, 0, len(relationships.Dependents))
	for _, ref := range relationships.Dependents {
		if ref.Kind == "PersistentVolumeClaim" && ref.Group == "" && !allowed[ref.Namespace] {
			continue
		}
		visible = append(visible, ref)
	}
	relationships.Dependents = visible
}
