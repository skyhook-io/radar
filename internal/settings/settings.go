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

// AuditConfig holds cluster audit preferences.
type AuditConfig struct {
	IgnoredNamespaces []string `json:"ignoredNamespaces"`
	DisabledChecks    []string `json:"disabledChecks"`
}

// DefaultAuditConfig returns the default audit settings.
func DefaultAuditConfig() AuditConfig {
	return AuditConfig{
		IgnoredNamespaces: []string{"kube-system", "kube-node-lease", "kube-public", "*-system"},
	}
}

// Settings holds user preferences persisted across restarts.
type Settings struct {
	Theme       string       `json:"theme,omitempty"`
	PinnedKinds []PinnedKind `json:"pinnedKinds,omitempty"`
	Audit       *AuditConfig `json:"audit,omitempty"`
	// ActiveNamespaces maps kubeconfig context name → the user's namespace
	// picks (the in-app multi-select namespace switcher's last selection per
	// cluster). Empty slice (or missing key) means no override → fall back to
	// the kubeconfig context's namespace / cluster-wide.
	ActiveNamespaces ActiveNamespacesMap `json:"activeNamespaces,omitempty"`
}

// ActiveNamespacesMap is the persisted picker state, keyed by kubeconfig
// context name. Pre-multi-select releases stored a single string value per
// context; UnmarshalJSON accepts both shapes so old settings.json files
// don't lose their pick on first read after upgrade.
type ActiveNamespacesMap map[string][]string

func (m *ActiveNamespacesMap) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	out := make(ActiveNamespacesMap, len(raw))
	for k, v := range raw {
		var arr []string
		if err := json.Unmarshal(v, &arr); err == nil {
			if len(arr) > 0 {
				out[k] = arr
			}
			continue
		}
		var single string
		if err := json.Unmarshal(v, &single); err != nil {
			return err
		}
		if single != "" {
			out[k] = []string{single}
		}
	}
	*m = out
	return nil
}

// mu serializes Load-mutate-Save cycles to prevent concurrent PUTs from
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
