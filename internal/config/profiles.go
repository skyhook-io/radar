package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
)

var ErrProfileConflict = errors.New("cluster settings changed; reload Settings and try again")
var ErrProfileBusy = errors.New("cluster settings are busy; try again")
var ErrProfileInvalid = errors.New("invalid cluster settings")

type ClusterProfile struct {
	Context      string                              `json:"context"`
	Source       string                              `json:"source,omitempty"`
	InFileName   string                              `json:"inFileName,omitempty"`
	Integrations map[Integration]IntegrationSettings `json:"integrations"`
}

type ClusterProfiles struct {
	Version  int                       `json:"version"`
	Profiles map[string]ClusterProfile `json:"profiles"`
	Imported map[Integration]bool      `json:"imported,omitempty"`
	// PreviousNoticeDismissed silences reminders about the previous global
	// settings everywhere; they stay available to copy.
	PreviousNoticeDismissed bool `json:"previousNoticeDismissed,omitempty"`
}

type ProfileStore struct{ Path string }

func NewProfileStore() *ProfileStore {
	if Path() == "" {
		return &ProfileStore{}
	}
	return &ProfileStore{Path: filepath.Join(filepath.Dir(Path()), "clusters.json")}
}

func (s *ProfileStore) Read() (ClusterProfiles, string, error) {
	file, revision, _, err := s.ReadSince("")
	return file, revision, err
}

func (s *ProfileStore) ReadSince(previous string) (ClusterProfiles, string, bool, error) {
	result := ClusterProfiles{Version: 1, Profiles: map[string]ClusterProfile{}, Imported: map[Integration]bool{}}
	if s.Path == "" {
		return result, "", false, errors.New("cluster settings directory is unavailable")
	}
	data, revision, changed, err := s.file().ReadSince(previous)
	if err != nil || !changed || data == nil {
		return result, revision, changed, err
	}
	result, err = s.decode(data)
	if err != nil {
		return result, "", false, err
	}
	return result, revision, true, nil
}

func (s *ProfileStore) decode(data []byte) (ClusterProfiles, error) {
	result := ClusterProfiles{Version: 1, Profiles: map[string]ClusterProfile{}, Imported: map[Integration]bool{}}
	if data == nil {
		return result, nil
	}
	var header struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return result, invalidProfileJSON(s.Path, err)
	}
	if header.Version > 1 {
		return result, fmt.Errorf("clusters.json uses a newer format (version %d); upgrade Radar to read it; the file was not changed", header.Version)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	result = ClusterProfiles{}
	if err := dec.Decode(&result); err != nil {
		return result, invalidProfileJSON(s.Path, err)
	}
	if err := result.ValidateStructure(); err != nil {
		return result, fmt.Errorf("%s is invalid: %v. Fix it, or rename the file to start over without saved integration settings", s.Path, err)
	}
	return result, nil
}

func invalidProfileJSON(path string, err error) error {
	// Raw decoder errors can expose user-supplied keys or numeric values.
	var offset int64
	var syntax *json.SyntaxError
	var valueType *json.UnmarshalTypeError
	if errors.As(err, &syntax) {
		offset = syntax.Offset
	} else if errors.As(err, &valueType) {
		offset = valueType.Offset
	}
	location := ""
	if offset > 0 {
		location = fmt.Sprintf(" near byte %d", offset)
	}
	return fmt.Errorf("%s is invalid%s. Fix its JSON syntax, field names or value types, or rename the file to start over without saved integration settings", path, location)
}

func (s *ProfileStore) file() RevisionFile {
	return RevisionFile{Path: s.Path, MaxBytes: 4 * 1024 * 1024, Label: "cluster settings", TempPattern: ".clusters-*.json", Conflict: ErrProfileConflict, Busy: ErrProfileBusy, Validate: func(data []byte) error { _, err := s.decode(data); return err }}
}

func (s *ProfileStore) Update(ctx context.Context, revision string, mutate func(*ClusterProfiles) error) (string, error) {
	return s.file().Update(ctx, revision, func(data []byte) ([]byte, error) {
		current, err := s.decode(data)
		if err != nil {
			return nil, err
		}
		beforeData, err := json.Marshal(current)
		if err != nil {
			return nil, err
		}
		var before ClusterProfiles
		if err := json.Unmarshal(beforeData, &before); err != nil {
			return nil, err
		}
		if err := mutate(&current); err != nil {
			return nil, err
		}
		if err := current.ValidateStructure(); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrProfileInvalid, err)
		}
		if err := current.ValidateChanges(before); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrProfileInvalid, err)
		}
		data, err = json.MarshalIndent(current, "", "  ")
		if err != nil {
			return nil, err
		}
		data = append(data, '\n')
		return data, nil
	})
}
