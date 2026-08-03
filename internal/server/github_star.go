package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type starStatusResponse struct {
	Starred      bool `json:"starred"`
	ShouldPrompt bool `json:"shouldPrompt"`
}

// handleGitHubStarStatus returns cached prompt state. It deliberately does not
// inspect the user's GitHub account.
func (s *Server) handleGitHubStarStatus(w http.ResponseWriter, r *http.Request) {
	state := readStarJSON()
	resp := starStatusResponse{
		Starred:      state.StarredAt != "",
		ShouldPrompt: state.shouldPromptUI(),
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleGitHubStarIntent records that the user followed the prompt's explicit
// "Star on GitHub" action. The browser opens GitHub itself; this endpoint only
// silences the local prompt. It never contacts GitHub, inspects an account, or
// claims that the repository was actually starred.
func (s *Server) handleGitHubStarIntent(w http.ResponseWriter, r *http.Request) {
	state := readStarJSON()
	state.PromptSatisfiedAt = time.Now().Format(time.RFC3339)
	state.PromptedAt = state.PromptSatisfiedAt
	writeStarJSON(state)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"acknowledged": true})
}

// handleGitHubDismiss records a UI prompt dismissal.
func (s *Server) handleGitHubDismiss(w http.ResponseWriter, r *http.Request) {
	state := readStarJSON()
	state.DismissedAt = time.Now().Format(time.RFC3339)
	state.Dismissals++
	state.PromptedAt = time.Now().Format(time.RFC3339)
	writeStarJSON(state)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"dismissed": true})
}

// githubStarState mirrors the structure in app/github_star.go.
type githubStarState struct {
	Opens             int    `json:"opens"`
	StarredAt         string `json:"starred_at,omitempty"`
	PromptSatisfiedAt string `json:"prompt_satisfied_at,omitempty"`
	DismissedAt       string `json:"dismissed_at,omitempty"`
	Dismissals        int    `json:"dismissals"`
	PromptedAt        string `json:"prompted_at,omitempty"`
}

// shouldPromptUI uses the same backoff schedule as the CLI prompt.
func (s *githubStarState) shouldPromptUI() bool {
	if s.StarredAt != "" || s.PromptSatisfiedAt != "" {
		return false
	}
	if s.Opens < 3 {
		return false
	}
	if s.PromptedAt == "" {
		return true
	}
	// Don't show more than one prompt per 48 hours (prevents CLI + UI double-prompt)
	if prompted, err := time.Parse(time.RFC3339, s.PromptedAt); err == nil {
		if time.Since(prompted) < 48*time.Hour {
			return false
		}
	}
	thresholds := []int{3, 10, 20, 30, 50, 100}
	if s.Dismissals >= len(thresholds) {
		return false
	}
	return s.Opens >= thresholds[s.Dismissals]
}

func readStarJSON() *githubStarState {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return &githubStarState{}
	}
	data, err := os.ReadFile(filepath.Join(homeDir, ".radar", "star.json"))
	if err != nil {
		return &githubStarState{}
	}
	var state githubStarState
	if err := json.Unmarshal(data, &state); err != nil {
		return &githubStarState{}
	}
	return &state
}

func writeStarJSON(state *githubStarState) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return
	}
	path := filepath.Join(homeDir, ".radar", "star.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	os.Rename(tmp, path)
}
