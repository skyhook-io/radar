package config

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Config holds startup configuration persisted across restarts.
// Values are used as flag defaults; explicit CLI flags always take precedence.
type Config struct {
	Kubeconfig        string   `json:"kubeconfig,omitempty"`
	KubeconfigDirs    []string `json:"kubeconfigDirs,omitempty"`
	Namespace         string   `json:"namespace,omitempty"`
	Namespaces        []string `json:"namespaces,omitempty"`
	Port              int      `json:"port,omitempty"`
	NoBrowser         bool     `json:"noBrowser,omitempty"`
	Browser           string   `json:"browser,omitempty"`
	TimelineStorage   string   `json:"timelineStorage,omitempty"`
	TimelineDBPath    string   `json:"timelineDbPath,omitempty"`
	TimelineRetention string   `json:"timelineRetention,omitempty"` // Go duration (e.g. "168h" for 7d); "0" disables age cleanup
	TimelineMaxSize   string   `json:"timelineMaxSize,omitempty"`   // Byte size (e.g. "800Mi", "8Gi"); "0" disables
	HistoryLimit      int      `json:"historyLimit,omitempty"`
	PrometheusURL     string   `json:"prometheusUrl,omitempty"`
	// PrometheusHeaders are sent with every request to the Prometheus API.
	// Required for auth-protected backends (Bearer tokens, X-Scope-OrgID, etc.).
	// Stored in plain text in ~/.radar/config.json — protect the file accordingly.
	PrometheusHeaders        map[string]string `json:"prometheusHeaders,omitempty"`
	PrometheusHeadersFromEnv map[string]string `json:"prometheusHeadersFromEnv,omitempty"`
	MCP                      *bool             `json:"mcp,omitempty"` // nil = default (true), false = disabled
	// DebugImage is the image used for ephemeral debug containers and node debug
	// pods. Empty falls back to busybox:latest; set it to a reachable mirror for
	// air-gapped / private-registry clusters.
	DebugImage string `json:"debugImage,omitempty"`
	// AIHistory persists AI investigations (transcripts + verdicts) to a local
	// SQLite file so they survive restarts. nil = default (true), false = off.
	AIHistory *bool `json:"aiHistory,omitempty"`
	// AIHistoryDBPath overrides the history DB location (default ~/.radar/ai-runs.db).
	AIHistoryDBPath string `json:"aiHistoryDbPath,omitempty"`
	// AIConsent records the acknowledged AI-diagnosis disclosure version per
	// surface ("standard", "cursor"). Machine-scoped on purpose: consent gates a
	// machine-scoped action (spawn this machine's agent CLI, persist transcripts
	// to this machine's disk), so one acknowledgment covers the web panel and
	// the `radar diagnose` CLI alike.
	AIConsent map[string]string `json:"aiConsent,omitempty"`
}

// AI-diagnosis consent disclosure versions, per surface. THE single source of
// truth for the server endpoint and the CLI's standalone path alike — bump when
// the consent copy's claims change materially, and prior acknowledgments stop
// counting everywhere at once. (standard v3: transcripts persist to local
// history; cursor v2: same disclosure on Cursor's distinct trust model.)
var aiConsentVersions = map[string]string{
	"standard": "v3",
	"cursor":   "v2",
}

// AIConsentVersion returns the current disclosure version for a surface
// ("" for an unknown surface).
func AIConsentVersion(surface string) string { return aiConsentVersions[surface] }

// AIConsentGiven reports whether the CURRENT disclosure version for the surface
// has been acknowledged on this machine.
func AIConsentGiven(surface string) bool {
	v := aiConsentVersions[surface]
	return v != "" && Load().AIConsent[surface] == v
}

// RecordAIConsent acknowledges the current disclosure version for a surface.
func RecordAIConsent(surface string) error {
	v := aiConsentVersions[surface]
	if v == "" {
		return os.ErrInvalid
	}
	_, err := Update(func(c *Config) {
		if c.AIConsent == nil {
			c.AIConsent = map[string]string{}
		}
		c.AIConsent[surface] = v
	})
	return err
}

// mu serializes Load-mutate-Save cycles to prevent concurrent writes
// from overwriting each other's changes.
var mu sync.Mutex

