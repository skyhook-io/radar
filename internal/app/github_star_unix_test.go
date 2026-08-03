//go:build !windows

package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTrackAndMaybePromptOnlyInvokesGitHubAfterConsent(t *testing.T) {
	originalInteractive := isInteractiveTerminal
	originalDelay := githubStarPromptDelay
	isInteractiveTerminal = func() bool { return true }
	githubStarPromptDelay = 0
	t.Cleanup(func() {
		isInteractiveTerminal = originalInteractive
		githubStarPromptDelay = originalDelay
	})

	tests := []struct {
		name          string
		response      string
		wantGHCall    bool
		wantStarred   bool
		wantDismissal bool
	}{
		{name: "decline", response: "n\n", wantDismissal: true},
		{name: "default decline", response: "\n", wantDismissal: true},
		{name: "accept", response: "y\n", wantGHCall: true, wantStarred: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			statePath := filepath.Join(home, ".radar", "star.json")
			saveStarState(statePath, &starState{Opens: 10})

			binDir := t.TempDir()
			marker := filepath.Join(t.TempDir(), "gh-called")
			fakeGH := filepath.Join(binDir, "gh")
			if err := os.WriteFile(fakeGH, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$GH_MARKER\"\n"), 0o755); err != nil {
				t.Fatalf("write fake gh: %v", err)
			}
			t.Setenv("HOME", home)
			t.Setenv("PATH", binDir)
			t.Setenv("GH_MARKER", marker)

			stdin, input, err := os.Pipe()
			if err != nil {
				t.Fatalf("create stdin pipe: %v", err)
			}
			if _, err := input.WriteString(tt.response); err != nil {
				t.Fatalf("write prompt response: %v", err)
			}
			if err := input.Close(); err != nil {
				t.Fatalf("close prompt input: %v", err)
			}
			originalStdin := os.Stdin
			os.Stdin = stdin
			t.Cleanup(func() {
				os.Stdin = originalStdin
				_ = stdin.Close()
			})

			if err := trackAndMaybePrompt(); err != nil {
				t.Fatalf("trackAndMaybePrompt: %v", err)
			}

			command, err := os.ReadFile(marker)
			if tt.wantGHCall {
				if err != nil {
					t.Fatalf("explicit acceptance did not invoke gh: %v", err)
				}
				if got := strings.TrimSpace(string(command)); got != "api user/starred/skyhook-io/radar -X PUT --silent" {
					t.Fatalf("gh arguments = %q", got)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("declining prompt invoked gh; stat error = %v", err)
			}

			state := loadStarState(statePath)
			if (state.StarredAt != "") != tt.wantStarred {
				t.Fatalf("starred state = %+v, want starred=%v", state, tt.wantStarred)
			}
			if (state.Dismissals > 0) != tt.wantDismissal {
				t.Fatalf("dismissal state = %+v, want dismissal=%v", state, tt.wantDismissal)
			}
		})
	}
}

func TestStarStatePromptSatisfiedSuppressesFuturePrompts(t *testing.T) {
	state := &starState{Opens: 100, PromptSatisfiedAt: "2026-08-03T10:00:00Z"}
	if state.shouldPrompt() {
		t.Fatal("web prompt acknowledgement did not suppress the CLI prompt")
	}
}
