package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
)

const rightsizingScanTimeout = 45 * time.Second

func (s *Server) handleRightsizingScan(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), rightsizingScanTimeout)
	defer cancel()
	r = r.WithContext(ctx)
	manager := prometheuspkg.RightsizingScans()
	generation := manager.Generation()
	id := chi.URLParam(r, "scanId")
	var namespaces []string
	if id != "" {
		original, err := manager.Namespaces(ctx, id, generation)
		if err != nil {
			s.writeRightsizingScanError(w, err)
			return
		}
		namespaces = s.getUserNamespaces(r, original)
	} else {
		namespaces = s.parseNamespacesForUser(r)
	}
	if noNamespaceAccess(namespaces) && (id != "" || hasExplicitNamespaceFilter(r)) {
		s.writeError(w, http.StatusForbidden, "no access to the requested namespace(s)")
		return
	}
	request := prometheuspkg.RightsizingScanRequest{
		Generation: generation, Namespaces: namespaces, Scope: s.resolveRightsizingScanScope(r, namespaces),
		ID: id, Start: r.Method == http.MethodPost, Refresh: r.Method == http.MethodPost, Cancel: r.Method == http.MethodDelete,
	}
	if r.Method == http.MethodPost {
		wait := 45
		if value := r.URL.Query().Get("wait_seconds"); value != "" {
			var err error
			wait, err = strconv.Atoi(value)
			if err != nil || wait < 0 || wait > 45 {
				s.writeError(w, http.StatusBadRequest, "wait_seconds must be an integer between 0 and 45")
				return
			}
		}
		request.Wait = time.Duration(wait) * time.Second
	}
	if request.Cancel {
		request.Wait = 5 * time.Second
	}
	result, err := manager.Resolve(ctx, request)
	if err != nil {
		s.writeRightsizingScanError(w, err)
		return
	}
	s.writeJSON(w, result)
}

func (s *Server) writeRightsizingScanError(w http.ResponseWriter, err error) {
	status := http.StatusServiceUnavailable
	switch {
	case errors.Is(err, prometheuspkg.ErrRightsizingScanNotFound):
		status = http.StatusNotFound
	case errors.Is(err, prometheuspkg.ErrRightsizingScanScopeChanged):
		status = http.StatusConflict
	case errors.Is(err, prometheuspkg.ErrRightsizingScanBusy):
		w.Header().Set("Retry-After", "5")
	}
	s.writeError(w, status, err.Error())
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
