package k8s

import (
	"fmt"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

// UserConfig returns a copy of the shared REST config that acts as the user
// (see UserRESTConfig), so K8s RBAC checks apply to them.
func UserConfig(user *pkgauth.User) (*rest.Config, error) {
	base := GetConfig()
	if base == nil {
		return nil, fmt.Errorf("K8s config not initialized")
	}
	return UserRESTConfig(base, user), nil
}

func UserConfigSnapshot(user *pkgauth.User) (*rest.Config, string, error) {
	base, contextName := GetConfigSnapshot()
	if base == nil {
		return nil, contextName, fmt.Errorf("K8s config not initialized")
	}
	return UserRESTConfig(base, user), contextName, nil
}

// UserClient creates a typed client that acts as the given user.
func UserClient(user *pkgauth.User) (kubernetes.Interface, error) {
	cfg, err := UserConfig(user)
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(cfg)
}

func UserClientSafetySnapshot(user *pkgauth.User) (kubernetes.Interface, string, error) {
	clientMu.RLock()
	base := k8sConfig
	binding := contextBinding
	if base != nil {
		base = rest.CopyConfig(base)
	}
	clientMu.RUnlock()
	if base == nil {
		return nil, binding, fmt.Errorf("K8s config not initialized")
	}
	client, err := kubernetes.NewForConfig(UserRESTConfig(base, user))
	return client, binding, err
}

// UserDynamicClient creates a dynamic client that acts as the given user.
// Used for write operations (update, delete, patch) when auth is enabled.
func UserDynamicClient(user *pkgauth.User) (dynamic.Interface, error) {
	cfg, err := UserConfig(user)
	if err != nil {
		return nil, err
	}
	return dynamic.NewForConfig(cfg)
}

func UserDynamicClientSnapshot(user *pkgauth.User) (dynamic.Interface, string, error) {
	cfg, contextName, err := UserConfigSnapshot(user)
	if err != nil {
		return nil, contextName, err
	}
	client, err := dynamic.NewForConfig(cfg)
	return client, contextName, err
}
