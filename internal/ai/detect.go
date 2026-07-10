package ai

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// AgentInfo describes a detected local agent CLI for the OSS BYO-agent picker.
type AgentInfo struct {
	Name    string `json:"name"`    // "claude"
	Label   string `json:"label"`   // display name, e.g. "Claude Code"
	Path    string `json:"path"`    // absolute path from LookPath
	Version string `json:"version"` // best-effort `--version`, "" if it failed
	Present bool   `json:"present"`
	// Supported is true when Radar can actually DRIVE this CLI (we parse its
	// stream-json). Detected-but-unsupported CLIs are shown so the user knows
	// they exist, but can't be selected to run an investigation yet.
	Supported bool `json:"supported"`
}

// knownAgents are the CLI names we probe for — a FIXED list. We never exec a
// user-supplied name/path: only these literals, resolved through PATH, are run.
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

// ConsentSurfaceFor maps an agent to its consent-disclosure surface. Cursor's
// trust model is materially different (its global MCP servers can't be
// excluded), so it has its own; drift between enforcement sites would silently
// mis-gate consent, so both the server and the CLI call this.
func ConsentSurfaceFor(agent string) string {
	if agent == "cursor-agent" {
		return "cursor"
	}
	return "standard"
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

// DetectAgents probes for known agent CLIs on PATH. Safe by construction: only
// the fixed knownAgents names are resolved + run.
//
// withVersions controls whether each CLI's `--version` is executed. That exec is
// SLOW (~hundreds of ms per CLI, several seconds total) and is NOT needed to show
// the Diagnose button (which only needs to know an agent is present) — so it's
// opt-in (the settings/picker passes it; the button's hot path does not). Version
// probing never triggers auth/network side effects — just `--version`.
func DetectAgents(ctx context.Context, withVersions bool) []AgentInfo {
	var out []AgentInfo
	for _, name := range knownAgents {
		path, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		info := AgentInfo{
			Name:      name,
			Label:     agentLabels[name],
			Path:      path,
			Present:   true,
			Supported: isSupportedAgent(name),
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
