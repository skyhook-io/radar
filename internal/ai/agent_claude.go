package ai

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strconv"
)

// claudeAgent drives Claude Code. Containment is via CLI flags: --tools "" disables
// ALL built-in tools (no Bash/WebFetch), and --allowedTools restricts MCP usage to
// radar's read tools (plus write tools on a confirmed apply turn). The read-only
// MCP mount enforces the same server-side, so the allowlist is defense-in-depth.
type claudeAgent struct{ bin string }

func (a *claudeAgent) Name() string { return "claude" }

func (a *claudeAgent) SigninCmd() string { return "claude auth login" }

func (a *claudeAgent) command(ctx context.Context, s turnSpec) (*exec.Cmd, func(), error) {
	cfgPath, cleanup, err := writeMCPConfig(s.mcpURL)
	if err != nil {
		return nil, nil, err
	}

	args := []string{
		"-p", s.prompt,
		"--mcp-config", cfgPath, "--strict-mcp-config",
		"--tools", "", // disable all built-in tools — cluster access is MCP-only
		"--allowedTools",
	}
	for _, t := range radarReadTools {
		args = append(args, "mcp__radar__"+t)
	}
	if s.apply {
		for _, t := range radarWriteTools {
			args = append(args, "mcp__radar__"+t)
		}
	}
	args = append(args,
		"--permission-mode", "acceptEdits",
		"--max-turns", strconv.Itoa(s.maxTurns),
		"--output-format", "stream-json", "--verbose",
	)
	if s.model != "" {
		args = append(args, "--model", s.model) // Claude has no separate effort knob
	}
	if s.sessionID != "" {
		args = append(args, "--resume", s.sessionID)
	} else {
		args = append(args, "--append-system-prompt", s.systemPrompt)
	}

	cmd := exec.CommandContext(ctx, a.bin, args...)
	cmd.Env = scrubbedEnv()
	// Run from the user's home dir so the session is stored under a stable,
	// predictable project path: Claude Code's `--resume <id>` is cwd-scoped, and
	// the "Open in Claude Code" hand-off resumes from a home-dir terminal. Claude's
	// built-in tools are disabled (--tools ""), so cwd doesn't widen its access.
	if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
	}
	return cmd, cleanup, nil
}

func (a *claudeAgent) parseStream(r io.Reader, onEvent func(StreamEvent)) Diagnosis {
	return parseStream(r, onEvent)
}
