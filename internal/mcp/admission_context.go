package mcp

import (
	"context"
	"time"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/meaningfulchanges"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	"k8s.io/apimachinery/pkg/runtime"
)

func mcpTemplateDivergence(ctx context.Context, obj runtime.Object) *resourcecontext.TemplateDivergence {
	return issues.NewCacheProvider().TemplateDivergence(obj, issueClusterScopedAccess(ctx), issueRelatedResourceAccess(ctx), func(verb, group, resource, namespace string) bool {
		return canReadInNamespace(ctx, group, resource, namespace, verb)
	})
}

func mcpClusterScopedChanges(ctx context.Context, namespace string, since time.Duration, quietStartup bool) *issuesapi.ClusterScopedChanges {
	nsLabels := meaningfulchanges.ReadNamespaceLabels(namespace, issueRelatedResourceAccess(ctx)(issues.Ref{Kind: "Namespace", Name: namespace}))
	return meaningfulchanges.ClusterScopedForNamespace(ctx, namespace, nsLabels, since, func(kind string) bool {
		if pkgauth.UserFromContext(ctx) == nil {
			return true
		}
		return k8s.ChangeReadAllowed(kind, "admissionregistration.k8s.io/v1", "", mcpChangeAuthorizer(ctx))
	}, quietStartup)
}
