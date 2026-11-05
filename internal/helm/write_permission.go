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
	user := pkgauth.UserFromContext(ctx)
	// Authenticated repository/source management and cache refresh have no target release.
	if user != nil && namespace == "" {
		return 0, nil
	}
	if k8s.ForceDisableHelmWrite {
		return http.StatusForbidden, fmt.Errorf("Helm write operations are disabled by --disable-helm-write")
	}
	if user != nil {
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
	if namespace != "" {
		caps, err := k8s.CheckNamespaceCapabilities(ctx, namespace)
		if err != nil {
			return http.StatusInternalServerError, fmt.Errorf("failed to check permissions: %w", err)
		}
		if caps == nil || caps.Errors.HelmWrite {
			return http.StatusServiceUnavailable, fmt.Errorf("couldn't verify Radar's Kubernetes permissions for Helm in namespace %q; try again", namespace)
		}
		if !caps.HelmWrite {
			return http.StatusForbidden, fmt.Errorf("Radar's Kubernetes credentials don't have permission to create Secrets in namespace %q, which Helm requires to manage releases", namespace)
		}
		return 0, nil
	}
	caps, err := k8s.CheckCapabilities(ctx)
	if err != nil {
		return http.StatusInternalServerError, fmt.Errorf("failed to check capabilities: %w", err)
	}
	if !caps.HelmWrite {
		return http.StatusForbidden, fmt.Errorf("Helm management requires permission to create Secrets in the current namespace scope. Select an allowed namespace scope or grant that permission (chart installations: rbac.helm=true).")
	}
	return 0, nil
}
