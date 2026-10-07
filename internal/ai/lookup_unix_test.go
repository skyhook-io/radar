//go:build !windows

package ai

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
		name  string
		agent string
		dir   []string
	}{
		{"native installer", "claude", []string{".local", "bin"}},
		{"claude migrate-installer (alias-only, never on PATH)", "claude", []string{".claude", "local"}},
		{"opencode install script", "opencode", []string{".opencode", "bin"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("PATH", "")
			want := writeExecutable(t, filepath.Join(append([]string{home}, c.dir...)...), c.agent)

			if got := lookupAgent(c.agent); got != want {
				t.Errorf("lookupAgent(%s) = %q, want %q", c.agent, got, want)
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

// A CLI installed while Radar runs must become usable without a restart.
func TestAddDetectedPicksUpACLIInstalledLater(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	t.Setenv("RADAR_AI_CLI_BIN", "")

	d := newDiagnoser(nil, nil)
	for _, name := range d.AddDetected(context.Background()) {
		if name == "opencode" {
			t.Skip("opencode is installed in a fixed system directory on this machine")
		}
	}
	writeExecutable(t, filepath.Join(home, ".local", "bin"), "opencode")

	added := d.AddDetected(context.Background())
	if len(added) != 1 || added[0] != "opencode" {
		t.Fatalf("AddDetected = %v, want [opencode]", added)
	}
	if got := d.AgentName("opencode"); got != "opencode" {
		t.Errorf("AgentName(opencode) = %q, want the new backend", got)
	}
	if again := d.AddDetected(context.Background()); len(again) != 0 {
		t.Errorf("a second AddDetected re-added %v", again)
	}
}

// RADAR_AI_CLI_BIN pins the backend set; detection must not widen it.
func TestAddDetectedKeepsAnOverridePinned(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	pinned := writeExecutable(t, filepath.Join(home, "bin"), "claude")
	t.Setenv("RADAR_AI_CLI_BIN", pinned)
	writeExecutable(t, filepath.Join(home, ".local", "bin"), "codex")

	d, err := NewDetected(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if added := d.AddDetected(context.Background()); len(added) != 0 {
		t.Errorf("AddDetected = %v with RADAR_AI_CLI_BIN set, want nothing", added)
	}
}

// An npm-installed agent is a `#!/usr/bin/env node` script; Homebrew puts node
// beside it. Found off PATH, it only starts if its own directory joins PATH.
func TestAgentFoundOffPATHCanFindTheNodeBesideIt(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "node"), []byte("#!/bin/sh\necho started-by-node\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte("#!/usr/bin/env node\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// An empty directory, so a real node elsewhere can't stand in for this one.
	t.Setenv("PATH", t.TempDir())

	cmd := exec.Command(bin)
	addAgentDirToPath(cmd)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("agent did not start: %v", err)
	}
	if string(out) != "started-by-node\n" {
		t.Errorf("output = %q", out)
	}
}

func TestAddAgentDirToPathLeavesAnInheritedEnvAloneWhenAlreadyOnPATH(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", "/usr/bin:"+dir+"/")
	cmd := exec.Command(filepath.Join(dir, "cursor-agent"))
	addAgentDirToPath(cmd)
	if cmd.Env != nil {
		t.Errorf("cmd.Env = %v, want nil so the agent inherits Radar's environment", cmd.Env)
	}
}

func TestAddAgentDirToPathKeepsAScrubbedEnvScrubbed(t *testing.T) {
	cmd := exec.Command("/opt/agents/codex")
	cmd.Env = []string{"HOME=/h", "PATH=/usr/bin"}
	addAgentDirToPath(cmd)
	if want := []string{"HOME=/h", "PATH=/usr/bin:/opt/agents"}; !slices.Equal(cmd.Env, want) {
		t.Errorf("cmd.Env = %v, want %v", cmd.Env, want)
	}
}

// With no PATH a child searches the system default; that must survive.
func TestAddAgentDirToPathKeepsTheDefaultSearchPathWhenPATHIsUnset(t *testing.T) {
	cmd := exec.Command("/opt/agents/codex")
	cmd.Env = []string{"HOME=/h"}
	addAgentDirToPath(cmd)
	if want := []string{"HOME=/h", "PATH=/usr/bin:/bin:/opt/agents"}; !slices.Equal(cmd.Env, want) {
		t.Errorf("cmd.Env = %v, want %v", cmd.Env, want)
	}
}

// The Settings picker shows each agent's version; an npm agent found off PATH
// must report one too, not a blank.
func TestProbeVersionRunsAnAgentFoundOffPATH(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "node"), []byte("#!/bin/sh\necho 2.1.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte("#!/usr/bin/env node\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	if got := probeVersion(context.Background(), bin); got != "2.1.0" {
		t.Errorf("probeVersion = %q, want 2.1.0", got)
	}
}
