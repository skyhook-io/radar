package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestPathUsesEnvironmentOverride(t *testing.T) {
	want := filepath.Join(t.TempDir(), "child-settings.json")
	t.Setenv(pathEnv, want)

	if got := Path(); got != want {
		t.Fatalf("Path() = %q, want %q", got, want)
	}
}

func TestLoadMissing(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("USERPROFILE", tmpDir)

	s := Load()
	if s.Theme != "" || s.PinnedKinds != nil {
		t.Errorf("expected zero-value Settings, got %+v", s)
	}
}

func TestSaveAndLoad(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("USERPROFILE", tmpDir)

	want := Settings{
		Theme:       "dark",
		PinnedKinds: []PinnedKind{{Name: "pods", Kind: "Pod", Group: ""}},
	}

	if err := Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got := Load()
	if got.Theme != "dark" {
		t.Errorf("Theme = %q, want %q", got.Theme, "dark")
	}
	if len(got.PinnedKinds) != 1 || got.PinnedKinds[0].Name != "pods" {
		t.Errorf("PinnedKinds = %v, want 1 entry with Name=pods", got.PinnedKinds)
	}
}

func TestUpdateMergesFields(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("USERPROFILE", tmpDir)

	// Set initial state
	Save(Settings{Theme: "light"})

	// Update only PinnedKinds — Theme should be preserved
	result, err := Update(func(s *Settings) {
		s.PinnedKinds = []PinnedKind{{Name: "services", Kind: "Service", Group: ""}}
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if result.Theme != "light" {
		t.Errorf("Theme was overwritten: got %q", result.Theme)
	}
	if len(result.PinnedKinds) != 1 || result.PinnedKinds[0].Name != "services" {
		t.Errorf("PinnedKinds = %v, want 1 entry with Name=services", result.PinnedKinds)
	}

	// Verify it persisted
	loaded := Load()
	if loaded.Theme != "light" || len(loaded.PinnedKinds) != 1 {
		t.Errorf("persisted state doesn't match: %+v", loaded)
	}
}

func TestUpdateCheckedDoesNotOverwriteInvalidSettings(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	path := filepath.Join(dir, ".radar", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	const invalid = "{not-json"
	if err := os.WriteFile(path, []byte(invalid), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := UpdateChecked(func(s *Settings) {
		s.Theme = "dark"
	}); err == nil {
		t.Fatal("UpdateChecked succeeded with invalid existing settings")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != invalid {
		t.Errorf("settings were overwritten: got %q, want %q", data, invalid)
	}
}

func TestEmptySettingsProducesMinimalJSON(t *testing.T) {
	s := Settings{}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}

	if string(data) != "{}" {
		t.Errorf("zero-value Settings should marshal to {}, got %s", data)
	}
}

func TestLoadInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	os.MkdirAll(filepath.Join(dir, ".radar"), 0o755)
	os.WriteFile(filepath.Join(dir, ".radar", "settings.json"), []byte("{bad"), 0o644)

	s := Load()
	if s.Theme != "" {
		t.Error("invalid JSON should return zero-value Settings")
	}
}

// TestActiveNamespaces_RoundTrip pins that the multi-namespace pick shape
// round-trips through Marshal → Unmarshal cleanly.
func TestActiveNamespaces_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	original := Settings{
		ActiveNamespaces: map[string][]string{
			"ctx-a": {"alpha"},
			"ctx-b": {"beta", "gamma"},
		},
	}
	if err := Save(original); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded := Load()
	if !reflect.DeepEqual(loaded.ActiveNamespaces, original.ActiveNamespaces) {
		t.Errorf("round-trip lost data: %v → %v", original.ActiveNamespaces, loaded.ActiveNamespaces)
	}
}

func TestSaveCreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	// .radar/ doesn't exist yet
	if err := Save(Settings{Theme: "light"}); err != nil {
		t.Fatalf("Save should create directory: %v", err)
	}

	path := filepath.Join(dir, ".radar", "settings.json")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("settings.json should exist: %v", err)
	}
}

func TestRolloutKeyMintsOnceAndPersists(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	id := RolloutKey()
	if id == "" {
		t.Fatal("RolloutKey returned empty with a writable home")
	}
	if RolloutKey() != id {
		t.Fatal("RolloutKey is not stable across calls")
	}
	// Its own file, never the settings struct: /api/settings serializes
	// Settings verbatim, so the identifier must not be reachable from it.
	if raw, err := json.Marshal(Load()); err != nil || strings.Contains(string(raw), id) {
		t.Fatalf("install ID leaked into serialized settings: %s (%v)", raw, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".radar", "install-id"))
	if err != nil || strings.TrimSpace(string(data)) != id {
		t.Fatalf("persisted id = %q (%v), want %q", data, err, id)
	}
}

func TestRolloutKeyConcurrentMintResolvesToOneWinner(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	// Simulates the CLI and Desktop starting together: every racer must end
	// up with the same identity (O_EXCL create; losers adopt the winner).
	const racers = 16
	ids := make([]string, racers)
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ids[i] = RolloutKey()
		}(i)
	}
	wg.Wait()

	for i, id := range ids {
		if id == "" {
			// A loser may observe the winner's file before its bytes land —
			// "" (out of cohort this start) is the allowed fail-safe, a
			// DIFFERENT id is not.
			continue
		}
		if id != ids[0] && ids[0] != "" {
			t.Fatalf("racer %d minted %q while racer 0 got %q", i, id, ids[0])
		}
	}
	if RolloutKey() == "" {
		t.Fatal("no identity persisted after the race")
	}
}
