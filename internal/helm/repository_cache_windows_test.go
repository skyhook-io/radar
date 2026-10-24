package helm

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReplaceRepositoryCacheFileWaitsForWindowsReader(t *testing.T) {
	dir := t.TempDir()
	source, destination := filepath.Join(dir, "staged.yaml"), filepath.Join(dir, "live.yaml")
	if err := os.WriteFile(source, []byte("new index"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("old index"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := os.Rename(source, destination); err == nil {
		t.Fatal("expected Windows reader to block direct replacement")
	}
	released := make(chan error, 1)
	go func() { time.Sleep(100 * time.Millisecond); released <- reader.Close() }()
	if err := replaceRepositoryCacheFile(source, destination); err != nil {
		t.Fatal(err)
	}
	if err := <-released; err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(destination)
	if err != nil || string(body) != "new index" {
		t.Fatalf("replacement = %q, %v", body, err)
	}
}

func TestReplaceRepositoryCacheFilePreservesLockedWindowsIndex(t *testing.T) {
	dir := t.TempDir()
	source, destination := filepath.Join(dir, "staged.yaml"), filepath.Join(dir, "live.yaml")
	if err := os.WriteFile(source, []byte("new index"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("old index"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := replaceRepositoryCacheFile(source, destination); err == nil {
		t.Fatal("expected locked index error")
	}
	body, err := os.ReadFile(destination)
	if err != nil || string(body) != "old index" {
		t.Fatalf("locked index changed = %q, %v", body, err)
	}
}
