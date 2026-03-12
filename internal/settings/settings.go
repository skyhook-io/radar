package settings

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
)

// PinnedKind is a resource kind the user has pinned to the sidebar.
type PinnedKind struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Group string `json:"group"`
}

// Settings holds user preferences persisted across restarts.
type Settings struct {
	Theme       string       `json:"theme,omitempty"`
	PinnedKinds []PinnedKind `json:"pinnedKinds,omitempty"`
}

// mu serializes Load-Decode-Save cycles to prevent concurrent PUTs from
// overwriting each other's changes.
var mu sync.Mutex

// Path returns the settings file path (~/.radar/settings.json).
func Path() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Printf("[settings] Cannot determine home directory: %v (settings will not be persisted)", err)
		return ""
	}
	return filepath.Join(homeDir, ".radar", "settings.json")
}

// Load reads settings from disk. Returns zero-value Settings if the file is missing or invalid.
func Load() Settings {
	path := Path()
	if path == "" {
		return Settings{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[settings] Failed to read %s: %v", path, err)
		}
		return Settings{}
	}
	var s Settings
	if err := json.Unmarshal(data, &s); err != nil {
		log.Printf("[settings] Failed to parse %s: %v", path, err)
		return Settings{}
	}
	return s
}

// Save writes settings to disk using atomic rename.
func Save(s Settings) error {
	path := Path()
	if path == "" {
		return os.ErrNotExist
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp) // best-effort cleanup
		return err
	}
	return nil
}

// Update atomically loads, applies a mutation, and saves settings.
// This prevents concurrent PUTs from overwriting each other's changes.
func Update(mutate func(*Settings)) (Settings, error) {
	mu.Lock()
	defer mu.Unlock()
	s := Load()
	mutate(&s)
	return s, Save(s)
}
