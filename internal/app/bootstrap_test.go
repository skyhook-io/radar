package app

import (
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/settings"
)

func TestConfigureNamespaceScopePreferenceResolverUsesSingleSavedLocalPick(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	k8s.ResetTestState()
	t.Cleanup(k8s.ResetTestState)
	k8s.SetTestContextName("ctx-a")

	if _, err := settings.Update(func(st *settings.Settings) {
		st.ActiveNamespaces = map[string][]string{"ctx-a": {"prod"}}
	}); err != nil {
		t.Fatalf("settings.Update: %v", err)
	}

	configureNamespaceScopePreferenceResolver(AppConfig{NamespaceScope: true})

	if got := k8s.GetNamespaceScopeTarget(); got != "prod" {
		t.Fatalf("GetNamespaceScopeTarget() = %q, want prod", got)
	}

	k8s.ClearNamespaceScopeOverride()
	k8s.RestoreNamespaceScopePreference("ctx-a")
	if got := k8s.GetNamespaceScopeTarget(); got != "prod" {
		t.Fatalf("GetNamespaceScopeTarget() after restore = %q, want prod", got)
	}
}

func TestConfigureNamespaceScopePreferenceResolverExplicitNamespaceWins(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	k8s.ResetTestState()
	t.Cleanup(k8s.ResetTestState)
	k8s.SetTestContextName("ctx-a")
	k8s.SetFallbackNamespace("cli-ns")

	if _, err := settings.Update(func(st *settings.Settings) {
		st.ActiveNamespaces = map[string][]string{"ctx-a": {"saved-ns"}}
	}); err != nil {
		t.Fatalf("settings.Update: %v", err)
	}

	configureNamespaceScopePreferenceResolver(AppConfig{NamespaceScope: true, Namespace: "cli-ns"})

	if got := k8s.GetNamespaceScopeTarget(); got != "cli-ns" {
		t.Fatalf("GetNamespaceScopeTarget() = %q, want cli-ns", got)
	}

	k8s.RestoreNamespaceScopePreference("ctx-a")
	if got := k8s.GetNamespaceScopeTarget(); got != "cli-ns" {
		t.Fatalf("GetNamespaceScopeTarget() after restore = %q, want cli-ns", got)
	}
}

func TestConfigureNamespaceScopePreferenceResolverAuthDoesNotUseLocalSettings(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	k8s.ResetTestState()
	t.Cleanup(k8s.ResetTestState)
	k8s.SetTestContextName("ctx-a")

	if _, err := settings.Update(func(st *settings.Settings) {
		st.ActiveNamespaces = map[string][]string{"ctx-a": {"saved-ns"}}
	}); err != nil {
		t.Fatalf("settings.Update: %v", err)
	}

	configureNamespaceScopePreferenceResolver(AppConfig{
		NamespaceScope: true,
		AuthConfig:     auth.Config{Mode: "proxy"},
	})

	k8s.RestoreNamespaceScopePreference("ctx-a")
	if got := k8s.GetNamespaceScopeTarget(); got != "" {
		t.Fatalf("GetNamespaceScopeTarget() = %q, want empty", got)
	}
}
