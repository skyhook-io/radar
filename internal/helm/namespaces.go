package helm

import (
	"context"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

// ResolveNoAuthListNamespaces returns a namespace fanout set for Helm list-style
// reads in no-auth mode. nil means keep the single cluster-wide Helm list.
func ResolveNoAuthListNamespaces(ctx context.Context) []string {
	accessible, _ := k8s.GetAccessibleNamespaces(ctx)
	if len(accessible) > 0 {
		allowed, apiErr := k8score.CanI(ctx, k8s.GetClient(), "", "", "secrets", "list")
		if !apiErr && allowed {
			return nil
		}
		return accessible
	}
	return nil
}
