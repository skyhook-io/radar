package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

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

// TestActiveNamespacesMap_UnmarshalJSON pins the upgrade contract for the
// per-context namespace pick. Older Radar versions wrote a single string
// per context; the current shape is a slice. A regression here silently
// wipes every local user's saved pick on upgrade — the kind of bug we
// don't notice until users complain.
func TestActiveNamespacesMap_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    ActiveNamespacesMap
		wantErr bool
	}{
		{
			name:  "empty object",
			input: `{}`,
			want:  ActiveNamespacesMap{},
		},
		{
			name:  "legacy single-string per context",
			input: `{"ctx-a":"alpha"}`,
			want:  ActiveNamespacesMap{"ctx-a": {"alpha"}},
		},
		{
			name:  "new array shape preserved",
			input: `{"ctx-a":["alpha","beta"]}`,
			want:  ActiveNamespacesMap{"ctx-a": {"alpha", "beta"}},
		},
		{
			name:  "mixed legacy + new shapes within one map",
			input: `{"ctx-a":"alpha","ctx-b":["beta","gamma"]}`,
			want: ActiveNamespacesMap{
				"ctx-a": {"alpha"},
				"ctx-b": {"beta", "gamma"},
			},
		},
		{
			name:  "legacy empty string drops the key",
			input: `{"ctx-a":""}`,
			want:  ActiveNamespacesMap{},
		},
		{
			name:  "empty array drops the key",
			input: `{"ctx-a":[]}`,
			want:  ActiveNamespacesMap{},
		},
		{
			name:  "array with empty strings filters them out",
			input: `{"ctx-a":["",""]}`,
			want:  ActiveNamespacesMap{},
		},
		{
			name:  "array mixing real and empty entries keeps only real",
			input: `{"ctx-a":["alpha","","beta"]}`,
			want:  ActiveNamespacesMap{"ctx-a": {"alpha", "beta"}},
		},
		{
			name:    "malformed inner value (number) propagates error",
			input:   `{"ctx-a":42}`,
			wantErr: true,
		},
		{
			name:    "malformed inner value (object) propagates error",
			input:   `{"ctx-a":{"oops":"nope"}}`,
			wantErr: true,
		},
		{
			name:    "malformed JSON top-level propagates error",
			input:   `{bad`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got ActiveNamespacesMap
			err := json.Unmarshal([]byte(tt.input), &got)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil; result: %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("UnmarshalJSON(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// TestActiveNamespacesMap_RoundTrip pins that the new shape round-trips
// through Marshal → Unmarshal cleanly (no custom MarshalJSON needed).
func TestActiveNamespacesMap_RoundTrip(t *testing.T) {
	original := ActiveNamespacesMap{
		"ctx-a": {"alpha"},
		"ctx-b": {"beta", "gamma"},
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var roundTripped ActiveNamespacesMap
	if err := json.Unmarshal(data, &roundTripped); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(roundTripped, original) {
		t.Errorf("round-trip lost data: %v → %v", original, roundTripped)
	}
}

// TestSettings_LegacyMigrationOnLoad verifies the end-to-end upgrade flow:
// a settings.json file written by an older Radar with the legacy
// single-string shape loads cleanly, gets the slice value, and rewrites in
// the new shape on next Save.
func TestSettings_LegacyMigrationOnLoad(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	if err := os.MkdirAll(filepath.Join(dir, ".radar"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	legacy := `{"theme":"dark","activeNamespaces":{"ctx-a":"alpha","ctx-b":"beta"}}`
	if err := os.WriteFile(filepath.Join(dir, ".radar", "settings.json"), []byte(legacy), 0o644); err != nil {
		t.Fatalf("write legacy file: %v", err)
	}

	loaded := Load()
	if loaded.Theme != "dark" {
		t.Errorf("legacy load lost Theme: %q", loaded.Theme)
	}
	wantPicks := ActiveNamespacesMap{
		"ctx-a": {"alpha"},
		"ctx-b": {"beta"},
	}
	if !reflect.DeepEqual(loaded.ActiveNamespaces, wantPicks) {
		t.Errorf("legacy migration mismatched: got %v, want %v", loaded.ActiveNamespaces, wantPicks)
	}

	// Save then re-read raw — the file must now be in the new shape.
	if err := Save(loaded); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rawAfter, err := os.ReadFile(filepath.Join(dir, ".radar", "settings.json"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var raw struct {
		ActiveNamespaces map[string]json.RawMessage `json:"activeNamespaces"`
	}
	if err := json.Unmarshal(rawAfter, &raw); err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	for ctx, val := range raw.ActiveNamespaces {
		var asArr []string
		if err := json.Unmarshal(val, &asArr); err != nil {
			t.Errorf("rewritten %q value is not a JSON array: %s", ctx, val)
		}
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
