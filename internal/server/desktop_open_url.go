package server

import (
	"encoding/json"
	"log"
	"net/http"
	"os/exec"
	"runtime"
	"strings"

	"github.com/skyhook-io/radar/internal/desktopenv"
	"github.com/skyhook-io/radar/internal/version"
)

// handleDesktopOpenURL opens a URL in the user's system browser.
// In the desktop app, window.open() is swallowed by the Wails webview
// (the JS runtime is lost after the redirect to localhost), so the
// frontend calls this endpoint instead.
// POST /api/desktop/open-url
func (s *Server) handleDesktopOpenURL(w http.ResponseWriter, r *http.Request) {
	if !version.IsDesktop() {
		s.writeError(w, http.StatusNotFound, "not available")
		return
	}

	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Only allow http/https URLs to prevent command injection
	if !strings.HasPrefix(req.URL, "http://") && !strings.HasPrefix(req.URL, "https://") {
		s.writeError(w, http.StatusBadRequest, "only http and https URLs are allowed")
		return
	}

	OpenSystemBrowser(req.URL)
	w.WriteHeader(http.StatusNoContent)
}

// OpenSystemBrowser opens a URL in the default browser using OS-specific commands.
func OpenSystemBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		log.Printf("[desktop] Cannot open browser on %s: %s", runtime.GOOS, url)
		return
	}
	cmd.Dir = desktopenv.ChildDir()
	if err := cmd.Start(); err != nil {
		log.Printf("[desktop] Failed to open URL: %v", err)
	}
}
