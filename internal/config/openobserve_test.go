package config

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestOpenObserveEntryRoundTripsWithPrivateFileMode(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	want := &OpenObserveConfig{
		BaseURL: "https://oo.example.com", Org: "default", User: "sa@example.com", Token: "secret",
		CACertFile: "/tmp/ca.pem", ClusterLabel: "cluster",
		Matches: map[string]OpenObserveMatch{
			"7b930c6c-dcbb-4d1f-9afd-67ea890873d7": {Label: "radar_cluster_uid", Value: "7b930c6c-dcbb-4d1f-9afd-67ea890873d7", By: OpenObserveMatchByID},
			"dd1bfeff-e60c-4827-b8e0-b822ab22a9e8": {Label: "cluster", Value: "oo-c", By: OpenObserveMatchByName, ConfirmedAt: "2026-11-23T10:00:00Z", Context: "kind-oo-c"},
		},
	}
	if err := Save(Config{Port: 9280, OpenObserve: want}); err != nil {
		t.Fatal(err)
	}
	got, err := LoadForStartup()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.OpenObserve, want) {
		t.Fatalf("round trip = %+v, want %+v", got.OpenObserve, want)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, ".radar", "config.json"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %o, want 0600", info.Mode().Perm())
		}
	}
	if err := Save(Config{}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".radar", "config.json"))
	if string(data) != "{}\n" {
		t.Fatalf("an absent entry must not be written, got %s", data)
	}
}

func TestOpenObserveTokenEnvWinsWhenSet(t *testing.T) {
	c := OpenObserveConfig{Token: "saved", TokenEnv: "RADAR_TEST_OO_TOKEN"}
	t.Setenv("RADAR_TEST_OO_TOKEN", "from-env")
	if got, err := c.ResolveToken(); err != nil || got != "from-env" {
		t.Fatalf("set env: got %q, %v", got, err)
	}
	t.Setenv("RADAR_TEST_OO_TOKEN", "")
	if got, err := c.ResolveToken(); err != nil || got != "saved" {
		t.Fatalf("empty env falls back to the saved token: got %q, %v", got, err)
	}
	c.Token = ""
	if _, err := c.ResolveToken(); err == nil {
		t.Fatal("empty env and no saved token must fail")
	}
}
