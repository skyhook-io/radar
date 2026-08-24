package k8s

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/errorlog"
)

func TestResolveKubeconfigSourcesDirectAndFallbackModes(t *testing.T) {
	home := t.TempDir()
	configPath := writeKubeconfig(t, home, "config", "direct", []kubeEntry{
		{ctxName: "direct", userName: "user", clusterName: "cluster"},
	})

	t.Run("configured path expands tilde in direct mode", func(t *testing.T) {
		got, err := resolveKubeconfigSources(InitOptions{KubeconfigPath: "~/config"}, "", home)
		if err != nil {
			t.Fatalf("resolveKubeconfigSources: %v", err)
		}
		if got.mode != "single" || got.useRegistry || got.tryInCluster {
			t.Fatalf("resolution = %+v", got)
		}
		if len(got.paths) != 1 || got.paths[0] != configPath {
			t.Fatalf("paths = %v, want %q", got.paths, configPath)
		}
	})

	t.Run("environment path is used without configured sources", func(t *testing.T) {
		got, err := resolveKubeconfigSources(InitOptions{}, configPath, home)
		if err != nil {
			t.Fatalf("resolveKubeconfigSources: %v", err)
		}
		if got.mode != "single" || got.useRegistry || got.tryInCluster || len(got.paths) != 1 || got.paths[0] != configPath {
			t.Fatalf("resolution = %+v", got)
		}
	})

	t.Run("multiple environment paths use isolated loading", func(t *testing.T) {
		second := writeKubeconfig(t, home, "second", "second", []kubeEntry{
			{ctxName: "second", userName: "user-2", clusterName: "cluster-2"},
		})
		got, err := resolveKubeconfigSources(InitOptions{}, configPath+string(os.PathListSeparator)+second, home)
		if err != nil {
			t.Fatalf("resolveKubeconfigSources: %v", err)
		}
		if got.mode != "multi-env" || !got.useRegistry || got.tryInCluster || len(got.paths) != 2 {
			t.Fatalf("resolution = %+v", got)
		}
	})

	t.Run("multiple configured paths are not reported as environment", func(t *testing.T) {
		second := writeKubeconfig(t, home, "configured-second", "second", []kubeEntry{
			{ctxName: "second", userName: "user-2", clusterName: "cluster-2"},
		})
		got, err := resolveKubeconfigSources(InitOptions{
			KubeconfigPath: configPath + string(os.PathListSeparator) + second,
		}, "", home)
		if err != nil {
			t.Fatalf("resolveKubeconfigSources: %v", err)
		}
		if got.mode != "multi-file" || !got.useRegistry || len(got.paths) != 2 {
			t.Fatalf("resolution = %+v", got)
		}
	})

	t.Run("empty sources try in-cluster before home default", func(t *testing.T) {
		got, err := resolveKubeconfigSources(InitOptions{}, "", home)
		if err != nil {
			t.Fatalf("resolveKubeconfigSources: %v", err)
		}
		want := filepath.Join(home, ".kube", "config")
		if got.mode != "single" || !got.tryInCluster || got.useRegistry || len(got.paths) != 1 || got.paths[0] != want {
			t.Fatalf("resolution = %+v, want fallback %q", got, want)
		}
	})

	t.Run("missing home still allows in-cluster attempt", func(t *testing.T) {
		got, err := resolveKubeconfigSources(InitOptions{}, "", "")
		if err != nil {
			t.Fatalf("resolveKubeconfigSources: %v", err)
		}
		if !got.tryInCluster || len(got.paths) != 0 {
			t.Fatalf("resolution = %+v", got)
		}
	})
}

