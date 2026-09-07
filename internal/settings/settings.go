package settings

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
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
	// LastDesktopContext is the context the Desktop window last used, reopened
	// on the next launch. Desktop-scoped by name because
	// `kubectl radar` shares this file and must never follow it: a command
	// typed after `kubectl config use-context` runs where the shell says it
	// will.
	LastDesktopContext *LastContext `json:"lastDesktopContext,omitempty"`
}

// LastContext identifies a context precisely enough to survive a restart.
// Name alone is not enough: with several kubeconfigs, which file owns the
// unqualified name depends on directory read order, so a new file can steal it
// and point the restore at another cluster. SourceFile + InFileName are the
// identity; Name is the label.
type LastContext struct {
	Name       string `json:"name"`
	SourceFile string `json:"sourceFile,omitempty"`
	InFileName string `json:"inFileName,omitempty"`
}

// mu serializes Load-mutate-Save cycles to prevent concurrent PUTs from
// overwriting each other's changes.
var mu sync.Mutex

const pathEnv = "RADAR_SETTINGS_PATH"

// Path returns the settings file path. Isolated child processes can override
// the default ~/.radar/settings.json location through RADAR_SETTINGS_PATH.
func Path() string {
	if path := os.Getenv(pathEnv); path != "" {
		return path
	}
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

// UpdateChecked refuses to write when existing settings cannot be read. It is
// for automatic writers, which must not replace a damaged or temporarily
// unavailable file with a mutated zero value.
func UpdateChecked(mutate func(*Settings)) (Settings, error) {
	mu.Lock()
	defer mu.Unlock()
	s, err := LoadChecked()
	if err != nil {
		return Settings{}, err
	}
	mutate(&s)
	return s, Save(s)
}

// RolloutKey returns the local value staged rollouts hash on, minting and
// persisting one on first use. It never leaves this machine. Returns "" when it
// cannot be persisted (no home directory, read-only filesystem) — a caller
// gating a partial rollout must treat that as out-of-cohort rather than
// re-rolling a fresh key every start.
//
// The file name is load-bearing: changing it makes every existing install mint
// a new key and land in a different bucket, so the rollout would visibly flip
// for people on both sides of it.
//
// It lives in its own file, deliberately NOT in the Settings struct:
// /api/settings serializes that struct verbatim (including through a Cloud
// tunnel), and a settings PUT round-trip could silently drop a field the
// client never saw. A random local value must be able to do neither.
// The O_EXCL create makes a concurrent first mint (CLI and Desktop starting
// together) resolve to one winner; losers adopt the winner's file.
func RolloutKey() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	path := filepath.Join(homeDir, ".radar", "install-id")
	if data, err := os.ReadFile(path); err == nil {
		return strings.TrimSpace(string(data))
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return ""
	}
	id := hex.EncodeToString(raw)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return ""
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			if data, rerr := os.ReadFile(path); rerr == nil {
				return strings.TrimSpace(string(data))
			}
		}
		return ""
	}
	// Any failure past the create must take the file with it. A stranded empty
	// file is read back as a valid-but-empty value on every later call, which
	// callers treat as nothing to hash — pinning the install out of a staged
	// rollout permanently, with no way to self-heal. Removing it lets the next
	// start mint cleanly.
	// Close is where a failed flush surfaces on some filesystems, so both it and
	// the write have to succeed before the id can be trusted — and either
	// failing is handled the same way.
	_, writeErr := f.WriteString(id)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		os.Remove(path)
		return ""
	}
	return id
}
