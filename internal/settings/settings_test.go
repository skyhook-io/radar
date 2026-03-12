package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissing(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	s := Load()
	if s.Theme != "" || s.PinnedKinds != nil {
		t.Errorf("expected zero-value Settings, got %+v", s)
	}
}

func TestSaveAndLoad(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

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
	t.Setenv("HOME", t.TempDir())

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

	os.MkdirAll(filepath.Join(dir, ".radar"), 0o755)
	os.WriteFile(filepath.Join(dir, ".radar", "settings.json"), []byte("{bad"), 0o644)

	s := Load()
	if s.Theme != "" {
		t.Error("invalid JSON should return zero-value Settings")
	}
}

func TestSaveCreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	// .radar/ doesn't exist yet
	if err := Save(Settings{Theme: "light"}); err != nil {
		t.Fatalf("Save should create directory: %v", err)
	}

	path := filepath.Join(dir, ".radar", "settings.json")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("settings.json should exist: %v", err)
	}
}
