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
func (s *Server) computeSearchKindRBAC(r *http.Request) func([]string, string, string) ([]string, []string, []string) {
	budget := new(atomic.Int32)
	budget.Store(readscope.MaxAccessChecks)
	return func(namespaces []string, group, resource string) (allowed, denied, unchecked []string) {
		user := auth.UserFromContext(r.Context())
		if user == nil {
			return namespaces, nil, nil
		}
		perms := s.permCache.Get(user.Username, user.Groups)
		return readscope.ClusterFirstNamespaceAccess(r.Context(), namespaces, budget,
			func(ns string) (bool, bool) {
				if perms == nil {
					return false, false
				}
				return perms.CanI("list", group, resource, ns)
			},
			func(ns string) (bool, bool) {
				return s.canReadDecision(r, group, resource, ns, "list")
			})
	}
}

// searchKindAccess authorizes the kinds search gates per kind (Trivy reports)
// by the caller's own list permission. nil when auth is off. Decisions are
// memoized per user; uncached ones are capped per request
// (readscope.MaxAccessChecks), and namespaces past the cap come back unchecked.
func (s *Server) searchKindAccess(r *http.Request, gate func([]string, string, string) ([]string, []string, []string)) *search.DynamicKindAccess {
	if auth.UserFromContext(r.Context()) == nil {
		return nil
	}
	return &search.DynamicKindAccess{
		ClusterWide: func(group, resource string) (bool, bool) {
			_, denied, unchecked := gate(nil, group, resource)
			return len(denied) == 0 && len(unchecked) == 0, len(unchecked) == 0
		},
		InNamespaces: func(group, resource string, namespaces []string) ([]string, []string) {
			allowed, _, unchecked := gate(namespaces, group, resource)
			return allowed, unchecked
		},
	}
}
