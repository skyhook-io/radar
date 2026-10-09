package server

import (
	"net/http"
	"slices"

	integration "github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
)

func (s *Server) capacityNamespacesForUser(r *http.Request) []string {
	requested := parseNamespaces(r.URL.Query())
	if k8s.ForceNamespaceScope {
		target := k8s.GetNamespaceScopeTarget()
		switch {
		case target == "":
			return []string{}
		case requested == nil:
			requested = []string{target}
		case slices.Contains(requested, target):
			requested = []string{target}
		default:
			return []string{}
		}
	}
	return s.getUserNamespaces(r, requested)
}

func (s *Server) capacityNamespacesForSource(r *http.Request, namespaces []string, group, resource string) []string {
	if integration.NoNamespaceAccess(namespaces) {
		return namespaces
	}
	if namespaces == nil {
		if s.canRead(r, group, resource, "", "list") {
			return nil
		}
		return s.filterNamespacesByCanRead(r, group, resource, "list", allNamespaceNames())
	}
	return s.filterNamespacesByCanRead(r, group, resource, "list", namespaces)
}
