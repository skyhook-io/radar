package mcp

import (
	"context"
	"github.com/skyhook-io/radar/internal/audit"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
)

func auditOptions(ctx context.Context) *audit.RunOptions {
	if auth.UserFromContext(ctx) == nil {
		return nil
	}
	namespaces := filterNamespacesForUser(ctx, nil)
	secrets := namespaces
	if secrets == nil && !canReadInNamespace(ctx, "", "secrets", "", "list") {
		candidates := k8s.AllNamespaceNames()
		if len(candidates) == 0 {
			candidates, _ = k8s.GetAccessibleNamespaces(ctx)
		}
		secrets = append([]string{}, candidates...)
	}
	secrets = filterNamespacesByCanRead(ctx, "", "secrets", "list", secrets)
	scope := audit.ResolveReadScope(namespaces, secrets, func(group, resource, ns string) bool { return canReadInNamespace(ctx, group, resource, ns, "list") })
	return &audit.RunOptions{Scope: scope}
}
