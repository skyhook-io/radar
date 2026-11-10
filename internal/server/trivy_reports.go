package server

import (
	"net/http"
	"sync/atomic"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/readscope"
)

// trivyCountAccess decides where the caller may list a Trivy report kind, for
// count surfaces, which visit every report kind on each load. A Trivy report
// needs the caller's own list permission, as a Secret does: an
// ExposedSecretReport quotes the leaked credential. A caller reading every
// namespace needs the cluster-wide grant: checking each namespace would cost
// one check per namespace per report kind. Fresh per-namespace checks share one
// request's budget (readscope.MaxAccessChecks); complete is false when a
// namespace went unchecked, so the caller reports the count as unavailable
// instead of a partial number. Later loads reuse the remembered decisions.
type trivyCountAccess struct {
	s      *Server
	r      *http.Request
	budget *atomic.Int32
}

func (s *Server) newTrivyCountAccess(r *http.Request) *trivyCountAccess {
	budget := new(atomic.Int32)
	budget.Store(readscope.MaxAccessChecks)
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
	allowed, unchecked := readscope.BudgetedNamespaceAccess(a.r.Context(), namespaces, a.budget,
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
