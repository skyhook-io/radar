package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/settings"
)

func useTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	return dir
}

func TestPersistLastContextRemembersTheSwitch(t *testing.T) {
	useTempHome(t)
	t.Cleanup(k8s.SetTestRegistryEntry("prod-eu", filepath.Join(t.TempDir(), "config"), "prod-eu"))

	persistLastContext(remembering(), "prod-eu")

	if saved := rememberedName(); saved != "prod-eu" {
		t.Errorf("remembered context = %q, want %q", saved, "prod-eu")
	}
}

func TestRegisterLastContextMemoryRemembersInitialContext(t *testing.T) {
	useTempHome(t)
	k8s.ResetTestState()
	t.Cleanup(k8s.ResetTestState)
	path := filepath.Join(t.TempDir(), "config")
	previous := k8s.SetTestContextName("prod-eu")
	t.Cleanup(func() { k8s.SetTestContextName(previous) })
	t.Cleanup(k8s.SetTestRegistryEntry("prod-eu", path, "prod-eu"))

	RegisterLastContextMemory(remembering())

	saved := settings.Load().LastDesktopContext
	if saved == nil || saved.Name != "prod-eu" || saved.SourceFile != path || saved.InFileName != "prod-eu" {
		t.Errorf("initial context was not remembered precisely: %+v", saved)
	}
}

func TestRegisterLastContextMemoryPreservesPreferenceThatMissed(t *testing.T) {
	useTempHome(t)
	k8s.ResetTestState()
	t.Cleanup(k8s.ResetTestState)
	if _, err := settings.Update(func(st *settings.Settings) {
		st.LastDesktopContext = &settings.LastContext{
			Name:       "prod-eu",
			SourceFile: "/configs/old.yaml",
			InFileName: "prod-eu",
		}
	}); err != nil {
		t.Fatal(err)
	}
	previous := k8s.SetTestContextName("staging")
	t.Cleanup(func() { k8s.SetTestContextName(previous) })
	t.Cleanup(k8s.SetTestRegistryEntry("staging", "/configs/current.yaml", "staging"))

	RegisterLastContextMemory(remembering())

	saved := settings.Load().LastDesktopContext
	if saved == nil || saved.Name != "prod-eu" || saved.SourceFile != "/configs/old.yaml" {
		t.Errorf("fallback context replaced the prior preference: %+v", saved)
	}
}

