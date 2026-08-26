//go:build !windows

package k8s

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDiscoverKubeconfigsSkipsFIFOAndKeepsRegularSymlink(t *testing.T) {
	dir := t.TempDir()
	regular := writeKubeconfig(t, dir, "regular.yaml", "prod", []kubeEntry{
		{ctxName: "prod", userName: "user", clusterName: "cluster"},
	})
	symlink := filepath.Join(dir, "linked.yaml")
	if err := os.Symlink(regular, symlink); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	directory := filepath.Join(dir, "nested")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	directorySymlink := filepath.Join(dir, "nested-link.yaml")
	if err := os.Symlink(directory, directorySymlink); err != nil {
		t.Fatalf("directory symlink: %v", err)
	}
	fifo := filepath.Join(dir, "blocked.pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	done := make(chan []string, 1)
	go func() { done <- discoverKubeconfigs([]string{dir}) }()

	var discovered []string
	select {
	case discovered = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("directory discovery blocked while inspecting a FIFO")
	}

	found := make(map[string]bool, len(discovered))
	for _, path := range discovered {
		found[path] = true
	}
	if !found[regular] || !found[symlink] {
		t.Fatalf("regular kubeconfig and symlink must be discovered, got %v", discovered)
	}
	if found[fifo] {
		t.Fatalf("FIFO must be skipped, got %v", discovered)
	}
	if found[directorySymlink] {
		t.Fatalf("symlink to a directory must be skipped, got %v", discovered)
	}
}

func TestHasUsableKubeconfigRejectsFIFOWithoutBlocking(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "primary.pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := hasUsableKubeconfig(fifo)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("hasUsableKubeconfig error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("primary kubeconfig validation blocked on a FIFO")
	}
}

func TestResolveKubeconfigSourcesSkipsFIFOSecondaryWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	regular := writeKubeconfig(t, dir, "regular.yaml", "prod", []kubeEntry{
		{ctxName: "prod", userName: "user", clusterName: "cluster"},
	})
	fifo := filepath.Join(dir, "secondary.pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	done := make(chan struct {
		sources kubeconfigSources
		err     error
	}, 1)
	go func() {
		sources, err := resolveKubeconfigSources(InitOptions{}, regular+string(os.PathListSeparator)+fifo, dir)
		done <- struct {
			sources kubeconfigSources
			err     error
		}{sources: sources, err: err}
	}()

	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("resolveKubeconfigSources: %v", result.err)
		}
		if len(result.sources.paths) != 1 || result.sources.paths[0] != regular {
			t.Fatalf("paths = %v, want [%q]", result.sources.paths, regular)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("KUBECONFIG resolution blocked on a FIFO secondary")
	}
}

func TestInitRejectsDefaultFIFOWithoutBlockingAfterInClusterFallback(t *testing.T) {
	home := t.TempDir()
	kubeDir := filepath.Join(home, ".kube")
	if err := os.Mkdir(kubeDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	fifo := filepath.Join(kubeDir, "config")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("KUBECONFIG", "")
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")
	ResetTestState()
	t.Cleanup(ResetTestState)

	done := make(chan error, 1)
	go func() { done <- doInit(InitOptions{}) }()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("doInit error = %v", err)
		}
		if IsInCluster() {
			t.Fatal("failed local kubeconfig initialization was classified as in-cluster")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("default kubeconfig resolution blocked on a FIFO")
	}
}
