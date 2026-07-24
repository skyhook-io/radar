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

	if s.systemPrompt != "" {
		args = append(args, "--system-prompt", s.systemPrompt)
	}

	args = append(args, s.prompt)

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