func TestResolveKubeconfigSourcesDirectories(t *testing.T) {
	home := t.TempDir()
	primaryDir := filepath.Join(home, "primary")
	additionalDir := filepath.Join(home, "additional")
	for _, dir := range []string{primaryDir, additionalDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	primary := writeKubeconfig(t, primaryDir, "config", "primary", []kubeEntry{
		{ctxName: "primary", userName: "primary-user", clusterName: "primary-cluster"},
	})
	additional := writeKubeconfig(t, additionalDir, "additional.yaml", "additional", []kubeEntry{
		{ctxName: "additional", userName: "additional-user", clusterName: "additional-cluster"},
	})

	t.Run("directories only ignore KUBECONFIG and stay registry-backed", func(t *testing.T) {
		got, err := resolveKubeconfigSources(InitOptions{KubeconfigDirs: []string{additionalDir}}, primary, home)
		if err != nil {
			t.Fatalf("resolveKubeconfigSources: %v", err)
		}
		if got.mode != "multi-dir" || !got.useRegistry || !got.ignoredKubeconfigEnv {
			t.Fatalf("resolution = %+v", got)
		}
		if len(got.paths) != 1 || got.paths[0] != additional {
			t.Fatalf("paths = %v, want only %q", got.paths, additional)
		}
	})

	t.Run("configured primary precedes directory files", func(t *testing.T) {
		got, err := resolveKubeconfigSources(InitOptions{
			KubeconfigPath: primary,
			KubeconfigDirs: []string{additionalDir},
		}, "", home)
		if err != nil {
			t.Fatalf("resolveKubeconfigSources: %v", err)
		}
		if got.mode != "multi-source" || !got.useRegistry || got.directoryFileCount != 1 {
			t.Fatalf("resolution = %+v", got)
		}
		if len(got.paths) != 2 || got.paths[0] != primary || got.paths[1] != additional {
			t.Fatalf("paths = %v, want [%q %q]", got.paths, primary, additional)
		}
	})

	t.Run("missing additional directory does not defeat primary", func(t *testing.T) {
		got, err := resolveKubeconfigSources(InitOptions{
			KubeconfigPath: primary,
			KubeconfigDirs: []string{filepath.Join(home, "missing")},
		}, "", home)
		if err != nil {
			t.Fatalf("resolveKubeconfigSources: %v", err)
		}
		if len(got.paths) != 1 || got.paths[0] != primary || !got.useRegistry || got.mode != "multi-source" || got.directoryFileCount != 0 {
			t.Fatalf("resolution = %+v", got)
		}
	})

	t.Run("empty additional directory is reported without defeating primary", func(t *testing.T) {
		errorlog.Reset()
		t.Cleanup(errorlog.Reset)
		empty := t.TempDir()
		got, err := resolveKubeconfigSources(InitOptions{
			KubeconfigPath: primary,
			KubeconfigDirs: []string{empty},
		}, "", home)
		if err != nil {
			t.Fatalf("resolveKubeconfigSources: %v", err)
		}
		if got.directoryFileCount != 0 {
			t.Fatalf("resolution = %+v", got)
		}
		entries := errorlog.GetEntries()
		if len(entries) != 1 || !strings.Contains(entries[0].Message, "yielded no valid files") {
			t.Fatalf("directory warnings = %+v", entries)
		}
	})

	t.Run("directories only require a valid file", func(t *testing.T) {
		empty := t.TempDir()
		if _, err := resolveKubeconfigSources(InitOptions{KubeconfigDirs: []string{empty}}, "", home); err == nil {
			t.Fatal("resolveKubeconfigSources returned nil error")
		}
	})

	t.Run("unusable primary does not fall through to directory cluster", func(t *testing.T) {
		errorlog.Reset()
		t.Cleanup(errorlog.Reset)
		invalid := filepath.Join(home, "invalid-config")
		if err := os.WriteFile(invalid, []byte("apiVersion: ["), 0o600); err != nil {
			t.Fatalf("write invalid kubeconfig: %v", err)
		}
		_, err := resolveKubeconfigSources(InitOptions{
			KubeconfigPath: invalid,
			KubeconfigDirs: []string{additionalDir},
		}, "", home)
		if err == nil {
			t.Fatal("resolveKubeconfigSources returned nil error")
		}
		if !strings.Contains(err.Error(), "is unusable") || !strings.Contains(err.Error(), filepath.Base(invalid)) {
			t.Fatalf("error = %q", err)
		}
		entries := errorlog.GetEntries()
		if len(entries) != 1 || !strings.Contains(entries[0].Message, "primary kubeconfig unusable") || strings.Contains(entries[0].Message, home) {
			t.Fatalf("primary errors = %+v", entries)
		}
	})

	t.Run("primary without contexts is distinguished from load failure", func(t *testing.T) {
		errorlog.Reset()
		t.Cleanup(errorlog.Reset)
		empty := filepath.Join(home, "empty-config")
		if err := os.WriteFile(empty, []byte("apiVersion: v1\nkind: Config\ncontexts: []\n"), 0o600); err != nil {
			t.Fatalf("write empty kubeconfig: %v", err)
		}
		_, err := resolveKubeconfigSources(InitOptions{
			KubeconfigPath: empty,
			KubeconfigDirs: []string{additionalDir},
		}, "", home)
		if err == nil || !strings.Contains(err.Error(), "contains no usable contexts") {
			t.Fatalf("error = %v", err)
		}
		entries := errorlog.GetEntries()
		if len(entries) != 1 || !strings.Contains(entries[0].Message, "contains no contexts") {
			t.Fatalf("primary errors = %+v", entries)
		}
	})
}

func TestNormalizeKubeconfigDirectoriesSkipsEmptyEntries(t *testing.T) {
	home := t.TempDir()
	got, err := normalizeKubeconfigDirectories([]string{"", "~"}, home)
	if err != nil {
		t.Fatalf("normalizeKubeconfigDirectories: %v", err)
	}
	if len(got) != 1 || got[0] != home {
		t.Fatalf("directories = %v, want [%q]", got, home)
	}
}

func TestScrubKubeconfigLoadErrorRemovesSourcePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	got := scrubKubeconfigLoadError(path, errors.New("decode "+path+": line 3"))
	if strings.Contains(got, filepath.Dir(path)) || !strings.Contains(got, "config: line 3") {
		t.Fatalf("scrubbed error = %q", got)
	}
}

