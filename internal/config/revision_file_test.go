package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

func TestRevisionFileDefaultErrors(t *testing.T) {
	s := RevisionFile{Path: filepath.Join(t.TempDir(), "state.json"), MaxBytes: 128}
	mutate := func([]byte) ([]byte, error) {
		t.Fatal("rejected update called mutate")
		return nil, nil
	}
	if rev, err := s.Update(context.Background(), "stale", mutate); rev != "" || !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale update = (%q, %v), want ErrRevisionConflict", rev, err)
	}

	lock := flock.New(s.Path + ".lock")
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if rev, err := s.Update(ctx, contentRevision(nil), mutate); rev != "" || !errors.Is(err, ErrRevisionBusy) {
		t.Fatalf("contended update = (%q, %v), want ErrRevisionBusy", rev, err)
	}
}

func TestRevisionFileSizeLimit(t *testing.T) {
	s := RevisionFile{Path: filepath.Join(t.TempDir(), "state.json"), MaxBytes: 512 << 10}
	oversized := make([]byte, s.MaxBytes+1)
	want := fmt.Sprintf("%s exceeds the 524288-byte limit", s.Path)
	if _, err := s.Update(context.Background(), contentRevision(nil), func([]byte) ([]byte, error) {
		return oversized, nil
	}); err == nil || err.Error() != want {
		t.Fatalf("oversized Update() error = %v, want %q", err, want)
	}
	if _, err := os.Stat(s.Path); !os.IsNotExist(err) {
		t.Fatalf("rejected write left a file: %v", err)
	}
	if err := os.WriteFile(s.Path, oversized, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.ReadSince(""); err == nil || err.Error() != want {
		t.Fatalf("oversized ReadSince() error = %v, want %q", err, want)
	}

	const custom = "The workspace state file is larger than 512 KiB"
	s.TooLarge = func(writing bool) string {
		if writing {
			t.Fatal("oversized read was reported as a write")
		}
		return custom
	}
	if _, _, _, err := s.ReadSince(""); err == nil || err.Error() != custom {
		t.Fatalf("oversized ReadSince() error = %v, want %q", err, custom)
	}
}

func TestRevisionFileValidatesBeforeConflict(t *testing.T) {
	invalid := errors.New("invalid workspace state")
	s := RevisionFile{
		Path: filepath.Join(t.TempDir(), "state.json"), MaxBytes: 128,
		Validate: func(data []byte) error {
			if string(data) != "invalid" {
				t.Fatalf("validated bytes = %q, want invalid", data)
			}
			return invalid
		},
	}
	if err := os.WriteFile(s.Path, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := s.Update(context.Background(), "stale", func([]byte) ([]byte, error) {
		t.Fatal("invalid file called mutate")
		return nil, nil
	})
	if !errors.Is(err, invalid) {
		t.Fatalf("invalid stale update error = %v, want %v", err, invalid)
	}
}

func TestRevisionFileMutationUsesCallerDeadline(t *testing.T) {
	s := RevisionFile{Path: filepath.Join(t.TempDir(), "state.json"), MaxBytes: 128}
	lock := flock.New(s.Path + ".lock")
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	released := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	unlock := time.AfterFunc(400*time.Millisecond, func() { released <- lock.Unlock() })
	defer unlock.Stop()

	revision, err := s.Update(ctx, contentRevision(nil), func([]byte) ([]byte, error) {
		if err := <-released; err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Until(start.Add(1100 * time.Millisecond)))
		return []byte("committed"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	data, actual, changed, err := s.ReadSince("")
	if err != nil || !changed || revision == "" || actual != revision || string(data) != "committed" {
		t.Fatalf("ReadSince() = (%q, %q, %v, %v), want committed revision %q", data, actual, changed, err, revision)
	}
}

func TestRevisionFileProfileSizeLimit(t *testing.T) {
	const oversized = 4*1024*1024 + 1

	t.Run("read", func(t *testing.T) {
		store := ProfileStore{Path: filepath.Join(t.TempDir(), "clusters.json")}
		if err := os.WriteFile(store.Path, make([]byte, oversized), 0600); err != nil {
			t.Fatal(err)
		}
		want := store.Path + " exceeds 4 MiB. Rename the file to start over without saved integration settings"
		if _, _, err := store.Read(); err == nil || err.Error() != want {
			t.Fatalf("Read() error = %v, want %q", err, want)
		}
	})

	t.Run("write", func(t *testing.T) {
		store := ProfileStore{Path: filepath.Join(t.TempDir(), "clusters.json")}
		_, revision, err := store.Read()
		if err != nil {
			t.Fatal(err)
		}
		_, err = store.Update(context.Background(), revision, func(profiles *ClusterProfiles) error {
			profiles.Profiles["binding"] = ClusterProfile{
				Context:      strings.Repeat("c", oversized),
				Integrations: map[Integration]IntegrationSettings{},
			}
			return nil
		})
		const want = "cluster settings exceed 4 MiB"
		if err == nil || err.Error() != want {
			t.Fatalf("Update() error = %v, want %q", err, want)
		}
		if _, err := os.Stat(store.Path); !os.IsNotExist(err) {
			t.Fatalf("rejected write left a settings file: %v", err)
		}
	})
}
