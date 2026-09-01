package opencost

import (
	"net/http"
	"slices"
	"sync/atomic"
)

// Scope is what the calling user is allowed to see cost data for. The cost
// endpoints read OpenCost metrics out of Prometheus under Radar's own
// identity, so nothing in the query path is bounded by the caller's RBAC —
// without this, a namespace-restricted user is served cluster-wide spend.
//
// Server.getUserNamespaces / Server.canRead are the concrete implementations;
// passing them via SetScopeResolver avoids an import cycle (server imports
// opencost, not the other way around).
type Scope struct {
	// Namespaces the caller may see. nil means unrestricted; an empty non-nil
	// slice means no namespace access at all. Mirrors
	// auth.FilterNamespacesForUser's nil-vs-empty contract, so the no-auth and
	// cluster-admin paths stay unrestricted by construction.
	Namespaces []string

	// CanReadNodes gates the cluster-scoped node cost breakdown. Node spend
	// has no per-namespace slice, so it is a separate grant rather than
	// something inferred from namespace access.
	CanReadNodes bool
}

// Restricted reports whether the caller sees less than the whole cluster.
func (s Scope) Restricted() bool { return s.Namespaces != nil }

// Allows reports whether namespace is within the caller's scope.
func (s Scope) Allows(namespace string) bool {
	return s.Namespaces == nil || slices.Contains(s.Namespaces, namespace)
}

// ScopeResolver derives the calling user's Scope from their request.
type ScopeResolver func(r *http.Request) Scope

var scopeResolver atomic.Pointer[ScopeResolver]

// SetScopeResolver installs the request-scoped authorization lookup. Pass nil
// to clear it (only appropriate for tests and the no-auth local path).
func SetScopeResolver(fn ScopeResolver) {
	if fn == nil {
		scopeResolver.Store(nil)
		return
	}
	scopeResolver.Store(&fn)
}

// scopeFor consults the installed resolver. With none installed the caller is
// unrestricted, so the gate stays strictly additive and never locks out the
// OSS no-auth path.
func scopeFor(r *http.Request) Scope {
	resolver := scopeResolver.Load()
	if resolver == nil {
		return Scope{CanReadNodes: true}
	}
	return (*resolver)(r)
}
