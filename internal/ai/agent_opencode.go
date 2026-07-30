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

var opencodeSessionRe = regexp.MustCompile(`(?i)(?:session[_-]?id|session)\s*[:=]\s*"?([a-zA-Z0-9_-]+)"?`)

// opencodeAgent drives the OpenCode CLI (`opencode`).
type opencodeAgent struct{ bin string }

func (a *opencodeAgent) Name() string { return "opencode" }

func (a *opencodeAgent) SigninCmd() string { return "opencode providers" }

func (a *opencodeAgent) command(ctx context.Context, s turnSpec) (*exec.Cmd, func(), error) {
	args := []string{"run", "--format", "json", "--auto"}

	if s.sessionID != "" {
		args = append(args, "--session", s.sessionID)
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
			"radar": map[string]any{
				"type": "remote",
				"url":  mcpURL,
			},
		},
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(workdir, "opencode.json"), b, 0o600)
}

type opencodeEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionID"`
	Session   string `json:"session_id"`
	Part      *struct {
		Text string `json:"text"`
	} `json:"part"`
	Content string `json:"content"`
	Text    string `json:"text"`
}

func (a *opencodeAgent) parseStream(r io.Reader, onEvent func(StreamEvent)) Diagnosis {
	var sb strings.Builder
	sc := bufio.NewScanner(r)
	buf := make([]byte, 64*1024)
	sc.Buffer(buf, 10*1024*1024)
	var sessionID string

	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		if strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}") {
			var ev opencodeEvent
			if err := json.Unmarshal([]byte(trimmed), &ev); err == nil {
				if ev.SessionID != "" {
					sessionID = ev.SessionID
				} else if ev.Session != "" {
					sessionID = ev.Session
				}

				text := ev.Content
				if text == "" {
					text = ev.Text
				}
				if text == "" && ev.Part != nil {
					text = ev.Part.Text
				}
				if text != "" {
					sb.WriteString(text + "\n")
					onEvent(StreamEvent{Type: "thinking", Token: text + "\n"})
				}
				continue
			}
		}

		sb.WriteString(line + "\n")
		onEvent(StreamEvent{Type: "thinking", Token: line + "\n"})
	}

	d := diagnosisFromText(sb.String())
	if sessionID != "" {
		d.SessionID = sessionID
	} else if m := opencodeSessionRe.FindStringSubmatch(sb.String()); len(m) > 1 {
		d.SessionID = m[1]
	}
	return d
}
