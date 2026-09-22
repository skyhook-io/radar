package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/skyhook-io/radar/pkg/investigation"
)

// opencodeAgent drives the OpenCode CLI (`opencode`).
type opencodeAgent struct{ bin string }

func (a *opencodeAgent) Name() string { return "opencode" }

func (a *opencodeAgent) Path() string { return a.bin }

func (a *opencodeAgent) SigninCmd() string { return "opencode auth login" }

func (a *opencodeAgent) command(ctx context.Context, s turnSpec) (*exec.Cmd, func(), error) {
	if s.profile != ExecutionProfileFullLocal {
		return nil, nil, fmt.Errorf("ai: OpenCode does not support execution profile %q", s.profile)
	}
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
	// Positional prompts are shell-quoted again by OpenCode; stdin preserves the contract verbatim.
	cmd.Stdin = strings.NewReader(prompt)

	return cmd, cleanup, nil
}

func writeOpencodeConfig(workdir, mcpURL string) error {
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		return err
	}

	cfg := map[string]any{
		// Preserve any result Radar can retain, including UTF-8 and the evidence marker.
		"tool_output": map[string]int{
			"max_bytes": utf8.UTFMax*maxToolPayload + 1024,
			"max_lines": maxToolPayload + 1024,
		},
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

// Fields emitted by OpenCode 1.18.5's `run --format json` command.
type opencodeEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionID"`
	Part      *struct {
		ID    string   `json:"id"`
		Text  string   `json:"text"`
		Tool  string   `json:"tool"`
		Cost  *float64 `json:"cost"`
		State struct {
			Status   string          `json:"status"`
			Input    json.RawMessage `json:"input"`
			Output   string          `json:"output"`
			Error    string          `json:"error"`
			Metadata struct {
				Truncated bool `json:"truncated"`
			} `json:"metadata"`
		} `json:"state"`
	} `json:"part"`
	Error *struct {
		Name string `json:"name"`
		Data struct {
			Message string `json:"message"`
		} `json:"data"`
	} `json:"error"`
}

func (a *opencodeAgent) parseStream(r io.Reader, onEvent func(StreamEvent)) Diagnosis {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 10*1024*1024)
	var answer strings.Builder
	var sessionID, errorText string
	var cost *float64
	var turns int
	var cliErrored bool

	for sc.Scan() {
		var ev opencodeEvent
		if json.Unmarshal(bytes.TrimSpace(sc.Bytes()), &ev) != nil {
			continue
		}
		if ev.SessionID != "" {
			sessionID = ev.SessionID
		}
		if ev.Type == "error" {
			cliErrored = true
			if ev.Error != nil {
				errorText = ev.Error.Data.Message
				if errorText == "" {
					errorText = ev.Error.Name
				}
			}
			continue
		}
		if ev.Part == nil {
			continue
		}
		part := ev.Part
		switch ev.Type {
		case "text":
			answer.WriteString(part.Text + "\n")
			onEvent(StreamEvent{Type: "thinking", Token: part.Text + "\n"})
		case "tool_use":
			if part.State.Status != "completed" && part.State.Status != "error" {
				continue
			}
			isError := part.State.Status == "error"
			output := part.State.Output
			if isError {
				output = part.State.Error
			}
			resultText, evidenceRef := investigation.SplitRefMarker(output)
			if evidenceRef != "" {
				// OpenCode joins MCP text blocks with two newlines after the marker block.
				resultText = strings.TrimPrefix(resultText, "\n\n")
			}
			result, truncated := capPayload(resultText)
			// OpenCode emits tool_use only when the call has finished.
			onEvent(StreamEvent{Type: "step", Step: &StepInfo{
				ID: part.ID, Tool: strings.TrimPrefix(part.Tool, "radar_"), Status: "done",
				Summary: toolArgsText(part.State.Input), Result: result,
				EvidenceRef: evidenceRef, IsError: &isError,
				Truncated:      truncated || part.State.Metadata.Truncated,
				producerResult: &resultText,
			}})
		case "step_finish":
			turns++
			if part.Cost != nil {
				if cost == nil {
					cost = new(float64)
				}
				*cost += *part.Cost
			}
		}
	}
	if err := sc.Err(); err != nil {
		cliErrored = true
		errorText = fmt.Sprintf("reading OpenCode output: %v", err)
	}
	d := diagnosisFromText(answer.String())
	d.SessionID = sessionID
	d.CostUSD = cost
	d.Turns = turns
	d.cliErrored = cliErrored
	d.cliErrText = errorText
	return d
}
