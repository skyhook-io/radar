package ai

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var opencodeSessionRe = regexp.MustCompile(`(?i)Session(?: ID)?:\s*([a-zA-Z0-9-]+)`)

// opencodeAgent drives the OpenCode CLI (`opencode`).
type opencodeAgent struct{ bin string }

func (a *opencodeAgent) Name() string { return "opencode" }

func (a *opencodeAgent) SigninCmd() string { return "opencode providers" }

func (a *opencodeAgent) command(ctx context.Context, s turnSpec) (*exec.Cmd, func(), error) {
	args := []string{"run"}

	if s.sessionID != "" {
		args = append(args, "--session", s.sessionID)
	} else if s.apply {
		args = append(args, "--continue")
	}

	if s.model != "" {
		args = append(args, "--model", s.model)
	}

	prompt := s.prompt
	if s.systemPrompt != "" {
		prompt = s.systemPrompt + "\n\n" + prompt
	}

	// OpenCode expects the prompt message as positional arguments, not --prompt
	args = append(args, prompt)

	workdir := s.workdir
	cleanup := func() {}
	if workdir == "" {
		dir, err := os.MkdirTemp("", "radar-opencode-")
		if err != nil {
			return nil, nil, fmt.Errorf("ai: opencode workdir: %w", err)
		}
		workdir = dir
		cleanup = func() { _ = os.RemoveAll(dir) }
	}

	if err := writeOpencodeConfig(workdir, s.mcpURL); err != nil {
		cleanup()
		return nil, nil, err
	}

	cmd := exec.CommandContext(ctx, a.bin, args...)
	cmd.Dir = workdir
	cmd.Env = scrubbedEnv()

	return cmd, cleanup, nil
}

func writeOpencodeConfig(workdir, mcpURL string) error {
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		return err
	}

	// Write local project opencode.json pointing to Radar's local MCP
	cfg := map[string]any{
		"mcp": map[string]any{
			"servers": map[string]any{
				"radar": map[string]any{
					"type": "remote",
					"url":  mcpURL,
				},
			},
		},
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(workdir, "opencode.json"), b, 0o600)
}

func (a *opencodeAgent) parseStream(r io.Reader, onEvent func(StreamEvent)) Diagnosis {
	var sb strings.Builder
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		sb.WriteString(line + "\n")
		onEvent(StreamEvent{Type: "thinking", Token: line + "\n"})
	}
	d := diagnosisFromText(sb.String())
	if m := opencodeSessionRe.FindStringSubmatch(sb.String()); len(m) > 1 {
		d.SessionID = m[1]
	}
	return d
}
