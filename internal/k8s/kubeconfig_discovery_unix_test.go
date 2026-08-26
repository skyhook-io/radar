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
