package server

import (
	"context"
	"net/http"
	"time"

	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
)

const rightsizingScanTimeout = 45 * time.Second

var rightsizingScanKinds = []struct {
	Kind     string
	Resource string
}{
	{Kind: "Deployment", Resource: "deployments"},
	{Kind: "StatefulSet", Resource: "statefulsets"},
	{Kind: "DaemonSet", Resource: "daemonsets"},
}

func (s *Server) handleRightsizingScan(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}

	namespaces := s.parseNamespacesForUser(r)
	if noNamespaceAccess(namespaces) && hasExplicitNamespaceFilter(r) {
		s.writeError(w, http.StatusForbidden, "no access to the requested namespace(s)")
		return
	}

	scope := s.resolveRightsizingScanScope(r, namespaces)
	ctx, cancel := context.WithTimeout(r.Context(), rightsizingScanTimeout)
	defer cancel()

	s.writeJSON(w, prometheuspkg.ScanRightsizing(ctx, scope))
}

func (s *Server) resolveRightsizingScanScope(r *http.Request, namespaces []string) prometheuspkg.RightsizingScanScope {
	scope := prometheuspkg.RightsizingScanScope{
		NamespacesByKind: make(map[string][]string, len(rightsizingScanKinds)),
	}
	if noNamespaceAccess(namespaces) {
		for _, workloadKind := range rightsizingScanKinds {
			scope.RestrictedKinds = append(scope.RestrictedKinds, workloadKind.Kind)
		}
		return scope
	}
	for _, workloadKind := range rightsizingScanKinds {
		if namespaces == nil {
			if s.canRead(r, "apps", workloadKind.Resource, "", "list") {
				scope.NamespacesByKind[workloadKind.Kind] = nil
			} else {
				scope.RestrictedKinds = append(scope.RestrictedKinds, workloadKind.Kind)
			}
			continue
		}

		allowed := s.filterNamespacesByCanRead(r, "apps", workloadKind.Resource, "list", namespaces)
		if len(allowed) > 0 {
			scope.NamespacesByKind[workloadKind.Kind] = allowed
		}
		if len(allowed) < len(namespaces) {
			scope.RestrictedKinds = append(scope.RestrictedKinds, workloadKind.Kind)
		}
	}
	return scope
}

func hasExplicitNamespaceFilter(r *http.Request) bool {
	return len(parseNamespaces(r.URL.Query())) > 0
}
