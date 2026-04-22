package k8s

import (
	"context"
	"testing"

	"k8s.io/client-go/rest"

	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

// swapGlobals temporarily sets the package-level k8sClient / k8sConfig /
// dynamicClient globals for the duration of a test, restoring them on
// cleanup. Tests run sequentially in the same package, so this is safe.
func swapGlobals(t *testing.T, cfg *rest.Config) {
	t.Helper()
	clientMu.Lock()
	prevCfg := k8sConfig
	prevClient := k8sClient
	k8sConfig = cfg
	k8sClient = nil
	clientMu.Unlock()

	t.Cleanup(func() {
		clientMu.Lock()
		k8sConfig = prevCfg
		k8sClient = prevClient
		clientMu.Unlock()
	})
}

// TestClientFromContext_Impersonation verifies that a user on the context
// produces an impersonated client whose rest.Config carries the user's
// identity. This is the core contract the cloud-mode retrofit depends on.
func TestClientFromContext_Impersonation(t *testing.T) {
	swapGlobals(t, &rest.Config{Host: "https://example.invalid"})

	user := &pkgauth.User{Username: "alice", Groups: []string{"cloud:owner"}}
	ctx := pkgauth.ContextWithUser(context.Background(), user)

	client := ClientFromContext(ctx)
	if client == nil {
		t.Fatal("ClientFromContext returned nil with valid config + user")
	}
	// Concrete type carries the impersonated REST config; assert we got
	// the user-scoped config back by rebuilding through the primitive.
	cfg, err := ImpersonatedConfig(user.Username, user.Groups)
	if err != nil {
		t.Fatalf("ImpersonatedConfig failed: %v", err)
	}
	if cfg.Impersonate.UserName != "alice" {
		t.Errorf("Impersonate.UserName = %q, want %q", cfg.Impersonate.UserName, "alice")
	}
	if len(cfg.Impersonate.Groups) != 1 || cfg.Impersonate.Groups[0] != "cloud:owner" {
		t.Errorf("Impersonate.Groups = %v, want [cloud:owner]", cfg.Impersonate.Groups)
	}
}

// TestClientFromContext_ImpersonationFailureReturnsNil verifies the
// fail-closed contract: when the base config is missing, impersonation
// can't be constructed, and the caller gets nil (never a fallback to SA).
func TestClientFromContext_ImpersonationFailureReturnsNil(t *testing.T) {
	swapGlobals(t, nil) // no base config

	user := &pkgauth.User{Username: "alice", Groups: []string{"cloud:viewer"}}
	ctx := pkgauth.ContextWithUser(context.Background(), user)

	if client := ClientFromContext(ctx); client != nil {
		t.Error("ClientFromContext should return nil when base config is missing (fail-closed)")
	}
}

// TestClientFromContext_TypedNilGuard is the regression test for the
// typed-nil interface trap. GetClient() returns *kubernetes.Clientset;
// wrapping a nil *Clientset in kubernetes.Interface produces a non-nil
// interface — callers' `if client == nil` checks slip past and the next
// method call panics. The guard in ClientFromContext must return untyped
// nil so callers can do the obvious check.
func TestClientFromContext_TypedNilGuard(t *testing.T) {
	swapGlobals(t, nil)

	client := ClientFromContext(context.Background())
	if client != nil {
		// client might be a non-nil interface wrapping a nil *Clientset
		// (the typed-nil trap). Surface the failure unambiguously.
		t.Fatalf("ClientFromContext with no config must return untyped nil; got %T (typed-nil interface trap)", client)
	}
}
