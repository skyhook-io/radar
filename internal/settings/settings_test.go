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
	if s.Theme != "" || s.PinnedKinds != nil || s.LogsWrap != nil || s.LogsTimestamps != nil {
		t.Errorf("expected zero-value Settings, got %+v", s)
	}
}

func TestSaveAndLoad(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	wrap := true
	ts := false
	want := Settings{
		Theme:          "dark",
		PinnedKinds:    []PinnedKind{{Name: "pods", Kind: "Pod", Group: ""}},
		LogsWrap:       &wrap,
		LogsTimestamps: &ts,
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
	if got.LogsWrap == nil || *got.LogsWrap != true {
		t.Errorf("LogsWrap = %v, want true", got.LogsWrap)
	}
	if got.LogsTimestamps == nil || *got.LogsTimestamps != false {
		t.Errorf("LogsTimestamps = %v, want false", got.LogsTimestamps)
	}
}

func TestUpdateMergesFields(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// Set initial state
	Save(Settings{Theme: "light"})

	// Update only LogsWrap — Theme should be preserved
	wrap := false
	result, err := Update(func(s *Settings) {
		s.LogsWrap = &wrap
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if result.Theme != "light" {
		t.Errorf("Theme was overwritten: got %q", result.Theme)
	}
	if result.LogsWrap == nil || *result.LogsWrap != false {
		t.Errorf("LogsWrap = %v, want false", result.LogsWrap)
	}

	// Verify it persisted
	loaded := Load()
	if loaded.Theme != "light" || loaded.LogsWrap == nil || *loaded.LogsWrap != false {
		t.Errorf("persisted state doesn't match: %+v", loaded)
	}
}

func TestNilBoolsOmittedInJSON(t *testing.T) {
	// Settings with nil bool pointers should produce clean JSON without those fields
	s := Settings{Theme: "dark"}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}

	var parsed map[string]any
	json.Unmarshal(data, &parsed)

	if _, ok := parsed["logsWrap"]; ok {
		t.Error("nil LogsWrap should be omitted from JSON")
	}
	if _, ok := parsed["logsTimestamps"]; ok {
		t.Error("nil LogsTimestamps should be omitted from JSON")
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
