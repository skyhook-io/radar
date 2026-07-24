package ai

import (
	"bufio"
	"context"
	"io"
	"os/exec"
	"regexp"
	"strings"
)

var antigravityConvRe = regexp.MustCompile(`(?i)Conversation ID:\s*([a-zA-Z0-9-]+)`)

// antigravityAgent drives the Antigravity CLI (`agy`).
type antigravityAgent struct{ bin string }

func (a *antigravityAgent) Name() string { return "antigravity" }

func (a *antigravityAgent) SigninCmd() string { return "agy auth login" }

func (a *antigravityAgent) command(ctx context.Context, s turnSpec) (*exec.Cmd, func(), error) {
	args := []string{}

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
		args = append(args, "--mode", "plan")
	}

	args = append(args, "--sandbox")

	prompt := s.prompt
	if s.systemPrompt != "" {
		prompt = s.systemPrompt + "\n\n" + prompt
	}

	args = append(args, "--prompt", prompt)

	cmd := exec.CommandContext(ctx, a.bin, args...)
	cmd.Env = scrubbedEnv()

	return cmd, func() {}, nil
}

func (a *antigravityAgent) parseStream(r io.Reader, onEvent func(StreamEvent)) Diagnosis {
	var sb strings.Builder
	sc := bufio.NewScanner(r)
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
