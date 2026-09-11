package app

import (
	"testing"

	"github.com/skyhook-io/radar/internal/settings"
)

// The zero value is what a terminal entrypoint passes: `kubectl radar` and
// `radar diagnose --standalone` start where the shell says they do, so a
// switch made in Desktop can't steer a command typed after
// `kubectl config use-context`.
func TestTerminalEntrypointDoesNotRememberTheContext(t *testing.T) {
	useTempHome(t)

	persistLastContext(AppConfig{}, "prod-eu")

	if saved := rememberedName(); saved != "" {
		t.Errorf("remembered context = %q, want empty for an entrypoint that doesn't opt in", saved)
	}
}

func TestTerminalEntrypointStartsOnCurrentContext(t *testing.T) {
	useTempHome(t)
	remember(t, "prod-eu")

	got, err := startupContextPreference(AppConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "" {
		t.Errorf("startupContextPreference() = %q, want empty so current-context stands", got.Name)
	}
}

func TestPersistLastContextSkippedWhenRestoreDisabled(t *testing.T) {
	useTempHome(t)

	cfg := remembering()
	cfg.RestoreLastDesktopContext = false
	persistLastContext(cfg, "prod-eu")

	if saved := rememberedName(); saved != "" {
		t.Errorf("remembered context = %q, want empty when restore is turned off", saved)
	}
}

func TestStartupContextPreferenceSkippedWhenRestoreDisabled(t *testing.T) {
	useTempHome(t)
	remember(t, "prod-eu")

	cfg := remembering()
	cfg.RestoreLastDesktopContext = false
	got, err := startupContextPreference(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "" {
		t.Errorf("startupContextPreference() = %q, want empty when restore is turned off", got.Name)
	}
}

func TestForgetLastContextClearsTheMemory(t *testing.T) {
	useTempHome(t)
	remember(t, "prod-eu")

	ForgetLastContext()

	if saved := rememberedName(); saved != "" {
		t.Errorf("remembered context = %q, want it cleared", saved)
	}
}

// It shares settings.json with every other preference — clearing it must
// leave the rest untouched.
func TestForgetLastContextLeavesOtherSettingsAlone(t *testing.T) {
	useTempHome(t)
	if _, err := settings.Update(func(st *settings.Settings) {
		st.Theme = "dark"
		st.HelmOCISources = []string{"oci://ghcr.io/acme/charts"}
	}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	remember(t, "prod-eu")

	ForgetLastContext()

	got := settings.Load()
	if got.LastDesktopContext != nil {
		t.Errorf("remembered context = %+v, want it cleared", got.LastDesktopContext)
	}
	if got.Theme != "dark" || len(got.HelmOCISources) != 1 {
		t.Errorf("ForgetLastContext disturbed sibling settings: %+v", got)
	}
}
