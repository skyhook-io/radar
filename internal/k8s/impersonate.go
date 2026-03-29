package k8s

import (
	"fmt"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

// ImpersonatedConfig returns a copy of the shared REST config with impersonation set.
// Used for write operations when auth is enabled, so K8s RBAC checks apply to the user.
func ImpersonatedConfig(username string, groups []string) (*rest.Config, error) {
	base := GetConfig()
	if base == nil {
		return nil, fmt.Errorf("K8s config not initialized")
	}
	return pkgauth.ImpersonatedConfig(base, username, groups), nil
}

// ImpersonatedClient creates a typed client that acts as the given user.
func ImpersonatedClient(username string, groups []string) (kubernetes.Interface, error) {
	base := GetConfig()
	if base == nil {
		return nil, fmt.Errorf("K8s config not initialized")
	}
	return pkgauth.ImpersonatedClient(base, username, groups)
}

// ImpersonatedDynamicClient creates a dynamic client that acts as the given user.
// Used for write operations (update, delete, patch) when auth is enabled.
func ImpersonatedDynamicClient(username string, groups []string) (dynamic.Interface, error) {
	base := GetConfig()
	if base == nil {
		return nil, fmt.Errorf("K8s config not initialized")
	}
	return pkgauth.ImpersonatedDynamicClient(base, username, groups)
}
