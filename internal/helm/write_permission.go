package helm

import (
	"context"
	"fmt"
	"net/http"

	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

// CheckWritePermission is shared by REST and MCP. Check before fetching charts:
// a caller who cannot manage a release must not initiate repository requests.
// Success returns status zero and nil error; failures return an HTTP status.
// An empty namespace skips the signed-in user preflight for existing REST callers;
// targeted operations must validate their namespace before calling.
func CheckWritePermission(ctx context.Context, namespace string) (int, error) {
	if k8s.ForceDisableHelmWrite {
		return http.StatusForbidden, fmt.Errorf("Helm write operations are disabled by --disable-helm-write")
	}
	if user := pkgauth.UserFromContext(ctx); user != nil {
		if namespace == "" {
			return 0, nil
		}
		caps, err := k8s.CheckNamespaceCapabilitiesForUser(ctx, user.Username, user.Groups, namespace)
		if err != nil {
			return http.StatusInternalServerError, fmt.Errorf("failed to check permissions: %w", err)
		}
		if caps == nil || caps.Errors.HelmWrite {
			return http.StatusServiceUnavailable, fmt.Errorf("couldn't verify your Kubernetes permissions for Helm; try again")
		}
		if !caps.HelmWrite {
			return http.StatusForbidden, fmt.Errorf("You don't have permission to manage Helm releases in namespace %q. Helm stores each release as a Secret there, and your Kubernetes RBAC doesn't allow creating Secrets in it.", namespace)
		}
		return 0, nil
	}
	caps, err := k8s.CheckCapabilities(ctx)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("failed to check capabilities: %w", err)
	}
	if !caps.HelmWrite {
		return http.StatusForbidden, fmt.Errorf("Helm write operations require additional RBAC permissions. Set rbac.helm=true in the Radar Helm chart values.")
	}
	return 0, nil
}
