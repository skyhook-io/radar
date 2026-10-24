package server

import (
	"net/http"
	"sync/atomic"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/search"
)

// readableTrivyNamespaces narrows namespaces (nil = every namespace) to those
// where the caller may list the Trivy report resource gvr, the way Secrets
// are narrowed. all reports that the caller may list it everywhere in scope,
// so namespaces need no narrowing. Otherwise the result is the readable
// subset, empty when there is none. A caller reading every namespace needs the
// cluster-wide grant: checking each namespace would cost one check per
// namespace per report kind.
func (s *Server) readableTrivyNamespaces(r *http.Request, gvr schema.GroupVersionResource, namespaces []string) (allowed []string, all bool) {
	if s.canRead(r, gvr.Group, gvr.Resource, "", "list") {
		return namespaces, true
	}
	readable := s.filterNamespacesByCanRead(r, gvr.Group, gvr.Resource, "list", namespaces)
	if readable == nil {
		// nil means every namespace to the callers; nothing readable is empty.
		readable = []string{}
	}
	return readable, false
}

// trivyCountAccess is readableTrivyNamespaces for count surfaces, which visit
// every report kind on each load. Fresh per-namespace checks share one
// request's budget (search.MaxAccessChecks); complete is false when a
// namespace went unchecked, so the caller reports the count as unavailable
// instead of a partial number. Later loads reuse the remembered decisions.
type trivyCountAccess struct {
	s      *Server
	r      *http.Request
	budget *atomic.Int32
}

func (s *Server) newTrivyCountAccess(r *http.Request) *trivyCountAccess {
	budget := new(atomic.Int32)
	budget.Store(search.MaxAccessChecks)
	return &trivyCountAccess{s: s, r: r, budget: budget}
}

func (a *trivyCountAccess) scope(gvr schema.GroupVersionResource, namespaces []string) (allowed []string, all, complete bool) {
	everywhere, answered := a.s.canReadDecision(a.r, gvr.Group, gvr.Resource, "", "list")
	if everywhere {
		return namespaces, true, true
	}
	if namespaces == nil {
		// A failed check is unknown, not a denial.
		return []string{}, false, answered
	}
	// A denied canRead means auth is on, so there is a user and a cache.
	user := auth.UserFromContext(a.r.Context())
	perms := a.s.permCache.Get(user.Username, user.Groups)
	allowed, unchecked := search.CheckNamespaces(namespaces, a.budget,
		func(ns string) (bool, bool) {
			if perms == nil {
				return false, false
			}
			return perms.CanI("list", gvr.Group, gvr.Resource, ns)
		},
		func(ns string) (bool, bool) {
			return a.s.canReadDecision(a.r, gvr.Group, gvr.Resource, ns, "list")
		})
	if allowed == nil {
		allowed = []string{}
	}
	return allowed, false, len(unchecked) == 0
}
