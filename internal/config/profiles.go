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
)

var ErrProfileConflict = errors.New("cluster settings changed; reload Settings and try again")
var ErrProfileBusy = errors.New("cluster settings are busy; try again")
var ErrProfileInvalid = errors.New("invalid cluster settings")

type ClusterProfile struct {
	Context      string                              `json:"context"`
	Source       string                              `json:"source,omitempty"`
	InFileName   string                              `json:"inFileName,omitempty"`
	CAPI         *CAPIProfileReference               `json:"capi,omitempty"`
	Integrations map[Integration]IntegrationSettings `json:"integrations"`
}

type CAPIProfileReference struct {
	ManagementBinding string `json:"managementBinding"`
	Namespace         string `json:"namespace"`
	Name              string `json:"name"`
}

type ClusterProfiles struct {
	Version   int                             `json:"version"`
	Profiles  map[string]ClusterProfile       `json:"profiles"`
	Imported  map[Integration]bool            `json:"imported,omitempty"`
	Dismissed map[string]map[Integration]bool `json:"dismissed,omitempty"`
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
	file, revision, _, err := s.ReadSince("")
	return file, revision, err
}

func (s *ProfileStore) ReadSince(previous string) (ClusterProfiles, string, bool, error) {
	result := ClusterProfiles{Version: 1, Profiles: map[string]ClusterProfile{}, Imported: map[Integration]bool{}, Dismissed: map[string]map[Integration]bool{}}
	if s.Path == "" {
		return result, "", false, errors.New("cluster settings directory is unavailable")
	}
	f, err := os.Open(s.Path)
	if os.IsNotExist(err) {
		return result, profileRevision(nil), previous != profileRevision(nil), nil
	}
	if err != nil {
		return result, "", false, fmt.Errorf("cannot read cluster settings: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 4*1024*1024+1))
	if err != nil {
		return result, "", false, err
	}
	if len(data) > 4*1024*1024 {
		return result, "", false, errors.New("cluster settings exceed 4 MiB")
	}
	revision := profileRevision(data)
	if previous == revision {
		return result, revision, false, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	result = ClusterProfiles{}
	if dec.Decode(&result) != nil || dec.Decode(new(any)) != io.EOF {
		return result, "", false, errors.New("invalid clusters.json; repair the file before saving settings")
	}
	if err := result.ValidateStructure(); err != nil {
		return result, "", false, err
	}
	return result, revision, true, nil
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
	beforeData, err := json.Marshal(current)
	if err != nil {
		return "", err
	}
	var before ClusterProfiles
	if err := json.Unmarshal(beforeData, &before); err != nil {
		return "", err
	}
	if err := mutate(&current); err != nil {
		return "", err
	}
	if err := current.ValidateStructure(); err != nil {
		return "", fmt.Errorf("%w: %v", ErrProfileInvalid, err)
	}
	if err := current.ValidateChanges(before); err != nil {
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
