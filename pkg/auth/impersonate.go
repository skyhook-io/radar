package auth

import (
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// ImpersonatedConfig returns a copy of the given REST config with impersonation set.
// Used for write operations when auth is enabled, so K8s RBAC checks apply to the user.
func ImpersonatedConfig(base *rest.Config, username string, groups []string) *rest.Config {
	cfg := rest.CopyConfig(base)
	cfg.Impersonate = rest.ImpersonationConfig{
		UserName: username,
		Groups:   groups,
	}
	return cfg
}

// ImpersonatedClient creates a typed K8s client that acts as the given user.
func ImpersonatedClient(base *rest.Config, username string, groups []string) (kubernetes.Interface, error) {
	return kubernetes.NewForConfig(ImpersonatedConfig(base, username, groups))
}

// ImpersonatedDynamicClient creates a dynamic K8s client that acts as the given user.
// Used for write operations (update, delete, patch) when auth is enabled.
func ImpersonatedDynamicClient(base *rest.Config, username string, groups []string) (dynamic.Interface, error) {
	return dynamic.NewForConfig(ImpersonatedConfig(base, username, groups))
}
