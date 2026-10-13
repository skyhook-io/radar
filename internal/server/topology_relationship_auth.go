package server

import (
	"context"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
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

// relationshipReadChecker follows the resource-detail gate, including namespace
// membership and the explicit checks for Secrets and cluster-scoped resources.
// Decisions are per tuple, never per reference name.
type relationshipReadChecker struct {
	s     *Server
	r     *http.Request
	cache map[string]bool
}

func (c *relationshipReadChecker) CanRead(_ context.Context, group, kind, namespace string) bool {
	key := group + "\x00" + kind + "\x00" + namespace
	if allowed, ok := c.cache[key]; ok {
		return allowed
	}

	_, _, allowed := c.s.preflightResourceGet(c.r, normalizeKind(kind), namespace, "", group)
	if group == "" && kind == "HelmRelease" {
		// Native Helm references are backed by release-storage Secrets.
		allowed = c.s.canRead(c.r, "", "secrets", namespace, "list")
		c.cache[key] = allowed
		return allowed
	}
	if namespace == "" {
		if clusterScoped, _, _ := k8s.ClassifyKindScope(kind, group); !clusterScoped {
			gvrGroup, resource := k8s.LookupResourceGVR(kind, group)
			if resource != "" {
				allowed = c.s.canRead(c.r, gvrGroup, resource, "", "get")
			}
		}
	}
	c.cache[key] = allowed
	return allowed
}

func (s *Server) newRelationshipReadChecker(r *http.Request) resourcecontext.RefAccessChecker {
	if auth.UserFromContext(r.Context()) == nil {
		return nil
	}
	return &relationshipReadChecker{s: s, r: r, cache: make(map[string]bool)}
}
