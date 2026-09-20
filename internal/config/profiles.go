package config

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
	"github.com/skyhook-io/radar/pkg/prom"
)

var ErrProfileConflict = errors.New("cluster settings changed; reload Settings and try again")
var ErrProfileBusy = errors.New("cluster settings are busy; try again")
var ErrProfileInvalid = errors.New("invalid cluster settings")

type ClusterProfile struct {
	Context     string          `json:"context"`
	Source      string          `json:"source,omitempty"`
	Description string          `json:"description,omitempty"`
	Target      string          `json:"target"`
	Prometheus  prom.Connection `json:"prometheus"`
}

type ClusterProfiles struct {
	Version                     int                       `json:"version"`
	Profiles                    map[string]ClusterProfile `json:"profiles"`
	PrometheusAdopted           map[string]bool           `json:"prometheusAdopted,omitempty"`
	PrometheusMigrationComplete bool                      `json:"prometheusMigrationComplete,omitempty"`
}

type ProfileStore struct{ Path string }

func NewProfileStore() *ProfileStore {
	if Path() == "" {
		return &ProfileStore{}
	}
	return &ProfileStore{Path: filepath.Join(filepath.Dir(Path()), "clusters.json")}
}

func profileRevision(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func (s *ProfileStore) Read() (ClusterProfiles, string, error) {
	result := ClusterProfiles{Version: 1, Profiles: map[string]ClusterProfile{}, PrometheusAdopted: map[string]bool{}}
	if s.Path == "" {
		return result, "", errors.New("cluster settings directory is unavailable")
	}
	f, err := os.Open(s.Path)
	if os.IsNotExist(err) {
		return result, profileRevision(nil), nil
	}
	if err != nil {
		return result, "", fmt.Errorf("cannot read cluster settings: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 4*1024*1024+1))
	if err != nil {
		return result, "", err
	}
	if len(data) > 4*1024*1024 {
		return result, "", errors.New("cluster settings exceed 4 MiB")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	result = ClusterProfiles{}
	if dec.Decode(&result) != nil || dec.Decode(new(any)) != io.EOF {
		return result, "", errors.New("invalid clusters.json; repair the file before saving settings")
	}
	if err := result.validate(); err != nil {
		return result, "", err
	}
	return result, profileRevision(data), nil
}

func (p ClusterProfiles) validate() error {
	if p.Version != 1 {
		return errors.New("unsupported clusters.json version (expected 1)")
	}
	if p.Profiles == nil {
		return errors.New("clusters.json requires a profiles object")
	}
	for binding, profile := range p.Profiles {
		if binding == "" || profile.Target == "" || profile.Context == "" {
			return errors.New("cluster profile requires binding, target and context")
		}
		if err := profile.Prometheus.Validate(); err != nil {
			return fmt.Errorf("invalid cluster profile: %w", err)
		}
	}
	return nil
}

func (s *ProfileStore) Update(ctx context.Context, revision string, mutate func(*ClusterProfiles) error) (string, error) {
	if s.Path == "" {
		return "", errors.New("cluster settings directory is unavailable")
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
			return "", ErrProfileBusy
		}
		return "", err
	}
	if !locked {
		return "", ErrProfileBusy
	}
	defer lock.Unlock()
	current, actual, err := s.Read()
	if err != nil {
		return "", err
	}
	if revision != actual {
		return "", ErrProfileConflict
	}
	if err := mutate(&current); err != nil {
		return "", err
	}
	if err := current.validate(); err != nil {
		return "", fmt.Errorf("%w: %v", ErrProfileInvalid, err)
	}
	data, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return "", err
	}
	data = append(data, '\n')
	if len(data) > 4*1024*1024 {
		return "", errors.New("cluster settings exceed 4 MiB")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.Path), ".clusters-*.json")
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
