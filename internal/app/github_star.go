package app

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"
)

const githubRepo = "skyhook-io/radar"

// starState tracks GitHub star prompt history across launches.
type starState struct {
	Opens       int    `json:"opens"`                  // Total radar launches
	StarredAt   string `json:"starred_at,omitempty"`   // Set when user starred via prompt
	DismissedAt string `json:"dismissed_at,omitempty"` // Last time user said no
	Dismissals  int    `json:"dismissals"`             // Number of times dismissed
	PromptedAt  string `json:"prompted_at,omitempty"`  // Last time we showed the prompt
}

// Prompt schedule: show at open 3, then back off with increasing gaps.
// After each dismissal, wait longer before asking again.
// Note: thresholds[0] is only used by the PromptedAt=="" path (first prompt).
// Subsequent prompts use thresholds[dismissals] where dismissals >= 1.
func (s *starState) shouldPrompt() bool {
	if s.StarredAt != "" {
		return false
	}

	if s.Opens < 3 {
		return false
	}

	// First prompt: 3+ opens and never prompted before
	if s.PromptedAt == "" {
		return true
	}

	// Don't show more than one prompt per 48 hours (prevents CLI + UI double-prompt)
	if prompted, err := time.Parse(time.RFC3339, s.PromptedAt); err == nil {
		if time.Since(prompted) < 48*time.Hour {
			return false
		}
	}

	// Backoff: prompt at increasing open thresholds after each dismissal
	thresholds := []int{3, 10, 20, 30, 50, 100}
	if s.Dismissals >= len(thresholds) {
		return false
	}
	return s.Opens >= thresholds[s.Dismissals]
}

// MaybePromptGitHubStar increments the open counter and may prompt the user to star.
// Runs asynchronously with a delay so it doesn't interrupt startup.
func MaybePromptGitHubStar() {
	go func() {
		if err := trackAndMaybePrompt(); err != nil {
			log.Printf("[star] %v", err)
		}
	}()
}

// IsStarred returns whether the user has starred the repo (based on cached state).
func IsStarred() bool {
	statePath, err := starStatePath()
	if err != nil {
		return false
	}
	state := loadStarState(statePath)
	return state.StarredAt != ""
}

// checkAndUpdateStarred checks the GitHub API via gh CLI and updates the cached state.
// Returns true if the user has starred the repo.
func checkAndUpdateStarred(statePath string, state *starState) bool {
	ghPath, _ := exec.LookPath("gh")
	if ghPath == "" {
		return state.StarredAt != ""
	}
	if err := exec.Command(ghPath, "auth", "status").Run(); err != nil {
		return state.StarredAt != ""
	}
	if err := exec.Command(ghPath, "api", "user/starred/"+githubRepo, "--silent").Run(); err == nil {
		// 204 No Content — user has starred
		if state.StarredAt == "" {
			state.StarredAt = time.Now().Format(time.RFC3339)
			saveStarState(statePath, state)
		}
		return true
	}
	// The error could be 404 (not starred) or a transient failure (network, rate limit).
	// Only clear cached StarredAt if we can confirm it's a 404 by re-checking with
	// a command that surfaces the HTTP status code.
	out, err := exec.Command(ghPath, "api", "user/starred/"+githubRepo, "-i", "--silent").CombinedOutput()
	if err != nil && strings.Contains(string(out), "HTTP 404") {
		if state.StarredAt != "" {
			state.StarredAt = ""
			saveStarState(statePath, state)
		}
	}
	// For any other error (network, rate limit, etc.), keep cached state as-is
	return false
}

func trackAndMaybePrompt() error {
	statePath, err := starStatePath()
	if err != nil {
		return fmt.Errorf("cannot determine state path: %w", err)
	}

	// Load or create state
	state := loadStarState(statePath)
	state.Opens++
	saveStarState(statePath, state)

	// Always check starred status in background (updates star.json for the UI)
	if checkAndUpdateStarred(statePath, state) {
		return nil // Already starred, no need to prompt
	}

	if !state.shouldPrompt() {
		return nil
	}

	// Only prompt in interactive terminals (check both stdin and stdout)
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return nil
	}

	// Mark as prompted immediately so the UI callout (which checks star.json)
	// won't also show a prompt during the sleep window below.
	state.PromptedAt = time.Now().Format(time.RFC3339)
	saveStarState(statePath, state)

	// Wait for the user to see the UI before prompting
	time.Sleep(5 * time.Second)

	// Re-check gh availability for the interactive prompt
	ghPath, _ := exec.LookPath("gh")
	ghReady := ghPath != ""
	if ghReady {
		if err := exec.Command(ghPath, "auth", "status").Run(); err != nil {
			ghReady = false
		}
	}

	if ghReady {
		// Interactive prompt — can star for them
		fmt.Printf("\n  ⭐ Enjoying Radar? Star us on GitHub! [Y/n]: ")
		reader := bufio.NewReader(os.Stdin)
		response, err := reader.ReadString('\n')
		if err != nil {
			// stdin closed/broken — don't auto-star, just save and bail
			state.Dismissals++
			saveStarState(statePath, state)
			return nil
		}
		response = strings.TrimSpace(strings.ToLower(response))

		if response == "" || response == "y" || response == "yes" {
			cmd := exec.Command(ghPath, "api", "user/starred/"+githubRepo, "-X", "PUT", "--silent")
			if err := cmd.Run(); err != nil {
				fmt.Printf("  Hmm, that didn't work — you can star manually at https://github.com/%s\n\n", githubRepo)
				state.Dismissals++
			} else {
				fmt.Printf("  Thanks for your support! ⭐\n\n")
				state.StarredAt = time.Now().Format(time.RFC3339)
			}
		} else {
			fmt.Println()
			state.DismissedAt = time.Now().Format(time.RFC3339)
			state.Dismissals++
		}
	} else {
		// No gh CLI — just nudge with a link
		fmt.Printf("\n  ⭐ Enjoying Radar? Star us on GitHub → https://github.com/%s\n\n", githubRepo)
		state.DismissedAt = time.Now().Format(time.RFC3339)
		state.Dismissals++
	}

	saveStarState(statePath, state)
	return nil
}

func starStatePath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, ".radar", "star.json"), nil
}

func loadStarState(path string) *starState {
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[star] Failed to read state file %s: %v", path, err)
		}
		return &starState{}
	}
	var state starState
	if err := json.Unmarshal(data, &state); err != nil {
		log.Printf("[star] Corrupt state file %s (resetting): %v", path, err)
		return &starState{}
	}
	return &state
}

func saveStarState(path string, state *starState) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("[star] Failed to create directory %s: %v", dir, err)
		return
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		log.Printf("[star] Failed to marshal state: %v", err)
		return
	}
	// Atomic write: temp file + rename to avoid corruption on crash
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		log.Printf("[star] Failed to write state file %s: %v", tmp, err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.Printf("[star] Failed to rename state file: %v", err)
	}
}
