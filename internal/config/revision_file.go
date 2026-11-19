package config

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

// RevisionFile serializes content-revision updates across Radar processes.
// Callers decode and validate their own typed payload under the file lock.
type RevisionFile struct {
	Path        string
	MaxBytes    int
	Label       string
	TempPattern string
	Conflict    error
	Busy        error
	Validate    func([]byte) error
}

func (s RevisionFile) ReadSince(previous string) ([]byte, string, bool, error) {
	if s.Path == "" {
		return nil, "", false, fmt.Errorf("%s directory is unavailable", s.Label)
	}
	f, err := os.Open(s.Path)
	if os.IsNotExist(err) {
		rev := profileRevision(nil)
		return nil, rev, previous != rev, nil
	}
	if err != nil {
		return nil, "", false, fmt.Errorf("cannot read %s: %w", s.Label, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, int64(s.MaxBytes)+1))
	if err != nil {
		return nil, "", false, fmt.Errorf("cannot read %s: %w", s.Label, err)
	}
	if len(data) > s.MaxBytes {
		return nil, "", false, fmt.Errorf("%s exceeds %d MiB. Rename the file to start over without saved integration settings", s.Path, s.MaxBytes/(1024*1024))
	}
	rev := profileRevision(data)
	return data, rev, previous != rev, nil
}

func (s RevisionFile) Update(ctx context.Context, revision string, mutate func([]byte) ([]byte, error)) (string, error) {
	if s.Path == "" {
		return "", fmt.Errorf("%s directory is unavailable", s.Label)
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0700); err != nil {
		return "", err
	}
	lock := flock.New(s.Path+".lock", flock.SetPermissions(0600))
	defer lock.Close()
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	locked, err := lock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return "", s.Busy
		}
		return "", err
	}
	if !locked {
		return "", s.Busy
	}
	defer lock.Unlock()
	current, actual, _, err := s.ReadSince("")
	if err != nil {
		return "", err
	}
	if s.Validate != nil {
		if err := s.Validate(current); err != nil {
			return "", err
		}
	}
	if actual != revision {
		return "", s.Conflict
	}
	data, err := mutate(current)
	if err != nil {
		return "", err
	}
	if len(data) > s.MaxBytes {
		return "", fmt.Errorf("%s exceed %d MiB", s.Label, s.MaxBytes/(1024*1024))
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.Path), s.TempPattern)
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return "", err
	}
	if err = tmp.Sync(); err != nil {
		tmp.Close()
		return "", err
	}
	if err = tmp.Close(); err != nil {
		return "", err
	}
	if err = os.Rename(tmp.Name(), s.Path); err != nil {
		return "", err
	}
	return profileRevision(data), nil
}

func profileRevision(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
