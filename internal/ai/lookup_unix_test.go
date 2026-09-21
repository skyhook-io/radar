//go:build !windows

package ai

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// notAnAgent is a name no real install can occupy, so negative cases stay
// hermetic on a developer machine that has the real CLIs in /usr/local/bin.
const notAnAgent = "radar-test-not-an-agent"

func writeExecutable(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLookupAgentFindsWellKnownInstallsOffPATH(t *testing.T) {
	cases := []struct {
		name string
		dir  []string
	}{
		{"native installer", []string{".local", "bin"}},
		{"claude migrate-installer (alias-only, never on PATH)", []string{".claude", "local"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("PATH", "")
			want := writeExecutable(t, filepath.Join(append([]string{home}, c.dir...)...), "claude")

			if got := lookupAgent("claude"); got != want {
				t.Errorf("lookupAgent(claude) = %q, want %q", got, want)
			}
		})
	}
}

func TestLookupAgentPrefersPATH(t *testing.T) {
	home := t.TempDir()
	onPath := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", onPath)
	writeExecutable(t, filepath.Join(home, ".local", "bin"), "claude")
	want := writeExecutable(t, onPath, "claude")

	if got := lookupAgent("claude"); got != want {
		t.Errorf("lookupAgent(claude) = %q, want the PATH hit %q", got, want)
	}
}

func TestLookupAgentIgnoresNonExecutables(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")

	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(filepath.Join(binDir, notAnAgent), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := lookupAgent(notAnAgent); got != "" {
		t.Errorf("a directory must not count as an agent CLI, got %q", got)
	}

	readable := filepath.Join(home, ".claude", "local")
	if err := os.MkdirAll(readable, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(readable, notAnAgent), []byte("notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := lookupAgent(notAnAgent); got != "" {
		t.Errorf("a non-executable file must not count as an agent CLI, got %q", got)
	}

	// Mode bits alone would accept this: some execute bit is set. This process
	// owns the file, so the owner bits are the ones that apply and it cannot
	// actually run it.
	otherOnly := filepath.Join(binDir, notAnAgent+"-other-exec")
	if err := os.WriteFile(otherOnly, []byte("#!/bin/sh\n"), 0o001); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root: every file is executable, so this case cannot be exercised")
	}
	if got := lookupAgent(notAnAgent + "-other-exec"); got != "" {
		t.Errorf("a file this process cannot execute must not count as an agent CLI, got %q", got)
	}
}

func TestLookupAgentReportsNothingWhenNothingIsInstalled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", "")
	if got := lookupAgent(notAnAgent); got != "" {
		t.Errorf("lookupAgent(%q) = %q, want \"\"", notAnAgent, got)
	}
}

// A GUI launch (macOS .app, Linux .desktop) hands Radar a PATH that misses the
// user's install, which is what "Radar doesn't detect Claude Code" looks like.
func TestDetectAgentsSeesClaudeWhenTheLaunchPATHMissesIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/usr/sbin:/sbin")
	writeExecutable(t, filepath.Join(home, ".local", "bin"), "claude")

	var claude *AgentInfo
	for _, a := range DetectAgents(context.Background(), false) {
		if a.Name == "claude" {
			info := a
			claude = &info
		}
	}
	if claude == nil {
		t.Fatal("Claude Code is installed but DetectAgents did not report it")
	}
	if !claude.Present || !claude.Supported {
		t.Errorf("claude present=%v supported=%v, want both true", claude.Present, claude.Supported)
	}
	if want := filepath.Join(home, ".local", "bin", "claude"); claude.Path != want {
		t.Errorf("claude path = %q, want %q", claude.Path, want)
	}
}

// The native installer puts a symlink at ~/.local/bin/claude pointing into
// ~/.local/share/claude/versions/, so the probe has to follow links.
func TestLookupAgentFollowsTheNativeInstallerSymlink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")

	real := writeExecutable(t, filepath.Join(home, ".local", "share", "claude", "versions"), "2.1.267")
	link := filepath.Join(home, ".local", "bin", "claude")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	if got := lookupAgent("claude"); got != link {
		t.Errorf("lookupAgent(claude) = %q, want %q", got, link)
	}
}
