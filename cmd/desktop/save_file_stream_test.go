package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestSaveFileStreamWritesToDownloads(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	a := &DesktopApp{}
	content := bytes.Repeat([]byte("radar"), 100000)
	path, err := a.saveFileStream("export.csv", bytes.NewReader(content))
	if err != nil {
		t.Fatalf("saveFileStream failed: %v", err)
	}
	if want := filepath.Join(home, "Downloads", "export.csv"); path != want {
		t.Errorf("saved to %q, want %q", path, want)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("saved %d bytes, want %d identical bytes", len(got), len(content))
	}
}

func TestSaveFileStreamNamesAroundCollisions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	a := &DesktopApp{}
	first, err := a.saveFileStream("export.csv", strings.NewReader("one"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.saveFileStream("export.csv", strings.NewReader("two"))
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("a second save must not overwrite the first")
	}
	if want := filepath.Join(home, "Downloads", "export (1).csv"); second != want {
		t.Errorf("second save landed on %q, want %q", second, want)
	}
}

// A transfer that dies partway must not leave anything that looks like a
// finished download — the whole point of the partial-file dance.
func TestSaveFileStreamLeavesNothingBehindOnFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	a := &DesktopApp{}
	failing := io.MultiReader(strings.NewReader("half a file"), errReader{errors.New("connection lost")})
	if _, err := a.saveFileStream("export.csv", failing); err == nil {
		t.Fatal("expected the failed transfer to be reported")
	}

	entries, err := os.ReadDir(filepath.Join(home, "Downloads"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("leftover file after a failed transfer: %q", e.Name())
	}
}

// Two downloads of the same name must both land, whole. The name is claimed
// with O_EXCL rather than tested with Stat first for exactly this case.
func TestSaveFileStreamConcurrentSavesOfTheSameName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	const savers = 8
	a := &DesktopApp{}
	paths := make(chan string, savers)
	var wg sync.WaitGroup
	for i := 0; i < savers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := strings.Repeat(fmt.Sprintf("%d", i), 4096)
			path, err := a.saveFileStream("export.csv", strings.NewReader(body))
			if err != nil {
				t.Errorf("saver %d failed: %v", i, err)
				return
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Errorf("saver %d cannot read back %q: %v", i, path, err)
				return
			}
			if string(got) != body {
				t.Errorf("saver %d got %d bytes of the wrong content", i, len(got))
			}
			paths <- path
		}(i)
	}
	wg.Wait()
	close(paths)

	seen := map[string]bool{}
	for path := range paths {
		if seen[path] {
			t.Errorf("two savers landed on the same path %q", path)
		}
		seen[path] = true
	}
	if len(seen) != savers {
		t.Errorf("%d distinct files saved, want %d", len(seen), savers)
	}

	// Nothing partial may survive a run where every transfer succeeded.
	entries, err := os.ReadDir(filepath.Join(home, "Downloads"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".part") {
			t.Errorf("leftover partial file: %q", e.Name())
		}
	}
}

// A file name is never allowed to steer the write out of the Downloads folder.
func TestSaveFileStreamKeepsHostileNamesInsideDownloads(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	a := &DesktopApp{}
	path, err := a.saveFileStream("../../escaped.csv", strings.NewReader("x"))
	if err != nil {
		t.Fatalf("saveFileStream failed: %v", err)
	}
	if want := filepath.Join(home, "Downloads", "escaped.csv"); path != want {
		t.Errorf("saved to %q, want %q", path, want)
	}
}

// The exports people pull out of pods carry ISO timestamps, and a colon is a
// legal Linux file name character that Windows refuses outright.
func TestWindowsSafeName(t *testing.T) {
	for name, want := range map[string]string{
		"data_2026-08-27T10:02:03.511991310Z.csv": "data_2026-08-27T10-02-03.511991310Z.csv",
		"report<1>|2?.log":                        "report-1--2-.log",
		"trailing.  ":                             "trailing",
		"CON":                                     "_CON",
		"con.txt":                                 "_con.txt",
		"lpt9.log":                                "_lpt9.log",
		"ordinary.csv":                            "ordinary.csv",
		":::":                                     "---",
		"...":                                     "download",
	} {
		if got := windowsSafeName(name); got != want {
			t.Errorf("windowsSafeName(%q) = %q, want %q", name, got, want)
		}
	}
}

// A name is only rewritten where the host demands it; elsewhere the file keeps
// the name it had in the container.
func TestSanitizeDownloadNameKeepsPosixNamesIntact(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows rewrites reserved characters by design")
	}
	if got := sanitizeDownloadName("data_2026-08-27T10:02:03Z.csv"); got != "data_2026-08-27T10:02:03Z.csv" {
		t.Errorf("sanitizeDownloadName rewrote a name this host accepts: %q", got)
	}
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }
