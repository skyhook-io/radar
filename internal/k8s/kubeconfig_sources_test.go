package k8s

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/errorlog"
	"k8s.io/client-go/tools/clientcmd"
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

	t.Run("configured path reports ignored environment", func(t *testing.T) {
		got, err := resolveKubeconfigSources(InitOptions{KubeconfigPath: configPath}, "/ambient/config", home)
		if err != nil {
			t.Fatalf("resolveKubeconfigSources: %v", err)
		}
		if !got.ignoredKubeconfigEnv || got.ignoredKubeconfigEnvReason != "primary kubeconfig configured" {
			t.Fatalf("resolution = %+v", got)
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

	t.Run("unusable environment secondary is skipped", func(t *testing.T) {
		errorlog.Reset()
		t.Cleanup(errorlog.Reset)
		missing := filepath.Join(home, "missing-secondary")
		got, err := resolveKubeconfigSources(InitOptions{}, configPath+string(os.PathListSeparator)+missing, home)
		if err != nil {
			t.Fatalf("resolveKubeconfigSources: %v", err)
		}
		if got.mode != "single" || got.useRegistry || len(got.paths) != 1 || got.paths[0] != configPath {
			t.Fatalf("resolution = %+v", got)
		}
		entries := errorlog.GetEntries()
		if len(entries) != 1 || !strings.Contains(entries[0].Message, "missing-secondary") || strings.Contains(entries[0].Message, home) {
			t.Fatalf("KUBECONFIG warnings = %+v", entries)
		}
	})

	t.Run("single unusable environment path keeps actionable reason", func(t *testing.T) {
		errorlog.Reset()
		t.Cleanup(errorlog.Reset)
		missing := filepath.Join(home, "typo-config")
		_, err := resolveKubeconfigSources(InitOptions{}, missing, home)
		if err == nil || !strings.Contains(err.Error(), "typo-config") || !strings.Contains(err.Error(), "no such file") || strings.Contains(err.Error(), home) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("configured path is always one file", func(t *testing.T) {
		configuredName := "configured" + string(os.PathListSeparator) + "second"
		configured := writeKubeconfig(t, home, configuredName, "second", []kubeEntry{
			{ctxName: "second", userName: "user-2", clusterName: "cluster-2"},
		})
		got, err := resolveKubeconfigSources(InitOptions{
			KubeconfigPath: configured,
		}, "", home)
		if err != nil {
			t.Fatalf("resolveKubeconfigSources: %v", err)
		}
		if got.mode != "single" || got.useRegistry || len(got.paths) != 1 || got.paths[0] != configured {
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
		if got.mode != "multi-dir" || !got.useRegistry || !got.ignoredKubeconfigEnv || got.ignoredKubeconfigEnvReason != "directories-only configuration" {
			t.Fatalf("resolution = %+v", got)
		}
		if len(got.paths) != 1 || got.paths[0] != additional {
			t.Fatalf("paths = %v, want only %q", got.paths, additional)
		}
		if len(got.directoryPaths) != 1 || got.directoryPaths[0] != additional {
			t.Fatalf("directory paths = %v, want only %q", got.directoryPaths, additional)
		}
	})

	t.Run("configured primary precedes directory files", func(t *testing.T) {
		got, err := resolveKubeconfigSources(InitOptions{
			KubeconfigPath: primary,
			KubeconfigDirs: []string{additionalDir},
		}, filepath.Join(home, "ambient"), home)
		if err != nil {
			t.Fatalf("resolveKubeconfigSources: %v", err)
		}
		if got.mode != "multi-source" || !got.useRegistry || got.directoryFileCount != 1 ||
			!got.ignoredKubeconfigEnv || got.ignoredKubeconfigEnvReason != "primary kubeconfig configured" {
			t.Fatalf("resolution = %+v", got)
		}
		if len(got.paths) != 2 || got.paths[0] != primary || got.paths[1] != additional {
			t.Fatalf("paths = %v, want [%q %q]", got.paths, primary, additional)
		}
		if len(got.directoryPaths) != 1 || got.directoryPaths[0] != additional {
			t.Fatalf("directory paths = %v, want only %q", got.directoryPaths, additional)
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

func TestKubeconfigDiagnosticErrorClassifiesWithoutDetails(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "empty", err: clientcmd.ErrEmptyConfig, want: "empty kubeconfig (no configuration provided)"},
		{name: "context", err: errors.New("context was not found for specified context"), want: "selected context not found"},
		{name: "switch context", err: fmt.Errorf("%w: prod", errKubeconfigContextNotFound), want: "selected context not found"},
		{name: "client setup", err: fmt.Errorf("%w: private details", errKubeconfigClientSetupFailed), want: "selected context client setup failed"},
		{name: "yaml syntax", err: errors.New("yaml: line 7: could not find expected ':' near private-value"), want: "invalid kubeconfig syntax"},
		{name: "unclassified", err: errors.New("server https://private.example and user alice"), want: "unclassified error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := kubeconfigDiagnosticError(tt.err); got != tt.want {
				t.Fatalf("diagnostic error = %q, want %q", got, tt.want)
			}
		})
	}

	got := kubeconfigDiagnosticError(&os.PathError{Op: "open", Path: "/private/config", Err: os.ErrNotExist})
	if strings.Contains(got, "/private/config") || !strings.Contains(got, "open: file does not exist") {
		t.Fatalf("path diagnostic error = %q", got)
	}
}

func TestInitMissingDefaultKubeconfigNamesLocalPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KUBECONFIG", "")
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")
	ResetTestState()
	t.Cleanup(ResetTestState)

	err := doInit(InitOptions{})
	wantPath := filepath.Join(home, ".kube", "config")
	if err == nil || !strings.Contains(err.Error(), wantPath) {
		t.Fatalf("doInit error = %v, want local path %q", err, wantPath)
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

}
