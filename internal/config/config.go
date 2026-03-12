package config

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Config holds startup configuration persisted across restarts.
// CLI flags override these values when explicitly provided.
type Config struct {
	Kubeconfig      string   `json:"kubeconfig,omitempty"`
	KubeconfigDirs  []string `json:"kubeconfigDirs,omitempty"`
	Namespace       string   `json:"namespace,omitempty"`
	Port            int      `json:"port,omitempty"`
	NoBrowser       bool     `json:"noBrowser,omitempty"`
	TimelineStorage string   `json:"timelineStorage,omitempty"`
	TimelineDBPath  string   `json:"timelineDbPath,omitempty"`
	HistoryLimit    int      `json:"historyLimit,omitempty"`
	PrometheusURL   string   `json:"prometheusUrl,omitempty"`
	MCP             *bool    `json:"mcp,omitempty"` // nil = default (true), false = disabled
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

// TimelineStorageOr returns c.TimelineStorage if non-empty, otherwise the provided default.
func (c Config) TimelineStorageOr(def string) string {
	if c.TimelineStorage != "" {
		return c.TimelineStorage
	}
	return def
}

// KubeconfigDirsFlag returns KubeconfigDirs joined as a comma-separated string
// suitable for use as a flag default value.
func (c Config) KubeconfigDirsFlag() string {
	return strings.Join(c.KubeconfigDirs, ",")
}
