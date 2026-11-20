package server

import (
	"encoding/json"
	"log"
	"net/http"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/skyhook-io/radar/internal/desktopenv"
	"github.com/skyhook-io/radar/internal/version"
)

// handleDesktopOpenFolder reveals a file in the system file manager.
// POST /api/desktop/open-folder
func (s *Server) handleDesktopOpenFolder(w http.ResponseWriter, r *http.Request) {
	if !version.IsDesktop() {
		s.writeError(w, http.StatusNotFound, "not available")
		return
	}

	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Sanitize and validate path
	req.Path = filepath.Clean(req.Path)
	if !filepath.IsAbs(req.Path) {
		s.writeError(w, http.StatusBadRequest, "absolute path required")
		return
	}

	revealInFileManager(req.Path)
	w.WriteHeader(http.StatusNoContent)
}

// handleDesktopOpenFile opens a file with the system default application.
// POST /api/desktop/open-file
func (s *Server) handleDesktopOpenFile(w http.ResponseWriter, r *http.Request) {
	if !version.IsDesktop() {
		s.writeError(w, http.StatusNotFound, "not available")
		return
	}

	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.Path = filepath.Clean(req.Path)
	if !filepath.IsAbs(req.Path) {
		s.writeError(w, http.StatusBadRequest, "absolute path required")
		return
	}

	openWithDefaultApp(req.Path)
	w.WriteHeader(http.StatusNoContent)
}

// openWithDefaultApp opens a file with the OS default application.
func openWithDefaultApp(path string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", path)
	case "linux":
		cmd = exec.Command("xdg-open", path)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", path)
	default:
		log.Printf("[desktop] Cannot open file on %s", runtime.GOOS)
		return
	}
	cmd.Dir = desktopenv.ChildDir()
	if err := cmd.Start(); err != nil {
		log.Printf("[desktop] Failed to open file: %v", err)
	}
}

// revealInFileManager opens the file manager with the given file selected.
func revealInFileManager(path string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", "-R", path)
	case "linux":
		cmd = exec.Command("xdg-open", filepath.Dir(path))
	case "windows":
		cmd = exec.Command("explorer", "/select,", path)
	default:
		log.Printf("[desktop] Cannot open file manager on %s", runtime.GOOS)
		return
	}
	cmd.Dir = desktopenv.ChildDir()
	if err := cmd.Start(); err != nil {
		log.Printf("[desktop] Failed to reveal in file manager: %v", err)
	}
}
