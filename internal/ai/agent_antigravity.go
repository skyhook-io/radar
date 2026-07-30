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

var antigravityConvRe = regexp.MustCompile(`(?i)Conversation ID:\s*([a-zA-Z0-9-]+)`)

// antigravityAgent drives the Antigravity CLI (`agy`).
type antigravityAgent struct{ bin string }

func (a *antigravityAgent) Name() string { return "antigravity" }

func (a *antigravityAgent) SigninCmd() string { return "agy auth login" }

func (a *antigravityAgent) command(ctx context.Context, s turnSpec) (*exec.Cmd, func(), error) {
	args := []string{"-p"}

	if s.sessionID != "" {
		args = append(args, "--conversation", s.sessionID)
	}

	if s.model != "" {
		args = append(args, "--model", s.model)
	}
	if s.effort != "" {
		args = append(args, "--effort", s.effort)
	}

	if s.apply {
		args = append(args, "--mode", "accept-edits", "--dangerously-skip-permissions")
	} else {
		args = append(args, "--sandbox")
	}

	prompt := s.prompt
	if s.systemPrompt != "" {
		prompt = s.systemPrompt + "\n\n" + prompt
	}

	args = append(args, prompt)

	workdir := s.workdir
	cleanup := func() {}
	if workdir == "" {
		dir, err := os.MkdirTemp("", "radar-antigravity-")
		if err != nil {
			return nil, nil, fmt.Errorf("ai: antigravity workdir: %w", err)
		}
		workdir = dir
		cleanup = func() { _ = os.RemoveAll(dir) }
	}

	if err := writeAntigravityConfig(workdir, s.mcpURL); err != nil {
		cleanup()
		return nil, nil, err
	}

	cmd := exec.CommandContext(ctx, a.bin, args...)
	cmd.Dir = workdir
	cmd.Env = scrubbedEnv()

	return cmd, cleanup, nil
}

func writeAntigravityConfig(workdir, mcpURL string) error {
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		return err
	}

	// Write standard mcp.json and .mcp.json
	cfg := map[string]any{"mcpServers": map[string]any{"radar": map[string]any{"url": mcpURL}}}
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}

	for _, name := range []string{"mcp.json", ".mcp.json"} {
		if err := os.WriteFile(filepath.Join(workdir, name), b, 0o600); err != nil {
			return err
		}
	}

	for _, sub := range []string{".gemini", ".cursor", ".antigravity", ".antigravitycli"} {
		dir := filepath.Join(workdir, sub)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "mcp.json"), b, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func (a *antigravityAgent) parseStream(r io.Reader, onEvent func(StreamEvent)) Diagnosis {
	var sb strings.Builder
	sc := bufio.NewScanner(r)
	buf := make([]byte, 64*1024)
	sc.Buffer(buf, 10*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		sb.WriteString(line + "\n")
		onEvent(StreamEvent{Type: "thinking", Token: line + "\n"})
	}
	d := diagnosisFromText(sb.String())
	if m := antigravityConvRe.FindStringSubmatch(sb.String()); len(m) > 1 {
		d.SessionID = m[1]
	}
	return d
}
