package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"

	"github.com/Masterminds/semver/v3"

	"github.com/skyhook-io/radar/internal/version"
)

// Which release notes the user has already seen. A personal install (the CLI or
// Desktop, no auth) keeps this in ~/.radar because its browser origin is not
// stable — Desktop binds a random port on every launch, so browser storage would
// read each upgrade as a fresh install. A shared install (in-cluster, or any
// install with auth on) serves many people, so each browser keeps its own.

const whatsNewFile = "whats-new.json"

// Captured before main writes anything under ~/.radar (install-id, star.json),
// which would otherwise make a first run look like an existing install.
var whatsNewPriorInstall = radarDirHadState()

var whatsNewVersionPattern = regexp.MustCompile(`^v?\d+\.\d+\.\d+[0-9A-Za-z.+-]*$`)

type whatsNewState struct {
	SeenVersion string `json:"seen_version,omitempty"`
}

type whatsNewResponse struct {
	CurrentVersion string `json:"currentVersion"`
	// "server": seenVersion / priorInstall below are authoritative.
	// "browser": the client keeps its own record.
	Storage      string  `json:"storage"`
	SeenVersion  *string `json:"seenVersion,omitempty"`
	PriorInstall bool    `json:"priorInstall,omitempty"`
}

type whatsNewSeenRequest struct {
	Version string `json:"version"`
}

// Same split as preference storage in handleGetSettings.
var whatsNewUsesServerStorage = func(s *Server) bool {
	return s.configManagement() == "local"
}

func (s *Server) handleGetWhatsNew(w http.ResponseWriter, r *http.Request) {
	resp := whatsNewResponse{CurrentVersion: version.Current, Storage: "browser"}
	if whatsNewUsesServerStorage(s) {
		resp.Storage = "server"
		if state, ok := readWhatsNewState(); ok {
			resp.SeenVersion = &state.SeenVersion
		}
		resp.PriorInstall = whatsNewPriorInstall
	}
	s.writeJSON(w, resp)
}

func (s *Server) handleMarkWhatsNewSeen(w http.ResponseWriter, r *http.Request) {
	if !whatsNewUsesServerStorage(s) {
		s.writeError(w, http.StatusConflict, "What's New state is kept in the browser on this install")
		return
	}
	var req whatsNewSeenRequest
	if err := decodeBoundedJSONBody(w, r, 1<<10, &req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.writeError(w, http.StatusRequestEntityTooLarge, "request body is too large")
			return
		}
		s.writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	if len(req.Version) > 64 || !whatsNewVersionPattern.MatchString(req.Version) {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid version %q", req.Version))
		return
	}
	// Two Radars can share ~/.radar (an older CLI beside an updated Desktop);
	// the record only moves forward, or the newer one would replay its notes.
	if prev, ok := readWhatsNewState(); ok && !versionAfter(req.Version, prev.SeenVersion) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := writeWhatsNewState(whatsNewState{SeenVersion: req.Version}); err != nil {
		log.Printf("[whats-new] Failed to record seen version %s: %v", req.Version, err)
		s.writeError(w, http.StatusInternalServerError, "failed to record the seen version")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// versionAfter reports whether a is a later version than b. An unparseable
// recorded version is treated as older, so a valid one replaces it.
func versionAfter(a, b string) bool {
	va, err := semver.NewVersion(a)
	if err != nil {
		return false
	}
	vb, err := semver.NewVersion(b)
	if err != nil {
		return true
	}
	return va.GreaterThan(vb)
}

func radarDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".radar"), nil
}

func radarDirHadState() bool {
	dir, err := radarDir()
	if err != nil {
		return false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("[whats-new] Failed to read %s: %v", dir, err)
		}
		return false
	}
	for _, e := range entries {
		if e.Name() != whatsNewFile {
			return true
		}
	}
	return false
}

// ok is false when nothing has been recorded yet (or the file is unreadable),
// which the client must tell apart from an empty seen version.
func readWhatsNewState() (whatsNewState, bool) {
	dir, err := radarDir()
	if err != nil {
		return whatsNewState{}, false
	}
	path := filepath.Join(dir, whatsNewFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			log.Printf("[whats-new] Failed to read %s: %v", path, err)
		}
		return whatsNewState{}, false
	}
	var state whatsNewState
	if err := json.Unmarshal(data, &state); err != nil {
		log.Printf("[whats-new] Ignoring unreadable %s: %v", path, err)
		return whatsNewState{}, false
	}
	if state.SeenVersion == "" {
		return whatsNewState{}, false
	}
	return state, true
}

func writeWhatsNewState(state whatsNewState) error {
	dir, err := radarDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	// A unique temp file: two Radars can share ~/.radar.
	tmp, err := os.CreateTemp(dir, whatsNewFile+".*.tmp")
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
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, whatsNewFile))
}
