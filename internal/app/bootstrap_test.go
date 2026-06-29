package app

import (
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/settings"
)

func TestValidateNamespaceScopeTarget(t *testing.T) {
	cases := []struct {
		name    string
		target  string
		wantErr bool
	}{
		{"single valid namespace", "team-prod", false},
		{"empty target", "", true},
		{"comma-separated (multiple)", "team-a,team-b", true},
		{"whitespace", "team a", true},
		{"uppercase invalid", "TeamProd", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateNamespaceScopeTarget(tc.target)
			if tc.wantErr && err == nil {
				t.Fatalf("validateNamespaceScopeTarget(%q) = nil, want error", tc.target)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("validateNamespaceScopeTarget(%q) = %v, want nil", tc.target, err)
			}
		})
	}
}

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

func TestConfigureNamespaceScopePreferenceResolverRescopeSurvivesReconnect(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	k8s.ResetTestState()
	t.Cleanup(k8s.ResetTestState)
	k8s.SetTestContextName("ctx-a")
	k8s.SetFallbackNamespace("foo")

	// --namespace=foo seeds the starting scope, overriding any stale saved pick.
	if _, err := settings.Update(func(st *settings.Settings) {
		st.ActiveNamespaces = map[string][]string{"ctx-a": {"stale"}}
	}); err != nil {
		t.Fatalf("settings.Update: %v", err)
	}
	configureNamespaceScopePreferenceResolver(AppConfig{NamespaceScope: true, Namespace: "foo"})
	if got := k8s.GetNamespaceScopeTarget(); got != "foo" {
		t.Fatalf("startup target = %q, want foo (seeded over stale pick)", got)
	}

	// The user rescopes to bar in the UI, which persists the pick.
	if _, err := settings.Update(func(st *settings.Settings) {
		st.ActiveNamespaces["ctx-a"] = []string{"bar"}
	}); err != nil {
		t.Fatalf("settings.Update: %v", err)
	}

	// A reconnect / context switch clears the override then restores from the pick:
	// the rescope to bar must survive, not snap back to the startup --namespace.
	k8s.ClearNamespaceScopeOverride()
	k8s.RestoreNamespaceScopePreference("ctx-a")
	if got := k8s.GetNamespaceScopeTarget(); got != "bar" {
		t.Fatalf("target after rescope+reconnect = %q, want bar", got)
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
