package mcp

import (
	"context"

	"github.com/skyhook-io/radar/internal/audit"
	"github.com/skyhook-io/radar/internal/auth"
)

func auditOptions(ctx context.Context) *audit.RunOptions {
	if auth.UserFromContext(ctx) == nil {
		return nil
	}
	namespaces := filterNamespacesForUser(ctx, nil)
	secrets := namespaces
	if secrets == nil && !canReadInNamespace(ctx, "", "secrets", "", "list") {
		secrets = append([]string{}, mcpAllNamespaceNames(ctx)...)
	}
	secrets = filterNamespacesByCanRead(ctx, "", "secrets", "list", secrets)
	scope := audit.ResolveReadScope(namespaces, secrets, func(group, resource, ns string) bool { return canReadInNamespace(ctx, group, resource, ns, "list") })
	return &audit.RunOptions{Scope: scope}
}
