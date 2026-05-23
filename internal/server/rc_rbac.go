package server

import (
	"context"
	"net/http"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

// requestScopedChecker adapts Server.canRead into resourcecontext.RefAccessChecker
// with a request-local memoization layer keyed on (verb, group, kind, namespace).
//
// A single resourceContext build emits ~30 candidate refs but only ~5 distinct
// (group, kind, namespace) tuples — most workloads point at ConfigMaps and
// Secrets in their own namespace, plus a ServiceAccount and a Node. Caching
// here collapses the SAR fan-out before reaching s.canRead's per-user cache.
//
// The map is intentionally request-scoped (not server-scoped): server-scoped
// caching is already in pkg/auth.PermissionCache (2-min TTL) and reused via
// s.canRead. The per-request layer exists only to deduplicate the burst this
// builder generates within a single response.
type requestScopedChecker struct {
	s     *Server
	req   *http.Request
	cache map[string]bool
}

// newRequestScopedChecker returns a checker scoped to a single HTTP request.
// Not safe for concurrent use across requests; each handler invocation MUST
// construct its own checker.
func (s *Server) newRequestScopedChecker(r *http.Request) *requestScopedChecker {
	return &requestScopedChecker{
		s:     s,
		req:   r,
		cache: make(map[string]bool, 8),
	}
}

// CanRead implements resourcecontext.RefAccessChecker.
//
// Authorization rules:
//   - Namespaced kinds: SAR on (verb=get, group, resource, namespace).
//   - Cluster-scoped kinds (namespace == ""): SAR on (verb=get, group, resource, "").
//   - Unknown kinds (not in discovery, not in static catalogue) pass through —
//     mirrors the rest of the codebase's unknown-kind passthrough semantics.
//     This is safe because Build only emits refs whose kinds are known to the
//     topology builder (which itself uses discovery); a kind unknown here is a
//     temporary discovery-cold state, not a permission bypass vector.
func (c *requestScopedChecker) CanRead(_ context.Context, group, kind, namespace string) bool {
	key := "get|" + group + "|" + kind + "|" + namespace
	if v, ok := c.cache[key]; ok {
		return v
	}

	resource := lookupResourceName(kind, group)
	if resource == "" {
		// Unknown kind — passthrough. See doc comment for rationale.
		c.cache[key] = true
		return true
	}

	allowed := c.s.canRead(c.req, group, resource, namespace, "get")
	c.cache[key] = allowed
	return allowed
}

// Compile-time assertion that requestScopedChecker satisfies the contract.
var _ resourcecontext.RefAccessChecker = (*requestScopedChecker)(nil)

// lookupResourceName resolves a (kind, group) pair to the canonical plural
// resource name used by SubjectAccessReview. Tries the static cluster-only
// catalogue (covers Nodes / ClusterRoles / etc.), then discovery for everything
// else including CRDs. Returns "" when neither path knows the kind.
func lookupResourceName(kind, group string) string {
	if kind == "" {
		return ""
	}
	if g, r, ok := k8s.ClusterOnlyKindGVR(kind); ok && (group == "" || group == g) {
		return r
	}
	if disc := k8s.GetResourceDiscovery(); disc != nil {
		if ar, ok := disc.GetResourceWithGroup(kind, group); ok {
			return ar.Name
		}
	}
	return ""
}
