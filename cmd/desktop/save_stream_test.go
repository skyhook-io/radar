package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// errReader yields prefix, then fails — standing in for a download that dies
// partway through.
type errReader struct {
	prefix string
	pos    int
}

func (r *errReader) Read(p []byte) (int, error) {
	if r.pos < len(r.prefix) {
		n := copy(p, r.prefix[r.pos:])
		r.pos += n
		return n, nil
	}
	return 0, errors.New("transport died")
}

func TestSaveStreamWritesFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	a := &DesktopApp{}

	path, err := a.saveStream("app.log", strings.NewReader("hello world"))
	if err != nil {
		t.Fatalf("saveStream: %v", err)
	}
	if want := filepath.Join(home, "Downloads", "app.log"); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != "hello world" {
		t.Errorf("content = %q, want %q", data, "hello world")
	}
}

func TestSaveStreamAvoidsCollisions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	a := &DesktopApp{}

	if _, err := a.saveStream("app.log", strings.NewReader("first")); err != nil {
		t.Fatalf("first save: %v", err)
	}
	path, err := a.saveStream("app.log", strings.NewReader("second"))
	if err != nil {
		t.Fatalf("second save: %v", err)
	}
	if want := filepath.Join(home, "Downloads", "app (1).log"); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	data, _ := os.ReadFile(filepath.Join(home, "Downloads", "app.log"))
	if string(data) != "first" {
		t.Errorf("original file was overwritten: %q", data)
	}
}

// A download that dies mid-copy must not leave a truncated file sitting in
// Downloads looking like a successful save.
func TestSaveStreamRemovesPartialFileOnError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	a := &DesktopApp{}

	_, err := a.saveStream("big.bin", &errReader{prefix: strings.Repeat("x", 4096)})
	if err == nil {
		t.Fatal("saveStream succeeded, want the underlying read error")
	}
	entries, readErr := os.ReadDir(filepath.Join(home, "Downloads"))
	if readErr != nil {
		t.Fatalf("read Downloads: %v", readErr)
	}
	if len(entries) != 0 {
		t.Errorf("Downloads contains %d file(s), want none", len(entries))
	}
}

func TestSaveStreamAndSaveFileAgreeOnPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	a := &DesktopApp{}

	first, err := a.saveFile("notes.txt", []byte("a"))
	if err != nil {
		t.Fatalf("saveFile: %v", err)
	}
	second, err := a.saveStream("notes.txt", io.LimitReader(strings.NewReader("bb"), 2))
	if err != nil {
		t.Fatalf("saveStream: %v", err)
	}
	if first == second {
		t.Fatalf("both saves resolved to %q; collision handling is not shared", first)
	}
	if want := filepath.Join(home, "Downloads", "notes (1).txt"); second != want {
		t.Errorf("second path = %q, want %q", second, want)
	}
}
