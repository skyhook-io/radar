package ai

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// AgentInfo describes a detected local agent CLI for the OSS BYO-agent picker.
type AgentInfo struct {
	Name    string `json:"name"`    // "claude"
	Label   string `json:"label"`   // display name, e.g. "Claude Code"
	Path    string `json:"path"`    // resolved executable (PATH, or a well-known install dir)
	Version string `json:"version"` // best-effort `--version`, "" if it failed
	Present bool   `json:"present"`
	// Supported is true when Radar can actually DRIVE this CLI (we parse its
	// stream-json). Detected-but-unsupported CLIs are shown so the user knows
	// they exist, but can't be selected to run an investigation yet.
	Supported       bool                        `json:"supported"`
	Profiles        []ExecutionProfile          `json:"profiles,omitempty"`
	ConsentSurfaces map[ExecutionProfile]string `json:"consentSurfaces,omitempty"`
	// Apply and Verification declare what the backend driving this agent
	// performs beyond a read-only investigation: a user-confirmed remediation
	// turn, and the automatic re-check after one. The frontend reads the
	// declaration for policy and the verdict for shape; a backend that omits
	// them is read-only.
	Apply        bool `json:"apply,omitempty"`
	Verification bool `json:"verification,omitempty"`
}

// knownAgents are the CLI names we probe for - a FIXED list. We never exec a
// user-supplied name/path: only these literals, resolved through PATH or the
// fixed directories in agentBinDirs, are run.
var knownAgents = []string{"claude", "codex", "gemini", "cursor-agent"}

var agentLabels = map[string]string{
	"claude": "Claude Code", "codex": "Codex", "gemini": "Gemini CLI", "cursor-agent": "Cursor Agent",
}

// AgentLabel is the display name for an agent CLI — the ONE table every
// surface (API, CLI header, consent prompts) reads, so labels can't drift.
func AgentLabel(name string) string {
	if l, ok := agentLabels[name]; ok {
		return l
	}
	return name
}

// EffectiveAgent resolves an agent pick exactly the way the server will at
// Start (Diagnoser.AgentName + defName): the pick when it names a supported
// agent, else the first supported one, else "". Pre-boot/remote clients must
// derive consent surfaces from THIS — an empty pick can resolve to Cursor.
func EffectiveAgent(pick string, agents []AgentInfo) string {
	def := ""
	for _, a := range agents {
		if !a.Supported {
			continue
		}
		if a.Name == pick {
			return pick
		}
		if def == "" {
			def = a.Name
		}
	}
	return def
}

// ProfilesFor returns the execution profiles Radar can honestly offer for an
// agent. Keep this policy beside detection so the API, consent gate, and UI all
// consume the same source of truth.
func ProfilesFor(agent string) []ExecutionProfile {
	switch agent {
	case "claude":
		return []ExecutionProfile{ExecutionProfileSafeguarded, ExecutionProfileFullLocal}
	case "codex":
		return []ExecutionProfile{ExecutionProfileSafeguarded, ExecutionProfileFullLocal}
	case "cursor-agent":
		return []ExecutionProfile{ExecutionProfileFullLocal}
	default:
		return nil
	}
}

func DefaultProfileFor(agent string) ExecutionProfile {
	profiles := ProfilesFor(agent)
	if len(profiles) == 0 {
		return ""
	}
	return profiles[0]
}

func SupportsProfile(agent string, profile ExecutionProfile) bool {
	for _, candidate := range ProfilesFor(agent) {
		if candidate == profile {
			return true
		}
	}
	return false
}

func ConsentSurfaceFor(agent string, profile ExecutionProfile) string {
	if !SupportsProfile(agent, profile) {
		return ""
	}
	return agent + ":" + string(profile)
}

func ConsentSurfacesFor(agent string) map[ExecutionProfile]string {
	profiles := ProfilesFor(agent)
	if len(profiles) == 0 {
		return nil
	}
	surfaces := make(map[ExecutionProfile]string, len(profiles))
	for _, profile := range profiles {
		surfaces[profile] = ConsentSurfaceFor(agent, profile)
	}
	return surfaces
}

func AllConsentSurfaces() []string {
	var surfaces []string
	for _, agent := range agentCLICandidates {
		for _, profile := range ProfilesFor(agent) {
			surfaces = append(surfaces, ConsentSurfaceFor(agent, profile))
		}
	}
	return surfaces
}

// supportedAgents are the CLIs we can drive today (have a stream-json parser).
func isSupportedAgent(name string) bool {
	for _, c := range agentCLICandidates {
		if c == name {
			return true
		}
	}
	return false
}

// lookupAgent resolves an agent CLI: PATH first, then a fixed set of well-known
// install directories.
//
// The fallback is what makes detection match what the user sees in their own
// terminal. Radar is often started where PATH is not the shell's PATH: a systemd
// unit, a Linux .desktop entry, a Windows shortcut. Claude Code's older installer
// is worse still, leaving the binary in ~/.claude/local behind a shell alias that
// no PATH can reach at all. Without this, Radar tells a user who has the CLI to go
// install it. (The desktop app is not in that list on purpose — cmd/desktop/env.go
// already replaces its PATH with the login shell's before the server boots.)
func lookupAgent(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, dir := range agentBinDirs() {
		for _, candidate := range executableNames(name) {
			// LookPath on an absolute path is the permission check too: it asks
			// whether THIS process can execute the file, which the mode bits
			// alone don't answer.
			if p, err := exec.LookPath(filepath.Join(dir, candidate)); err == nil {
				return p
			}
		}
	}
	return ""
}

// DetectAgents probes for known agent CLIs on PATH and in the well-known install
// directories. Safe by construction: only the fixed knownAgents names, resolved
// only from PATH or the fixed agentBinDirs, are run.
//
// withVersions controls whether each CLI's `--version` is executed. That exec is
// SLOW (~hundreds of ms per CLI, several seconds total) and is NOT needed to show
// the Diagnose button (which only needs to know an agent is present) — so it's
// opt-in (the settings/picker passes it; the button's hot path does not). Version
// probing never triggers auth/network side effects — just `--version`.
func DetectAgents(ctx context.Context, withVersions bool) []AgentInfo {
	var out []AgentInfo
	for _, name := range knownAgents {
		path := lookupAgent(name)
		if path == "" {
			continue
		}
		info := AgentInfo{
			Name:            name,
			Label:           agentLabels[name],
			Path:            path,
			Present:         true,
			Supported:       isSupportedAgent(name),
			Profiles:        ProfilesFor(name),
			ConsentSurfaces: ConsentSurfacesFor(name),
		}
		if withVersions {
			info.Version = probeVersion(ctx, path)
		}
		out = append(out, info)
	}
	return out
}

// probeVersion runs `<path> --version` with a hard 3s timeout. Best-effort: a
// hang or error yields "" rather than blocking the request.
func probeVersion(ctx context.Context, path string) string {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, path, "--version").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
