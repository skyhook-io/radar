package server

import (
	"net/http"

	"github.com/skyhook-io/radar/internal/auth"
)

// computeSearchKindRBAC narrows sensitive typed namespaced scans using exact
// list permission, first cluster-wide and then through bounded namespace checks.
func (s *Server) computeSearchKindRBAC(r *http.Request, scanNamespaces []string, group, resource string) (decision string, scopedNamespaces []string) {
	if auth.UserFromContext(r.Context()) == nil {
		return "", nil
	}
	// scanNamespaces is already intersected with caller visibility. A denied
	// cluster-wide check may narrow that scope, never expand it; failed checks
	// fail closed without describing a transient error as an RBAC verdict.
	allowed, authoritative := s.canReadDecision(r, group, resource, "", "list")
	if allowed {
		return "", nil
	}
	if !authoritative {
		return "list_error", nil
	}
	if len(scanNamespaces) == 0 {
		return "skip", nil
	}
	scoped, authoritative := s.filterNamespacesByCanReadDecision(r, group, resource, "list", scanNamespaces)
	if !authoritative {
		return "list_error", scoped
	}
	if len(scoped) == 0 {
		return "skip", nil
	}
	if len(scoped) == len(scanNamespaces) {
		return "", nil
	}
	return "override", scoped
}
