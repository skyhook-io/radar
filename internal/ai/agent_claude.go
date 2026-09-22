package ai

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/skyhook-io/radar/pkg/investigation"
)

// claudeAgent drives Claude Code. In safeguarded mode, --tools "" disables all
// built-in tools and --allowedTools restricts MCP usage to Radar. Full-local
// deliberately omits those constraints and merges Radar into the user's setup.
type claudeAgent struct{ bin string }

func (a *claudeAgent) Name() string { return "claude" }

func (a *claudeAgent) Path() string { return a.bin }

func (a *claudeAgent) SigninCmd() string { return "claude auth login" }

func (a *claudeAgent) command(ctx context.Context, s turnSpec) (*exec.Cmd, func(), error) {
	switch s.profile {
	case ExecutionProfileSafeguarded, ExecutionProfileFullLocal:
	default:
		return nil, nil, fmt.Errorf("ai: Claude does not support execution profile %q", s.profile)
	}
	cfgPath, cleanup, err := writeMCPConfig(s.mcpURL, s.mcpToken)
	if err != nil {
		return nil, nil, err
	}

	// The prompt goes over stdin, not as an argument: it carries the saved
	// story and every claim on an explanation turn, none of which is bounded,
	// and one argument is capped at 128 KiB on Linux.
	args := []string{
		"-p",
		"--mcp-config", cfgPath,
	}
	if s.profile == ExecutionProfileSafeguarded {
		args = append(args,
			"--strict-mcp-config",
			"--tools", "", // disable all built-in tools — cluster access is MCP-only
			"--allowedTools",
		)
		for _, t := range investigation.ReadOnlyTools {
			args = append(args, "mcp__radar__"+t)
		}
		if s.apply {
			for _, t := range investigation.WriteTools {
				args = append(args, "mcp__radar__"+t)
			}
		}
		args = append(args, "--permission-mode", "acceptEdits")
	}
	args = append(args,
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
	cmd.Stdin = strings.NewReader(s.prompt)
	if s.profile == ExecutionProfileSafeguarded {
		cmd.Env = scrubbedEnv()
	}
	// Run from the user's home dir so the session is stored under a stable,
	// predictable project path: Claude Code's `--resume <id>` is cwd-scoped, and
	// the "Open in Claude Code" hand-off resumes from a home-dir terminal.
	if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
	}
	return cmd, cleanup, nil
}

func (a *claudeAgent) parseStream(r io.Reader, onEvent func(StreamEvent)) Diagnosis {
	return parseStream(r, onEvent)
}
