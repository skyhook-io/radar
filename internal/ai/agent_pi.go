package ai

import (
	"bufio"
	"context"
	"io"
	"os/exec"
	"regexp"
	"strings"
)

var piSessionRe = regexp.MustCompile(`(?i)Session(?: ID)?:\s*([a-zA-Z0-9-]+)`)

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
	d := diagnosisFromText(sb.String())
	if m := piSessionRe.FindStringSubmatch(sb.String()); len(m) > 1 {
		d.SessionID = m[1]
	}
	return d
}
