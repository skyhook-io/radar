package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// cursorAgent drives the Cursor CLI (`cursor-agent -p`). Cursor has no hermetic
// read-only mode: it always loads the user's global ~/.cursor/mcp.json, and the
// --force grant required for headless MCP calls also approves its built-in tools
// and every loaded MCP server. --sandbox enabled is still requested, but Cursor's
// tools can write outside the supplied workspace, so neither it nor the
// throwaway workspace is treated as a security boundary. Radar's investigation
// MCP mount remains read-only; Cursor's other tools are outside Radar's control.
// This exposure is explicit in the versioned full-local consent surface.
//
// Cursor's --resume is workspace-scoped, so every turn of a run must share one
// workspace dir (RunManager supplies a stable per-run dir via turnSpec).
type cursorAgent struct {
	bin string

	approvalMu    sync.Mutex
	approvalKnown bool
	approvalArgs  []string // resolved approval flags, e.g. {"--force", "--trust"}
}

func (a *cursorAgent) Name() string { return "cursor-agent" }

func (a *cursorAgent) Path() string { return a.bin }

func (a *cursorAgent) SigninCmd() string { return "cursor-agent login" }

func (a *cursorAgent) command(ctx context.Context, s turnSpec) (*exec.Cmd, func(), error) {
	if s.profile != ExecutionProfileFullLocal {
		return nil, nil, fmt.Errorf("ai: Cursor Agent does not support execution profile %q", s.profile)
	}
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
		"--sandbox", "enabled", // request Cursor's sandbox; full-local consent does not treat it as a security boundary
		"--approve-mcps", // auto-approve the radar server for this headless run
	}
	// Headless (-p) runs get no TTY, so nothing can answer Cursor's approval
	// prompts: without an approval flag the run aborts at the workspace-trust gate,
	// and the per-run throwaway workdir means a one-time manual trust never carries
	// over. --force/--yolo is the load-bearing grant — it approves tool calls, and
	// observably clears the workspace gate as well (the force-only shim run pins
	// that). --trust clears only the gate: --approve-mcps registers the radar server
	// but does NOT approve its invocations, so a run granted trust alone has every
	// MCP call auto-denied and ends with no evidence. Trust is still passed when
	// advertised, since no help text promises force subsumes it; an unsupported flag
	// aborts the run, so the set comes from probing --help.
	//
	// --force also approves Cursor's built-in tools and every MCP server it loaded,
	// the user's own from ~/.cursor/mcp.json included. Cursor offers no way to
	// exclude those servers, and its sandbox is not a reliable workspace boundary.
	// The versioned full-local consent gate discloses that wider grant.
	approvalArgs, err := a.resolveApprovalFlags()
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	args = append(args, approvalArgs...)
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
	// CURSOR_API_KEY) and it has no safeguarded mode, so a scrubbed env would
	// break login. This is the full-local trust posture.
	return cmd, cleanup, nil
}

// resolveApprovalFlags returns every approval flag this cursor-agent supports,
// probing --help once and caching. Errors if the probe is inconclusive or the CLI
// offers no known flag.
func (a *cursorAgent) resolveApprovalFlags() ([]string, error) {
	a.approvalMu.Lock()
	defer a.approvalMu.Unlock()
	if a.approvalKnown {
		if !hasCursorForceGrant(a.approvalArgs) {
			return nil, errNoCursorApprovalFlag
		}
		return a.approvalArgs, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, a.bin, "--help").CombinedOutput()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("ai: Cursor Agent capability probe timed out")
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return nil, fmt.Errorf("ai: Cursor Agent capability probe returned no help output")
	}
	a.approvalArgs = cursorHelpApprovalFlags(string(out))
	a.approvalKnown = true
	log.Printf("[ai] cursor-agent approval flags=%v", a.approvalArgs)
	if !hasCursorForceGrant(a.approvalArgs) {
		return nil, errNoCursorApprovalFlag
	}
	return a.approvalArgs, nil
}

// hasCursorForceGrant reports whether the resolved set carries the tool-approval
// grant. --trust alone is not enough: it clears the workspace gate, so the run
// starts and looks healthy, then auto-denies every MCP call and ends with no
// evidence. Failing here costs nothing; spawning burns a full agent turn.
func hasCursorForceGrant(flags []string) bool {
	return slices.Contains(flags, "--force") || slices.Contains(flags, "--yolo")
}

var errNoCursorApprovalFlag = fmt.Errorf("ai: installed cursor-agent supports no tool-approval flag (--force/--yolo); upgrade cursor-agent")

var (
	cursorTrustFlag = regexp.MustCompile(`(?m)^[[:space:]]*(-[[:alnum:]],?[[:space:]]+)?--trust([[:space:],=]|$)`)
	cursorForceFlag = regexp.MustCompile(`(?m)^[[:space:]]*(-[[:alnum:]],?[[:space:]]+)?--force([[:space:],=]|$)`)
	cursorYoloFlag  = regexp.MustCompile(`(?m)^[[:space:]]*(-[[:alnum:]],?[[:space:]]+)?--yolo([[:space:],=]|$)`)
)

