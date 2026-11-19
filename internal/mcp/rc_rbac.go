package mcp

import (
	"context"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/ownership"
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
	cache map[string]ownership.Decision
}

// newMCPRequestScopedChecker returns a checker scoped to a single MCP
// tool call. Not safe for concurrent use across calls.
func newMCPRequestScopedChecker(ctx context.Context) *requestScopedChecker {
	return &requestScopedChecker{
		ctx:   ctx,
		cache: make(map[string]ownership.Decision, 8),
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
func (c *requestScopedChecker) CanRead(ctx context.Context, group, kind, namespace string) bool {
	return c.DecideRead(ctx, group, kind, namespace) == ownership.Allowed
}

// DecideRead implements resourcecontext.RefAccessDecider: CanRead's check,
// Undecided when it couldn't be made.
func (c *requestScopedChecker) DecideRead(_ context.Context, group, kind, namespace string) ownership.Decision {
	key := "get|" + group + "|" + kind + "|" + namespace
	if v, ok := c.cache[key]; ok {
		return v
	}

	gvrGroup, resource := k8s.LookupResourceGVR(kind, group)
	if resource == "" {
		c.cache[key] = ownership.Allowed
		return ownership.Allowed
	}

	var allowed, decided bool
	if namespace == "" {
		allowed, decided = canReadClusterScopedKindDecision(c.ctx, kind, group, "get")
	} else {
		allowed, decided = canReadInNamespaceDecision(c.ctx, gvrGroup, resource, namespace, "get")
	}
	decision := ownership.Denied
	switch {
	case !decided:
		decision = ownership.Undecided
	case allowed:
		decision = ownership.Allowed
	}
	c.cache[key] = decision
	return decision
}

// workloadReadAccess adapts the request's checker to trace.Deps.CanReadWorkload.
func workloadReadAccess(ctx context.Context) func(group, kind, namespace string) bool {
	checker := newMCPRequestScopedChecker(ctx)
	return func(group, kind, namespace string) bool {
		return checker.CanRead(ctx, group, kind, namespace)
	}
}

// Compile-time assertion that requestScopedChecker satisfies the contract.
var _ resourcecontext.RefAccessDecider = (*requestScopedChecker)(nil)
