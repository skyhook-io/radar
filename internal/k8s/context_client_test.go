package k8s

import (
	"context"
	"testing"

	"k8s.io/client-go/rest"

	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

func swapGlobals(t *testing.T, cfg *rest.Config) {
	t.Helper()
	prev := Current()
	restoreTestConnection(&Connection{config: cfg})
	t.Cleanup(func() { restoreTestConnection(prev) })
}

// TestClientFromContext_Impersonation verifies that a user on the context
// produces an impersonated client whose rest.Config carries the user's
// identity. This is the core contract the cloud-mode retrofit depends on.
func TestClientFromContext_Impersonation(t *testing.T) {
	swapGlobals(t, &rest.Config{Host: "https://example.invalid"})

	user := &pkgauth.User{Username: "alice", Groups: []string{"radar:owner"}}
	ctx := pkgauth.ContextWithUser(context.Background(), user)

	client := ClientFromContext(ctx)
	if client == nil {
		t.Fatal("ClientFromContext returned nil with valid config + user")
	}
	// Concrete type carries the impersonated REST config; assert we got
	// the user-scoped config back by rebuilding through the primitive.
	cfg, err := UserConfig(user)
	if err != nil {
		t.Fatalf("UserConfig failed: %v", err)
	}
	if cfg.Impersonate.UserName != "alice" {
		t.Errorf("Impersonate.UserName = %q, want %q", cfg.Impersonate.UserName, "alice")
	}
	if len(cfg.Impersonate.Groups) != 1 || cfg.Impersonate.Groups[0] != "radar:owner" {
		t.Errorf("Impersonate.Groups = %v, want [radar:owner]", cfg.Impersonate.Groups)
	}
}

func TestConfigSnapshotKeepsContextAndConfigTogether(t *testing.T) {
	prev := Current()
	restoreTestConnection(&Connection{config: &rest.Config{Host: "https://cluster-a.invalid"}, contextName: "cluster-a"})
	t.Cleanup(func() { restoreTestConnection(prev) })

	conn := Current()
	config, snapshotContext := conn.ConfigCopy(), conn.ContextName()
	if config == nil || config.Host != "https://cluster-a.invalid" || snapshotContext != "cluster-a" {
		t.Fatalf("snapshot = (%+v, %q), want cluster-a config and context", config, snapshotContext)
	}
	config.Host = "https://mutated.invalid"
	if current := GetConfig(); current.Host != "https://cluster-a.invalid" {
		t.Fatalf("snapshot mutation changed shared config: %q", current.Host)
	}
}

// TestClientFromContext_ImpersonationFailureReturnsNil verifies the
// fail-closed contract: when the base config is missing, impersonation
// can't be constructed, and the caller gets nil (never a fallback to SA).
func TestClientFromContext_ImpersonationFailureReturnsNil(t *testing.T) {
	swapGlobals(t, nil) // no base config

	user := &pkgauth.User{Username: "alice", Groups: []string{"radar:viewer"}}
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

// MCP write tools (apply_resource, manage_workload, manage_gitops, …) all
// route through DynamicClientFromContext so writes are RBAC-checked against
// the calling user, not the SA. These tests pin the contract: any refactor
// that swaps DynamicClientFromContext for k8s.GetDynamicClient() would
// silently run user writes as the SA.

func TestDynamicClientFromContext_Impersonation(t *testing.T) {
	swapGlobals(t, &rest.Config{Host: "https://example.invalid"})

	user := &pkgauth.User{Username: "alice", Groups: []string{"radar:owner"}}
	ctx := pkgauth.ContextWithUser(context.Background(), user)

	if client := DynamicClientFromContext(ctx); client == nil {
		t.Fatal("DynamicClientFromContext returned nil with valid config + user")
	}
	cfg := ConfigFromContext(ctx)
	if cfg == nil {
		t.Fatal("ConfigFromContext returned nil with valid config + user")
	}
	if cfg.Impersonate.UserName != "alice" {
		t.Errorf("Impersonate.UserName = %q, want %q", cfg.Impersonate.UserName, "alice")
	}
}

func TestDynamicClientFromContext_FailsClosedOnNoConfig(t *testing.T) {
	swapGlobals(t, nil)

	user := &pkgauth.User{Username: "alice", Groups: []string{"radar:viewer"}}
	ctx := pkgauth.ContextWithUser(context.Background(), user)

	if client := DynamicClientFromContext(ctx); client != nil {
		t.Error("DynamicClientFromContext must return nil when base config is missing (fail-closed)")
	}
	if cfg := ConfigFromContext(ctx); cfg != nil {
		t.Error("ConfigFromContext must return nil when base config is missing (fail-closed)")
	}
}