func TestResolveKubeconfigSourcesDeduplicatesFileIdentity(t *testing.T) {
	home := t.TempDir()
	configsDir := filepath.Join(home, "configs")
	if err := os.MkdirAll(configsDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	configPath := writeKubeconfig(t, configsDir, "config", "ctx", []kubeEntry{
		{ctxName: "ctx", userName: "user", clusterName: "cluster"},
	})
	symlinkDir := filepath.Join(home, "links")
	if err := os.MkdirAll(symlinkDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Symlink(configPath, filepath.Join(symlinkDir, "linked-config")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	got, err := resolveKubeconfigSources(InitOptions{
		KubeconfigPath: "~/configs/config",
		KubeconfigDirs: []string{configsDir, symlinkDir},
	}, "", home)
	if err != nil {
		t.Fatalf("resolveKubeconfigSources: %v", err)
	}
	if len(got.paths) != 1 || got.paths[0] != configPath {
		t.Fatalf("paths = %v, want one primary path %q", got.paths, configPath)
	}
	if got.mode != "multi-source" || !got.useRegistry || got.directoryFileCount != 0 {
		t.Fatalf("resolution = %+v", got)
	}

	t.Chdir(configsDir)
	relative, err := resolveKubeconfigSources(InitOptions{
		KubeconfigPath: "config" + string(os.PathListSeparator) + configPath,
	}, "", home)
	if err != nil {
		t.Fatalf("resolve relative/absolute duplicate: %v", err)
	}
	if len(relative.paths) != 1 || relative.paths[0] != configPath || relative.mode != "single" || relative.useRegistry {
		t.Fatalf("relative resolution = %+v", relative)
	}
}
