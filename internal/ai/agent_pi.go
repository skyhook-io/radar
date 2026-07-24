package ai

import (
	"bufio"
	"context"
	"io"
	"os/exec"
	"strings"
)

// piAgent drives the Pi CLI.
type piAgent struct{ bin string }

func (a *piAgent) Name() string { return "pi" }

func (a *piAgent) SigninCmd() string { return "pi auth" }

func (a *piAgent) command(ctx context.Context, s turnSpec) (*exec.Cmd, func(), error) {
	args := []string{}
	prompt := s.prompt
	if s.systemPrompt != "" {
		prompt = s.systemPrompt + "\n\n" + prompt
	}
	args = append(args, prompt)

	cmd := exec.CommandContext(ctx, a.bin, args...)
	cmd.Env = scrubbedEnv()

	return cmd, func() {}, nil
}

func (a *piAgent) parseStream(r io.Reader, onEvent func(StreamEvent)) Diagnosis {
	var sb strings.Builder
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		sb.WriteString(line + "\n")
		onEvent(StreamEvent{Type: "thinking", Token: line + "\n"})
	}
	return diagnosisFromText(sb.String())
}
