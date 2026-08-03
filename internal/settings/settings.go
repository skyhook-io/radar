package settings

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	// picks (the in-app multi-select switcher's last selection per cluster).
	// Tri-state: a missing key means the user never chose — the view defaults
	// to the kubeconfig context's namespace (falling back to "All namespaces");
	// an empty slice is an explicit "All namespaces" choice that suppresses
	// that default; a non-empty slice is an explicit pick.
	ActiveNamespaces map[string][]string `json:"activeNamespaces,omitempty"`
	// HelmOCISources are registered OCI chart-source prefixes (e.g.
	// "oci://ghcr.io/myorg/charts") — the OCI analog of `helm repo add`, which
	// has no native equivalent for OCI registries. Helm doesn't persist the ref
	// a release was installed from, so these let Radar discover upgrades for the
	// user's own OCI-published charts by probing "<prefix>/<chartName>". Not
	// cluster-scoped: a registry is where your charts live, independent of which
	// cluster they're deployed to.
	HelmOCISources []string `json:"helmOciSources,omitempty"`
	// InstallID is a random identifier minted on first use. It exists solely
	// so staged feature rollouts can bucket this installation deterministically
	// (same install → same verdict across restarts). It is never transmitted
	// anywhere — Radar makes no network calls with it, and nothing derives it
	// from the machine or the user.
	InstallID string `json:"installId,omitempty"`
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
	s, _ := LoadChecked()
	return s
}

// LoadChecked reads settings from disk, distinguishing "no settings file"
// (zero value, nil error) from a failed read or parse (zero value, error).
// Callers that take a state-changing action on absence — like defaulting the
// namespace view when no pick was ever saved — must use this: treating a
// failed read as absence would act on data that may actually exist.
func LoadChecked() (Settings, error) {
	path := Path()
	if path == "" {
		return Settings{}, errors.New("settings path unavailable")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Settings{}, nil
		}
		log.Printf("[settings] Failed to read %s: %v", path, err)
		return Settings{}, err
	}
	var s Settings
	if err := json.Unmarshal(data, &s); err != nil {
		log.Printf("[settings] Failed to parse %s: %v", path, err)
		return Settings{}, err
	}
	return s, nil
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

// InstallID returns this installation's stable rollout identifier, minting
// and persisting one on first use. Returns "" when settings cannot be
// persisted (no home directory, read-only filesystem) — a caller gating a
// partial rollout must treat that as out-of-cohort rather than re-rolling a
// fresh identity every start.
func InstallID() string {
	mu.Lock()
	defer mu.Unlock()
	s, err := LoadChecked()
	if err != nil {
		return ""
	}
	if s.InstallID != "" {
		return s.InstallID
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return ""
	}
	s.InstallID = hex.EncodeToString(raw)
	if err := Save(s); err != nil {
		return ""
	}
	return s.InstallID
}
