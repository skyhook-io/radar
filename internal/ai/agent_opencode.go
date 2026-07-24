package ai

import (
	"bufio"
	"context"
	"io"
	"os/exec"
	"strings"
)

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

	args = append(args, "--prompt", prompt)

	cmd := exec.CommandContext(ctx, a.bin, args...)
	cmd.Env = scrubbedEnv()

	return cmd, func() {}, nil
}

func (a *opencodeAgent) parseStream(r io.Reader, onEvent func(StreamEvent)) Diagnosis {
	var sb strings.Builder
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		sb.WriteString(line + "\n")
		onEvent(StreamEvent{Type: "thinking", Token: line + "\n"})
	}
	return diagnosisFromText(sb.String())
}
