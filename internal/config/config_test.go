package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissing(t *testing.T) {
	// Override path to a non-existent file
	orig := os.Getenv("HOME")
	t.Setenv("HOME", t.TempDir())
	defer os.Setenv("HOME", orig)

	c := Load()
	if c.Kubeconfig != "" || c.Port != 0 || c.MCP != nil {
		t.Errorf("expected zero-value Config, got %+v", c)
	}
}

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	mcp := true
	want := Config{
		Kubeconfig:      "/tmp/kubeconfig",
		KubeconfigDirs:  []string{"/dir1", "/dir2"},
		Namespace:       "prod",
		Port:            9999,
		NoBrowser:       true,
		TimelineStorage: "sqlite",
		HistoryLimit:    5000,
		PrometheusURL:   "http://prom:9090",
		MCP:             &mcp,
	}

	if err := Save(want); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Verify file exists
	path := filepath.Join(dir, ".radar", "config.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config file not created: %v", err)
	}

	got := Load()
	if got.Kubeconfig != want.Kubeconfig {
		t.Errorf("Kubeconfig = %q, want %q", got.Kubeconfig, want.Kubeconfig)
	}
	if len(got.KubeconfigDirs) != 2 || got.KubeconfigDirs[0] != "/dir1" || got.KubeconfigDirs[1] != "/dir2" {
		t.Errorf("KubeconfigDirs = %v, want %v", got.KubeconfigDirs, want.KubeconfigDirs)
	}
	if got.Port != want.Port {
		t.Errorf("Port = %d, want %d", got.Port, want.Port)
	}
	if got.Namespace != want.Namespace {
		t.Errorf("Namespace = %q, want %q", got.Namespace, want.Namespace)
	}
	if got.NoBrowser != want.NoBrowser {
		t.Errorf("NoBrowser = %v, want %v", got.NoBrowser, want.NoBrowser)
	}
	if got.TimelineStorage != want.TimelineStorage {
		t.Errorf("TimelineStorage = %q, want %q", got.TimelineStorage, want.TimelineStorage)
	}
	if got.HistoryLimit != want.HistoryLimit {
		t.Errorf("HistoryLimit = %d, want %d", got.HistoryLimit, want.HistoryLimit)
	}
	if got.MCP == nil || *got.MCP != true {
		t.Errorf("MCP = %v, want true", got.MCP)
	}
}

func TestUpdate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	result, err := Update(func(c *Config) {
		c.Port = 8080
		c.Namespace = "staging"
	})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if result.Port != 8080 || result.Namespace != "staging" {
		t.Errorf("unexpected result: %+v", result)
	}

	// Verify persisted
	loaded := Load()
	if loaded.Port != 8080 {
		t.Errorf("Port not persisted: got %d", loaded.Port)
	}
}

func TestHelpers(t *testing.T) {
	t.Run("PortOr", func(t *testing.T) {
		if (Config{}).PortOr(9280) != 9280 {
			t.Error("zero Port should return default")
		}
		if (Config{Port: 8080}).PortOr(9280) != 8080 {
			t.Error("set Port should return value")
		}
	})

	t.Run("HistoryLimitOr", func(t *testing.T) {
		if (Config{}).HistoryLimitOr(10000) != 10000 {
			t.Error("zero HistoryLimit should return default")
		}
		if (Config{HistoryLimit: 5000}).HistoryLimitOr(10000) != 5000 {
			t.Error("set HistoryLimit should return value")
		}
	})

	t.Run("MCPEnabledOr", func(t *testing.T) {
		if (Config{}).MCPEnabledOr(true) != true {
			t.Error("nil MCP should return default")
		}
		f := false
		if (Config{MCP: &f}).MCPEnabledOr(true) != false {
			t.Error("false MCP should return false")
		}
	})

	t.Run("TimelineStorageOr", func(t *testing.T) {
		if (Config{}).TimelineStorageOr("memory") != "memory" {
			t.Error("empty TimelineStorage should return default")
		}
		if (Config{TimelineStorage: "sqlite"}).TimelineStorageOr("memory") != "sqlite" {
			t.Error("set TimelineStorage should return value")
		}
	})

	t.Run("KubeconfigDirsFlag", func(t *testing.T) {
		if (Config{}).KubeconfigDirsFlag() != "" {
			t.Error("nil dirs should return empty string")
		}
		c := Config{KubeconfigDirs: []string{"/a", "/b"}}
		if c.KubeconfigDirsFlag() != "/a,/b" {
			t.Errorf("got %q, want %q", c.KubeconfigDirsFlag(), "/a,/b")
		}
	})
}

func TestLoadInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	path := filepath.Join(dir, ".radar")
	os.MkdirAll(path, 0o755)
	os.WriteFile(filepath.Join(path, "config.json"), []byte("not json"), 0o644)

	c := Load()
	if c.Port != 0 {
		t.Errorf("invalid JSON should return zero-value Config")
	}
}

func TestOmitemptyFields(t *testing.T) {
	// Verify that zero-value config produces minimal JSON
	c := Config{}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{}" {
		t.Errorf("zero-value Config should marshal to {}, got %s", data)
	}
}