// Path returns the config file path (~/.radar/config.json).
func Path() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Printf("[config] Cannot determine home directory: %v (config will not be persisted)", err)
		return ""
	}
	return filepath.Join(homeDir, ".radar", "config.json")
}

// Load reads config from disk. Returns zero-value Config if the file is missing or invalid.
func Load() Config {
	path := Path()
	if path == "" {
		return Config{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[config] Failed to read %s: %v", path, err)
		}
		return Config{}
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		log.Printf("[config] Failed to parse %s: %v", path, err)
		return Config{}
	}
	return c
}

// Save writes config to disk using atomic rename.
func Save(c Config) error {
	path := Path()
	if path == "" {
		return os.ErrNotExist
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
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

// Update atomically loads, applies a mutation, and saves config.
func Update(mutate func(*Config)) (Config, error) {
	mu.Lock()
	defer mu.Unlock()
	c := Load()
	mutate(&c)
	return c, Save(c)
}

// PortOr returns c.Port if set (> 0), otherwise the provided default.
func (c Config) PortOr(def int) int {
	if c.Port > 0 {
		return c.Port
	}
	return def
}

// HistoryLimitOr returns c.HistoryLimit if set (> 0), otherwise the provided default.
func (c Config) HistoryLimitOr(def int) int {
	if c.HistoryLimit > 0 {
		return c.HistoryLimit
	}
	return def
}

// MCPEnabledOr returns *c.MCP if non-nil, otherwise the provided default.
func (c Config) MCPEnabledOr(def bool) bool {
	if c.MCP != nil {
		return *c.MCP
	}
	return def
}

// AIHistoryOr returns *c.AIHistory if non-nil, otherwise the provided default.
func (c Config) AIHistoryOr(def bool) bool {
	if c.AIHistory != nil {
		return *c.AIHistory
	}
	return def
}

// TimelineStorageOr returns c.TimelineStorage if non-empty, otherwise the provided default.
func (c Config) TimelineStorageOr(def string) string {
	if c.TimelineStorage != "" {
		return c.TimelineStorage
	}
	return def
}

// TimelineRetentionOr parses c.TimelineRetention as a Go duration. Returns the
// provided default if unset or unparseable. A literal "0" disables cleanup.
func (c Config) TimelineRetentionOr(def time.Duration) time.Duration {
	if c.TimelineRetention == "" {
		return def
	}
	d, err := time.ParseDuration(c.TimelineRetention)
	if err != nil {
		log.Printf("[config] Invalid timelineRetention %q (using default %s): %v", c.TimelineRetention, def, err)
		return def
	}
	return d
}

func (c Config) TimelineMaxSizeOr(def string) string {
	if c.TimelineMaxSize == "" {
		return def
	}
	return c.TimelineMaxSize
}

func ParseByteSize(raw string) (int64, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, strconv.ErrSyntax
	}
	lower := strings.ToLower(s)
	multipliers := []struct {
		suffix string
		value  int64
	}{
		{"gib", 1 << 30},
		{"gb", 1000 * 1000 * 1000},
		{"gi", 1 << 30},
		{"g", 1000 * 1000 * 1000},
		{"mib", 1 << 20},
		{"mb", 1000 * 1000},
		{"mi", 1 << 20},
		{"m", 1000 * 1000},
		{"kib", 1 << 10},
		{"kb", 1000},
		{"ki", 1 << 10},
		{"k", 1000},
		{"b", 1},
	}
	for _, m := range multipliers {
		if strings.HasSuffix(lower, m.suffix) {
			num := strings.TrimSpace(s[:len(s)-len(m.suffix)])
			v, err := strconv.ParseFloat(num, 64)
			if err != nil {
				return 0, err
			}
			if v < 0 {
				return 0, strconv.ErrSyntax
			}
			return int64(v * float64(m.value)), nil
		}
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, err
	}
	if v < 0 {
		return 0, strconv.ErrSyntax
	}
	return v, nil
}

// KubeconfigDirsFlag returns KubeconfigDirs joined as a comma-separated string
// suitable for use as a flag default value.
func (c Config) KubeconfigDirsFlag() string {
	return strings.Join(c.KubeconfigDirs, ",")
}

// NamespacesFlag returns Namespaces joined as a comma-separated string
// suitable for use as a flag default value.
func (c Config) NamespacesFlag() string {
	return strings.Join(c.Namespaces, ",")
}
