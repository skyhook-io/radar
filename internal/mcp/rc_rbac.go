package mcp

import (
	"context"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

// requestScopedChecker adapts the MCP-side RBAC helpers
// (canReadInNamespace / canReadClusterScopedKind) into
// resourcecontext.RefAccessChecker with a request-local memoization layer
// keyed on (verb, group, kind, namespace).
//
// A single resourceContext build emits ~30 candidate refs but only ~5
// distinct (group, kind, namespace) tuples — caching here collapses the
// SAR fan-out before reaching the inner per-user cache. Mirrors the REST
// equivalent in internal/server/rc_rbac.go so the two surfaces share the
// same enforcement story.
//
// Request-scoped (not server-scoped): per-user caching already lives one
// layer down. This layer only deduplicates the burst a single Build
// invocation generates.
type requestScopedChecker struct {
	ctx   context.Context
	cache map[string]bool
}

// newMCPRequestScopedChecker returns a checker scoped to a single MCP
// tool call. Not safe for concurrent use across calls.
func newMCPRequestScopedChecker(ctx context.Context) *requestScopedChecker {
	return &requestScopedChecker{
		ctx:   ctx,
		cache: make(map[string]bool, 8),
	}
}

// CanRead implements resourcecontext.RefAccessChecker.
//
// Authorization rules mirror the REST adapter:
//   - Namespaced kinds: SAR on (verb=get, group, resource, namespace).
//   - Cluster-scoped kinds (namespace == ""): SAR on (verb=get, group,
//     resource, "").
//   - Unknown kinds (not in discovery, not in static catalogue) pass
//     through — Build only emits refs whose kinds are known to the
//     topology builder, and an unknown kind here is a temporary
//     discovery-cold state, not a permission bypass vector.
func (c *requestScopedChecker) CanRead(_ context.Context, group, kind, namespace string) bool {
	key := "get|" + group + "|" + kind + "|" + namespace
	if v, ok := c.cache[key]; ok {
		return v
	}

	resource := lookupResourceName(kind, group)
	if resource == "" {
		c.cache[key] = true
		return true
	}

	var allowed bool
	if namespace == "" {
		allowed = canReadClusterScopedKind(c.ctx, kind, group, "get")
	} else {
		allowed = canReadInNamespace(c.ctx, group, resource, namespace, "get")
	}
	c.cache[key] = allowed
	return allowed
}

// Compile-time assertion that requestScopedChecker satisfies the contract.
var _ resourcecontext.RefAccessChecker = (*requestScopedChecker)(nil)

// lookupResourceName resolves a (kind, group) pair to the canonical plural
// resource name used by SubjectAccessReview. Tries the static cluster-only
// catalogue first (covers Nodes / ClusterRoles / etc.), then discovery for
// everything else including CRDs. Returns "" when neither path knows the
// kind. Mirrors internal/server/rc_rbac.go's helper of the same name.
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
