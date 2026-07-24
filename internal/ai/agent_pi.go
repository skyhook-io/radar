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

var piSessionRe = regexp.MustCompile(`(?i)Session(?: ID)?:\s*([a-zA-Z0-9-]+)`)

// piAgent drives the Pi CLI.
type piAgent struct{ bin string }

func (a *piAgent) Name() string { return "pi" }

func (a *piAgent) SigninCmd() string { return "pi config" }

func (a *piAgent) command(ctx context.Context, s turnSpec) (*exec.Cmd, func(), error) {
	args := []string{"-p"} // Run in non-interactive print mode and exit

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

	args = append(args, prompt)

	workdir := s.workdir
	cleanup := func() {}
	if workdir == "" {
		dir, err := os.MkdirTemp("", "radar-pi-")
		if err != nil {
			return nil, nil, fmt.Errorf("ai: pi workdir: %w", err)
		}
		workdir = dir
		cleanup = func() { _ = os.RemoveAll(dir) }
	}

	if err := writePiConfig(workdir, s.mcpURL); err != nil {
		cleanup()
		return nil, nil, err
	}

	cmd := exec.CommandContext(ctx, a.bin, args...)
	cmd.Dir = workdir
	cmd.Env = scrubbedEnv()

	return cmd, cleanup, nil
}

func writePiConfig(workdir, mcpURL string) error {
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		return err
	}
	cfg := map[string]any{"mcpServers": map[string]any{"radar": map[string]any{"url": mcpURL}}}
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(workdir, "mcp.json"), b, 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(workdir, ".mcp.json"), b, 0o600); err != nil {
		return err
	}
	piDir := filepath.Join(workdir, ".pi")
	if err := os.MkdirAll(piDir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(piDir, "mcp.json"), b, 0o600)
}

func (a *piAgent) parseStream(r io.Reader, onEvent func(StreamEvent)) Diagnosis {
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
	if m := piSessionRe.FindStringSubmatch(sb.String()); len(m) > 1 {
		d.SessionID = m[1]
	}
	return d
}