func TestLastContextSwitchRecorderPreservesMissedPreferenceAcrossReconnect(t *testing.T) {
	useTempHome(t)
	if _, err := settings.Update(func(st *settings.Settings) {
		st.LastDesktopContext = &settings.LastContext{
			Name:       "prod-eu",
			SourceFile: "/configs/old.yaml",
			InFileName: "prod-eu",
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.SetTestRegistryEntry("staging", "/configs/current.yaml", "staging"))

	recordSwitch := lastContextSwitchRecorder(remembering(), k8s.ContextSourceFor("staging"))
	recordSwitch("staging")

	saved := settings.Load().LastDesktopContext
	if saved == nil || saved.Name != "prod-eu" || saved.SourceFile != "/configs/old.yaml" {
		t.Errorf("reconnect to the fallback replaced the prior preference: %+v", saved)
	}
}

func TestLastContextSwitchRecorderRecordsADifferentDurableContext(t *testing.T) {
	useTempHome(t)
	t.Cleanup(k8s.SetTestRegistryEntry("staging", "/configs/current.yaml", "staging"))
	t.Cleanup(k8s.SetTestRegistryEntry("prod-us", "/configs/prod.yaml", "prod-us"))

	recordSwitch := lastContextSwitchRecorder(remembering(), k8s.ContextSourceFor("staging"))
	recordSwitch("prod-us")

	saved := settings.Load().LastDesktopContext
	if saved == nil || saved.Name != "prod-us" || saved.SourceFile != "/configs/prod.yaml" {
		t.Errorf("new durable selection was not remembered: %+v", saved)
	}
}

func TestPersistLastContextIgnoresEmptyName(t *testing.T) {
	useTempHome(t)

	persistLastContext(remembering(), "")

	if saved := rememberedName(); saved != "" {
		t.Errorf("remembered context = %q, want empty for an empty context name", saved)
	}
}

func TestPersistLastContextSkipsUnresolvableReference(t *testing.T) {
	useTempHome(t)
	previous := k8s.SetTestContextName("prod-eu")
	t.Cleanup(func() { k8s.SetTestContextName(previous) })
	t.Cleanup(k8s.SetTestRegistryEntry("prod-eu", "", ""))

	persistLastContext(remembering(), "prod-eu")

	if saved := settings.Load().LastDesktopContext; saved != nil {
		t.Errorf("unresolvable context was remembered: %+v", saved)
	}
}

func TestPersistLastContextPreservesInvalidSettings(t *testing.T) {
	dir := useTempHome(t)
	path := filepath.Join(dir, ".radar", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	const invalid = "{not-json"
	if err := os.WriteFile(path, []byte(invalid), 0o644); err != nil {
		t.Fatal(err)
	}
	kubeconfig := filepath.Join(t.TempDir(), "config")
	t.Cleanup(k8s.SetTestRegistryEntry("prod-eu", kubeconfig, "prod-eu"))

	persistLastContext(remembering(), "prod-eu")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != invalid {
		t.Errorf("settings were overwritten: got %q, want %q", data, invalid)
	}
}

func TestForgetLastContextPreservesInvalidSettings(t *testing.T) {
	dir := useTempHome(t)
	path := filepath.Join(dir, ".radar", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	const invalid = "{not-json"
	if err := os.WriteFile(path, []byte(invalid), 0o644); err != nil {
		t.Fatal(err)
	}

	ForgetLastContext()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != invalid {
		t.Errorf("settings were overwritten: got %q, want %q", data, invalid)
	}
}

// A shared server must not remember one user's cluster pick on disk — the
// switch belongs to whoever made it, not to the machine.
func TestPersistLastContextSkippedWhenAuthEnabled(t *testing.T) {
	useTempHome(t)

	persistLastContext(withAuth(auth.Config{Mode: "oidc"}), "prod-eu")

	if saved := rememberedName(); saved != "" {
		t.Errorf("remembered context = %q, want empty when auth is enabled", saved)
	}
}

func TestPersistLastContextSkippedForCloudTunnel(t *testing.T) {
	useTempHome(t)

	persistLastContext(cloudTunnelled(), "prod-eu")

	if saved := rememberedName(); saved != "" {
		t.Errorf("remembered context = %q, want empty in cloud-tunnel mode", saved)
	}
}

func TestStartupContextPreferenceReturnsLastUsedContext(t *testing.T) {
	useTempHome(t)
	remember(t, "prod-eu")

	got, err := startupContextPreference(remembering())
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "prod-eu" {
		t.Errorf("startupContextPreference() = %q, want %q", got.Name, "prod-eu")
	}
}

func TestStartupContextPreferenceEmptyWithoutSavedContext(t *testing.T) {
	useTempHome(t)

	got, err := startupContextPreference(remembering())
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "" {
		t.Errorf("startupContextPreference() = %q, want empty", got.Name)
	}
}

func TestStartupContextPreferenceReportsUnreadableSettings(t *testing.T) {
	dir := useTempHome(t)
	path := filepath.Join(dir, ".radar", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not-json"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := startupContextPreference(remembering()); err == nil {
		t.Fatal("startupContextPreference() error = nil, want unreadable settings reported")
	}
}

func TestStartupContextPreferenceSkippedWhenAuthEnabled(t *testing.T) {
	useTempHome(t)
	remember(t, "prod-eu")

	got, err := startupContextPreference(withAuth(auth.Config{Mode: "proxy"}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "" {
		t.Errorf("startupContextPreference() = %q, want empty when auth is enabled", got.Name)
	}
}

// remembering returns the config of an entrypoint that opts into the memory —
// Desktop's shape. The zero AppConfig deliberately does not.
func remembering() AppConfig {
	return AppConfig{RestoreLastDesktopContext: true}
}

func withAuth(c auth.Config) AppConfig {
	cfg := remembering()
	cfg.AuthConfig = c
	return cfg
}

func cloudTunnelled() AppConfig {
	cfg := remembering()
	cfg.CloudTunnelConfigured = true
	return cfg
}

// rememberedName reads back the recorded context name, treating "nothing
// recorded" as the empty string.
func rememberedName() string {
	if saved := settings.Load().LastDesktopContext; saved != nil {
		return saved.Name
	}
	return ""
}

func remember(t *testing.T, name string) {
	t.Helper()
	if _, err := settings.Update(func(st *settings.Settings) {
		st.LastDesktopContext = &settings.LastContext{Name: name}
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
}

// The switch is recorded with the file it came from, not just the name the
// header showed — see settings.LastContext for why the name alone is not a
// stable handle across kubeconfigs.
func TestPersistLastContextRecordsWhereTheContextCameFrom(t *testing.T) {
	useTempHome(t)
	path := filepath.Join(t.TempDir(), "team.yaml")
	t.Cleanup(k8s.SetTestRegistryEntry("dev (team)", path, "dev"))

	persistLastContext(remembering(), "dev (team)")

	saved := settings.Load().LastDesktopContext
	if saved == nil {
		t.Fatal("nothing recorded")
	}
	if got := *saved; got.Name != "dev (team)" || got.SourceFile != path || got.InFileName != "dev" {
		t.Errorf("recorded %+v, want name/file/in-file-name all pinned", saved)
	}
}
