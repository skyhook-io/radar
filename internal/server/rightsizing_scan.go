package server

import (
	"context"
	"net/http"
	"time"

	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
)

const rightsizingScanTimeout = 45 * time.Second

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

// serverScanAuthorizer answers scope questions for the requesting user.
type serverScanAuthorizer struct {
	server  *Server
	request *http.Request
}

func (a serverScanAuthorizer) CanListAllNamespaces(resource string) bool {
	return a.server.canRead(a.request, "apps", resource, "", "list")
}

func (a serverScanAuthorizer) FilterNamespaces(resource string, namespaces []string) []string {
	return a.server.filterNamespacesByCanRead(a.request, "apps", resource, "list", namespaces)
}

func (s *Server) resolveRightsizingScanScope(r *http.Request, namespaces []string) prometheuspkg.RightsizingScanScope {
	// No readable namespace at all is reported as every kind restricted rather
	// than an empty scan, so the response carries why it found nothing.
	if noNamespaceAccess(namespaces) {
		scope := prometheuspkg.RightsizingScanScope{
			NamespacesByKind: make(map[string][]string, len(prometheuspkg.RightsizingScanKinds)),
		}
		for _, workloadKind := range prometheuspkg.RightsizingScanKinds {
			scope.RestrictedKinds = append(scope.RestrictedKinds, workloadKind.Kind)
		}
		return scope
	}
	return prometheuspkg.ResolveScanScope(namespaces, serverScanAuthorizer{server: s, request: r})
}

func hasExplicitNamespaceFilter(r *http.Request) bool {
	return len(parseNamespaces(r.URL.Query())) > 0
}
