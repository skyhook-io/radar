package k8s

import (
	"context"
	"log"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

// ClientFromContext returns a typed client scoped to the user on the context.
// When a user is attached (auth enabled), returns an impersonated client so
// K8s RBAC applies to the caller. When no user is attached (auth disabled
// or local-binary path), returns the shared ServiceAccount client.
//
// Returns nil if impersonation is required but fails — callers must handle
// nil rather than falling back to the SA client, which would silently
// escalate the user's privileges.
//
// Used by code paths that don't have access to *http.Request (e.g. MCP
// tools, which only receive ctx). REST handlers should prefer
// Server.getClientForRequest for consistency.
func ClientFromContext(ctx context.Context) kubernetes.Interface {
	if user := pkgauth.UserFromContext(ctx); user != nil {
		client, err := ImpersonatedClient(user.Username, user.Groups)
		if err != nil {
			log.Printf("[auth] Impersonation failed for %s: %v", user.Username, err)
			return nil
		}
		return client
	}
	// Guard against the typed-nil trap: GetClient returns *Clientset, which
	// can be nil before the K8s connection is established. Assigning it to
	// kubernetes.Interface would produce a non-nil interface wrapping a nil
	// pointer, and callers' `if client == nil` checks would slip through.
	if c := GetClient(); c != nil {
		return c
	}
	return nil
}

// DynamicClientFromContext is the dynamic-client analog of ClientFromContext.
// Same nil-on-impersonation-failure contract.
func DynamicClientFromContext(ctx context.Context) dynamic.Interface {
	if user := pkgauth.UserFromContext(ctx); user != nil {
		client, err := ImpersonatedDynamicClient(user.Username, user.Groups)
		if err != nil {
			log.Printf("[auth] Impersonation failed for %s: %v", user.Username, err)
			return nil
		}
		return client
	}
	// Same typed-nil guard as ClientFromContext.
	if c := GetDynamicClient(); c != nil {
		return c
	}
	return nil
}

// ConfigFromContext is the REST-config analog of ClientFromContext.
// Returns nil on impersonation failure (same fail-closed contract).
func ConfigFromContext(ctx context.Context) *rest.Config {
	if user := pkgauth.UserFromContext(ctx); user != nil {
		cfg, err := ImpersonatedConfig(user.Username, user.Groups)
		if err != nil {
			log.Printf("[auth] Impersonation failed for %s: %v", user.Username, err)
			return nil
		}
		return cfg
	}
	return GetConfig()
}
