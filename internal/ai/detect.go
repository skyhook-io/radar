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
	Supported       bool                        `json:"supported"`
	Profiles        []ExecutionProfile          `json:"profiles,omitempty"`
	ConsentSurfaces map[ExecutionProfile]string `json:"consentSurfaces,omitempty"`
}

// knownAgents are the CLI names we probe for — a FIXED list. We never exec a
// user-supplied name/path: only these literals, resolved through PATH, are run.
// Note "copilot" is GitHub's standalone Copilot CLI, not the older `gh copilot`
// gh-extension — that one has no headless agent mode and never lands on PATH
// under this name.
var knownAgents = []string{"claude", "codex", "gemini", "cursor-agent", "copilot"}

var agentLabels = map[string]string{
	"claude": "Claude Code", "codex": "Codex", "gemini": "Gemini CLI",
	"cursor-agent": "Cursor Agent", "copilot": "GitHub Copilot CLI",
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
	case "copilot":
		return []ExecutionProfile{ExecutionProfileSafeguarded, ExecutionProfileFullLocal}
	default:
		return nil
	}
}

// ReasoningEfforts returns the reasoning-effort levels an agent accepts. Kept
// beside ProfilesFor for the same reason: the API validator, the UI menu, and the
// arg builder must not drift. The levels are CLI vocabulary, not Radar's — Codex
// and Copilot name them differently and Copilot's range is wider. An agent with no
// effort knob returns nil, so only the empty (default) value validates.
func ReasoningEfforts(agent string) []string {
	switch agent {
	case "codex":
		return []string{"minimal", "low", "medium", "high"}
	case "copilot":
		return []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}
	default:
		return nil
	}
}

// SupportsEffort reports whether an effort value may be passed to this agent. The
// empty string (the CLI's own default) is always allowed; anything else must be a
// listed literal, because the value is interpolated into CLI arguments.
func SupportsEffort(agent, effort string) bool {
	if effort == "" {
		return true
	}
	for _, candidate := range ReasoningEfforts(agent) {
		if candidate == effort {
			return true
		}
	}
	return false
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