// cursorHelpApprovalFlags returns the supported approval flags, empty if none.
// --yolo is documented as an alias of --force, so only one of the pair is taken.
func cursorHelpApprovalFlags(help string) []string {
	var flags []string
	switch {
	case cursorForceFlag.MatchString(help):
		flags = append(flags, "--force")
	case cursorYoloFlag.MatchString(help):
		flags = append(flags, "--yolo")
	}
	if cursorTrustFlag.MatchString(help) {
		flags = append(flags, "--trust")
	}
	return flags
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
	Result *cursorMCPResult `json:"result"`
}

// cursorMCPResult mirrors the McpResult protobuf oneof emitted by Cursor
// 2026.08.25. The CLI JSON projection uses lowerCamelCase field names.
type cursorMCPResult struct {
	Success          *cursorMCPSuccess          `json:"success"`
	Error            *cursorMCPError            `json:"error"`
	Rejected         *cursorMCPRejected         `json:"rejected"`
	PermissionDenied *cursorMCPPermissionDenied `json:"permissionDenied"`
	ToolNotFound     *cursorMCPToolNotFound     `json:"toolNotFound"`
	ServerNotFound   *cursorMCPServerNotFound   `json:"serverNotFound"`
	Approved         *struct{}                  `json:"approved"`
}

type cursorMCPSuccess struct {
	IsError bool `json:"isError"`
	Content []struct {
		Text struct {
			Text string `json:"text"`
		} `json:"text"`
	} `json:"content"`
}

type cursorMCPError struct {
	Error string `json:"error"`
}

type cursorMCPRejected struct {
	Reason     string `json:"reason"`
	IsReadonly bool   `json:"isReadonly"`
}

type cursorMCPPermissionDenied struct {
	Error      string `json:"error"`
	IsReadonly bool   `json:"isReadonly"`
}

type cursorMCPToolNotFound struct {
	Name           string   `json:"name"`
	AvailableTools []string `json:"availableTools"`
}

type cursorMCPServerNotFound struct {
	Name             string   `json:"name"`
	AvailableServers []string `json:"availableServers"`
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
		resultText, evidenceRef := splitInvestigationEvidenceMarker(
			cursorMCPResultText(m),
		)
		res, trunc := capPayload(resultText)
		onEvent(StreamEvent{Type: "step", Step: &StepInfo{
			ID: tc.ToolCallID, Tool: m.Args.ToolName, Status: "done",
			Result: res, EvidenceRef: evidenceRef,
			IsError: cursorMCPResultErrorState(m.Result), Truncated: trunc,
			producerResult: &resultText,
		}})
	}
}

func cursorMCPResultErrorState(result *cursorMCPResult) *bool {
	if result == nil {
		return nil
	}
	confirmed := false
	switch {
	case result.Success != nil:
		confirmed = result.Success.IsError
	case result.Approved != nil:
		confirmed = false
	case result.Error != nil,
		result.Rejected != nil,
		result.PermissionDenied != nil,
		result.ToolNotFound != nil,
		result.ServerNotFound != nil:
		confirmed = true
	default:
		return nil
	}
	return &confirmed
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
	if m.Result == nil {
		return ""
	}
	result := m.Result
	switch {
	case result.Success != nil:
		var b strings.Builder
		for _, c := range result.Success.Content {
			b.WriteString(c.Text.Text)
		}
		if b.Len() == 0 && result.Success.IsError {
			return "Cursor reported an MCP tool error without details."
		}
		return b.String()
	case result.Error != nil:
		return cursorMCPFailureText("Cursor reported an MCP tool error", result.Error.Error)
	case result.Rejected != nil:
		return cursorMCPAccessFailureText("Cursor rejected the MCP tool call", result.Rejected.Reason, result.Rejected.IsReadonly)
	case result.PermissionDenied != nil:
		return cursorMCPAccessFailureText("Cursor denied permission for the MCP tool call", result.PermissionDenied.Error, result.PermissionDenied.IsReadonly)
	case result.ToolNotFound != nil:
		text := "Cursor could not find the requested MCP tool"
		if result.ToolNotFound.Name != "" {
			text = fmt.Sprintf("Cursor could not find MCP tool %q", result.ToolNotFound.Name)
		}
		if len(result.ToolNotFound.AvailableTools) > 0 {
			text += "; available tools: " + strings.Join(result.ToolNotFound.AvailableTools, ", ")
		}
		return text
	case result.ServerNotFound != nil:
		text := "Cursor could not find the requested MCP server"
		if result.ServerNotFound.Name != "" {
			text = fmt.Sprintf("Cursor could not find MCP server %q", result.ServerNotFound.Name)
		}
		if len(result.ServerNotFound.AvailableServers) > 0 {
			text += "; available servers: " + strings.Join(result.ServerNotFound.AvailableServers, ", ")
		}
		return text
	case result.Approved != nil:
		return "Cursor approved the MCP tool call."
	default:
		return ""
	}
}

func cursorMCPFailureText(summary, detail string) string {
	if detail == "" {
		return summary + " without details."
	}
	return summary + ": " + detail
}

func cursorMCPAccessFailureText(summary, detail string, isReadonly bool) string {
	text := cursorMCPFailureText(summary, detail)
	if isReadonly {
		text += " (read-only tool)"
	}
	return text
}
