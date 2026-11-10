package mcp

import (
	"context"
	"slices"

	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// ownershipAccess is what an MCP request's user may read: an exact get check
// per kind and namespace, as the resource context gates every reference it
// emits. Without authentication, everything.
func ownershipAccess(ctx context.Context, catalog *ownership.Catalog) ownership.Access {
	if pkgauth.UserFromContext(ctx) == nil {
		return ownership.Unrestricted()
	}
	return &mcpAccess{ctx: ctx, catalog: catalog, namespaces: filterNamespacesForUser(ctx, nil)}
}

type mcpAccess struct {
	ctx     context.Context
	catalog *ownership.Catalog
	// namespaces are the user's, nil for every one.
	namespaces []string
}

// CanRead's checks are memoized on the user's permissions, so asking again
// costs a lookup.
func (a *mcpAccess) CanRead(kind resourceid.GroupKind, namespace string) ownership.Decision {
	resource, namespaced := "", false
	if b, ok := resourceid.BuiltinForKind(kind.Kind); ok && b.Group == kind.Group {
		resource, namespaced = b.Resource, b.Namespaced
	} else if k, ok := a.catalog.Kind(kind); ok {
		resource, namespaced = k.Resource, k.Namespaced
	}
	switch {
	case resource == "" && a.catalog.Ambiguous(kind):
		// Two resources serve the kind, so no one grant decides it. Denied
		// would drop a gap on it, and an answer would claim to be complete.
		return ownership.Undecided
	case resource == "":
		// No served resource names it, so there is nothing to read: the read
		// says why. Denied would count its objects as hidden from the user.
		return ownership.Allowed
	case namespace == "" && namespaced:
		// In at least one of the user's namespaces: a cluster-wide grant
		// allows it; anything else would take a check per namespace.
		if a.check(kind.Group, resource, "") == ownership.Allowed {
			return ownership.Allowed
		}
		return ownership.Undecided
	case namespace != "" && a.namespaces != nil && !slices.Contains(a.namespaces, namespace):
		return ownership.Denied
	}
	return a.check(kind.Group, resource, namespace)
}

func (a *mcpAccess) check(group, resource, namespace string) ownership.Decision {
	allowed, decided := canReadInNamespaceDecision(a.ctx, group, resource, namespace, "get")
	switch {
	case !decided:
		return ownership.Undecided
	case allowed:
		return ownership.Allowed
	}
	return ownership.Denied
}

func (a *mcpAccess) Namespaces() ([]string, bool) { return a.namespaces, a.namespaces == nil }
