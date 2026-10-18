package server

import (
	"net/http"
	"sync/atomic"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/readscope"
	"github.com/skyhook-io/radar/internal/search"
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
		return "permission_check_failed", nil
	}
	if len(scanNamespaces) == 0 {
		return "skip", nil
	}
	scoped, authoritative := s.filterNamespacesByCanReadDecision(r, group, resource, "list", scanNamespaces)
	if !authoritative {
		return "permission_check_failed", scoped
	}
	if len(scoped) == 0 {
		return "skip", nil
	}
	if len(scoped) == len(scanNamespaces) {
		return "", nil
	}
	return "override", scoped
}

// searchKindAccess authorizes the kinds search gates per kind (Trivy reports)
// by the caller's own list permission. nil when auth is off. Decisions are
// memoized per user; uncached ones are capped per request
// (readscope.MaxAccessChecks), and namespaces past the cap come back unchecked.
func (s *Server) searchKindAccess(r *http.Request) *search.KindAccess {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		return nil
	}
	budget := new(atomic.Int32)
	budget.Store(readscope.MaxAccessChecks)
	return &search.KindAccess{
		ClusterWide: func(group, resource string) bool {
			return s.canRead(r, group, resource, "", "list")
		},
		InNamespaces: func(group, resource string, namespaces []string) ([]string, []string) {
			perms := s.permCache.Get(user.Username, user.Groups)
			return readscope.BudgetedNamespaceAccess(namespaces, budget,
				func(ns string) (bool, bool) {
					if perms == nil {
						return false, false
					}
					return perms.CanI("list", group, resource, ns)
				},
				func(ns string) (bool, bool) {
					return s.canReadDecision(r, group, resource, ns, "list")
				})
		},
	}
}
