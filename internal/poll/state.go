package poll

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// State is what a local Radar remembers about the poll, in ~/.radar/poll.json.
// In-cluster Radar keeps none: its home directory is an emptyDir shared by
// every viewer, so the browser keeps that state instead.
type State struct {
	ShownAt        time.Time `json:"shownAt,omitzero"`
	NeverAt        time.Time `json:"neverAt,omitzero"`
	SubmittedRound string    `json:"submittedRound,omitempty"`
	SubmittedAt    time.Time `json:"submittedAt,omitzero"`
}

// Store serializes read-modify-write cycles. Several tabs can dismiss, record
// a showing and submit at nearly the same moment.
type Store struct {
	mu   sync.Mutex
	path string
}

func NewStore(path string) *Store {
	return &Store{path: path}
}

// DefaultPath is ~/.radar/poll.json, or "" when there is no home directory.
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".radar", "poll.json")
}

func (s *Store) Load() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

// Update applies fn to the current state and saves the result.
func (s *Store) Update(fn func(*State)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.loadLocked()
	fn(&st)
	return s.saveLocked(st)
}

func (s *Store) loadLocked() State {
	if s.path == "" {
		return State{}
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return State{}
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return State{}
	}
	return st
}

func (s *Store) saveLocked(st State) error {
	if s.path == "" {
		return errors.New("no home directory to save poll state in")
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	// A unique temp file per write: two processes sharing ~/.radar (the CLI
	// and the desktop app) must not clobber each other's half-written file.
	tmp, err := os.CreateTemp(dir, "poll-*.json.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path)
}
