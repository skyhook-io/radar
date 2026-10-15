package cnpg

import (
	"context"
	"net/http"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
)

func boolPtr(b bool) *bool { return &b }

func newTestReader(perms *auth.UserPermissions) *Reader {
	return &Reader{
		Access: Access{
			CanRead: func(context.Context, string, string, string, string) bool { return true },
			Permission: func(_ context.Context, g auth.Grant) string {
				if perms == nil {
					return integration.PermissionAllowed
				}
				resource := g.Resource
				if g.Subresource != "" {
					resource += "/" + g.Subresource
				}
				allowed, known := perms.CanI(g.Verb, g.Group, resource, g.Namespace)
				if !known {
					return integration.PermissionUnknown
				}
				if allowed {
					return integration.PermissionAllowed
				}
				return integration.PermissionDenied
			},
		},
		Observations: Observations{
			Cluster: func(context.Context, string, string, ...auth.Grant) (*k8s.ResourceCache, *unstructured.Unstructured, error) {
				return nil, nil, &ReadFailure{Status: http.StatusServiceUnavailable, Message: "not connected"}
			},
		},
	}
}

func int32Ptr(v int32) *int32 { return &v }
