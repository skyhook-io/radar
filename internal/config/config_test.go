package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadMissing(t *testing.T) {
	// Override path to a non-existent file
	orig := os.Getenv("HOME")
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("USERPROFILE", tmpDir)
	defer os.Setenv("HOME", orig)

	c := Load()
	if c.Kubeconfig != "" || c.Port != 0 || c.MCP != nil {
		t.Errorf("expected zero-value Config, got %+v", c)
	}
}

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

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
		PrometheusHeaders: map[string]string{
			"Authorization": "Bearer abc",
			"X-Scope-OrgID": "tenant-1",
		},
		MCP: &mcp,
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
	if len(got.PrometheusHeaders) != 2 ||
		got.PrometheusHeaders["Authorization"] != "Bearer abc" ||
		got.PrometheusHeaders["X-Scope-OrgID"] != "tenant-1" {
		t.Errorf("PrometheusHeaders = %v, want %v", got.PrometheusHeaders, want.PrometheusHeaders)
	}
}

func TestUpdate(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("USERPROFILE", tmpDir)

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

	t.Run("TimelineRetentionOr", func(t *testing.T) {
		def := 7 * 24 * time.Hour
		cases := []struct {
			name string
			in   string
			want time.Duration
		}{
			{"empty falls back to default", "", def},
			{"valid duration", "168h", 168 * time.Hour},
			{"explicit zero disables", "0", 0},
			// Go's ParseDuration doesn't accept "d" — verify we fall back to
			// def, not 0. A future "improvement" returning 0 on error would
			// silently disable retention for everyone with this typo.
			{"7d typo falls back to default", "7d", def},
			{"garbage falls back to default", "garbage", def},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got := (Config{TimelineRetention: tc.in}).TimelineRetentionOr(def)
				if got != tc.want {
					t.Errorf("TimelineRetentionOr(%q) = %s, want %s", tc.in, got, tc.want)
				}
			})
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
	t.Setenv("USERPROFILE", dir)

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
