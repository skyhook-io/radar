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
)

// cursorAgent drives the Cursor CLI (`cursor-agent -p`). Containment differs from
// Claude/Codex and is weaker by necessity: Cursor has NO flag to suppress the
// user's global ~/.cursor/mcp.json, so the user's other MCP servers also load and
// --approve-mcps approves all of them. There is no hermetic read-only mode. What
// we DO contain:
//   - cluster WRITE access is gated by the read-only MCP MOUNT (radar passes
//     /mcp-readonly on investigation turns, full /mcp only on a confirmed apply) —
//     same server-side gate Claude/Codex rely on;
//   - --sandbox enabled blocks Cursor's own shell/file tools from writing;
//   - the workspace is a throwaway per-run temp dir, not the user's project.
//
// The residual exposure (the user's other MCP servers are reachable during a run)
// is disclosed in the consent UI — this is a BYO "your own setup" mode, not a
// hermetic one. Cursor's --resume is workspace-scoped, so every turn of a run must
// share one workspace dir (RunManager supplies a stable per-run dir via turnSpec).
type cursorAgent struct{ bin string }

func (a *cursorAgent) Name() string { return "cursor-agent" }

func (a *cursorAgent) SigninCmd() string { return "cursor-agent login" }

func (a *cursorAgent) command(ctx context.Context, s turnSpec) (*exec.Cmd, func(), error) {
	// Cursor has no system-prompt flag; the framing rides on the first turn's
	// prompt (the resumed session already carries it).
	prompt := s.prompt
	if s.systemPrompt != "" {
		prompt = s.systemPrompt + "\n\n" + prompt
	}

	workdir := s.workdir
	cleanup := func() {}
	if workdir == "" {
		// One-shot (non-RunManager) use: a fresh throwaway workspace, removed after.
		dir, err := os.MkdirTemp("", "radar-cursor-")
		if err != nil {
			return nil, nil, fmt.Errorf("ai: cursor workdir: %w", err)
		}
		workdir = dir
		cleanup = func() { _ = os.RemoveAll(dir) }
	}
	if err := writeCursorMCPConfig(workdir, s.mcpURL); err != nil {
		cleanup()
		return nil, nil, err
	}

	args := []string{
		"-p", "--output-format", "stream-json",
		"--workspace", workdir,
		"--sandbox", "enabled", // sandbox Cursor's own shell/file tools; MCP calls run server-side in radar
		"--approve-mcps", // auto-approve the radar server for this headless run
		"--trust",        // headless: trust the workspace without an interactive prompt
	}
	if s.model != "" {
		args = append(args, "--model", s.model) // free-form Cursor model slug; "" = the user's default
	}
	if s.sessionID != "" {
		args = append(args, "--resume", s.sessionID)
	}
	args = append(args, prompt)

	cmd := exec.CommandContext(ctx, a.bin, args...)
	cmd.Dir = workdir
	// Inherit the full environment: Cursor's auth lives under ~/.cursor (and
	// CURSOR_API_KEY) and it has no isolated mode, so a scrubbed env would break
	// login. This is the BYO "your own setup" trust posture, like Codex "my setup".
	return cmd, cleanup, nil
}

// writeCursorMCPConfig points Cursor at radar's MCP via the workspace-local config
// (<workdir>/.cursor/mcp.json). The endpoint is loopback-only in standalone mode,
// so no auth header.
func writeCursorMCPConfig(workdir, mcpURL string) error {
	dir := filepath.Join(workdir, ".cursor")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("ai: cursor mcp config dir: %w", err)
	}
	cfg := map[string]any{"mcpServers": map[string]any{"radar": map[string]any{"url": mcpURL}}}
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "mcp.json"), b, 0o600)
}

// Cursor stream-json event shapes (`cursor-agent -p --output-format stream-json`).
// Only the fields we consume. session_id rides on every event.
type cursorEvent struct {
	Type      string          `json:"type"`    // system|user|assistant|thinking|tool_call|result
	Subtype   string          `json:"subtype"` // init | delta | started | completed | success | ...
	SessionID string          `json:"session_id"`
	Text      string          `json:"text"`    // on thinking/delta
	Message   *cursorMessage  `json:"message"` // on assistant/user
	ToolCall  *cursorToolCall `json:"tool_call"`
	Result    string          `json:"result"`   // on result: full concatenated answer text
	IsError   bool            `json:"is_error"` // on result: the turn failed (don't treat result as a verdict)
}

type cursorMessage struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

// cursorToolCall carries one of several sub-objects keyed by tool kind
// (mcpToolCall, listMcpResourcesToolCall, shell, …). We only surface mcpToolCall —
// the cluster-touching calls. toolCallId correlates started↔completed.
type cursorToolCall struct {
	ToolCallID  string         `json:"toolCallId"`
	MCPToolCall *cursorMCPCall `json:"mcpToolCall"`
}

type cursorMCPCall struct {
	Args struct {
		ToolName string          `json:"toolName"`
		Args     json.RawMessage `json:"args"`
	} `json:"args"`
	Result *struct {
		Success *struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text struct {
					Text string `json:"text"`
				} `json:"text"`
			} `json:"content"`
		} `json:"success"`
	} `json:"result"`
}

func (a *cursorAgent) parseStream(r io.Reader, onEvent func(StreamEvent)) Diagnosis {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	var sessionID string
	var answer strings.Builder // streamed assistant text (fallback)
	var finalText string       // authoritative full answer from the result event

	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var e cursorEvent
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		if e.SessionID != "" {
			sessionID = e.SessionID
		}
		switch e.Type {
		case "thinking":
			if e.Subtype == "delta" && e.Text != "" {
				onEvent(StreamEvent{Type: "thinking", Token: e.Text})
			}
		case "assistant":
			if e.Message != nil {
				for _, c := range e.Message.Content {
					if c.Type == "text" {
						answer.WriteString(c.Text)
					}
				}
			}
		case "tool_call":
			cursorToolCallEvent(e, onEvent)
		case "result":
			// On a failed turn, don't promote the result string to the verdict — it's
			// an error message, not a diagnosis. Leaving finalText empty degrades to
			// the streamed assistant text (or an inconclusive verdict), and the
			// generic runner surfaces the failure via the CLI's nonzero exit.
			if e.Result != "" && !e.IsError {
				finalText = e.Result
			}
		}
	}

	text := finalText
	if text == "" {
		text = answer.String()
	}
	d := diagnosisFromText(text)
	d.SessionID = sessionID
	return d
}

// cursorToolCallEvent surfaces an MCP tool call as a running/done step. Non-MCP
// tool calls (resource discovery, shell) are not surfaced — they aren't part of
// the investigation transcript the user cares about.
func cursorToolCallEvent(e cursorEvent, onEvent func(StreamEvent)) {
	tc := e.ToolCall
	if tc == nil || tc.MCPToolCall == nil {
		return
	}
	m := tc.MCPToolCall
	switch e.Subtype {
	case "started":
		onEvent(StreamEvent{Type: "step", Step: &StepInfo{
			ID: tc.ToolCallID, Tool: m.Args.ToolName, Status: "running",
			Summary: cursorArgsText(m.Args.Args),
		}})
	case "completed":
		res, trunc := capPayload(cursorMCPResultText(m))
		onEvent(StreamEvent{Type: "step", Step: &StepInfo{
			ID: tc.ToolCallID, Tool: m.Args.ToolName, Status: "done",
			Result: res, Truncated: trunc,
		}})
	}
}

func cursorArgsText(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" || s == "{}" {
		return ""
	}
	out, _ := capPayload(s)
	return out
}

// cursorMCPResultText joins the text parts of a Cursor mcpToolCall result. Cursor
// nests the text one level deeper than Codex: content[].text.text. Capping happens
// at the call site so the truncated flag can be surfaced.
func cursorMCPResultText(m *cursorMCPCall) string {
	if m.Result == nil || m.Result.Success == nil {
		return ""
	}
	var b strings.Builder
	for _, c := range m.Result.Success.Content {
		b.WriteString(c.Text.Text)
	}
	return b.String()
}
