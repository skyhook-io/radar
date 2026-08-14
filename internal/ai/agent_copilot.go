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
	"sort"
	"strings"
	"sync"
	"time"
)

// copilotMCPServer is the name Radar's MCP server is registered under. Copilot
// namespaces MCP tools as "<server>-<tool>", so this prefix is load-bearing:
// it appears in --available-tools, in --allow-tool, and in the tool names the
// event stream reports back.
const copilotMCPServer = "radar"

// copilotAgent drives GitHub Copilot CLI (`copilot -p --output-format json`).
// Containment in safeguarded mode is the strongest of the four backends because
// Copilot can filter the model's entire tool surface:
//   - --available-tools leaves ONLY Radar's MCP tools visible — no shell, no file
//     edits, no web fetch (Copilot's own sandbox is experimental and unnecessary
//     once the tools are gone);
//   - --allow-tool <server> approves those tools without --allow-all-tools, so
//     nothing else is auto-approved;
//   - --disable-builtin-mcps drops github-mcp-server, and every server in the
//     user's own config is disabled by name (see resolveUserMCPServers) — Copilot
//     has no single "ignore user config" flag, so the set is enumerated;
//   - cluster WRITE access is gated by the read-only MCP MOUNT, the same
//     server-side gate the other backends rely on.
//
// Both profiles pass --no-remote --no-remote-export: Copilot otherwise exports the
// session to GitHub web and mobile, and these transcripts carry cluster data.
type copilotAgent struct {
	bin string

	serversMu    sync.Mutex
	serversKnown bool
	userServers  []string
}

func (a *copilotAgent) Name() string { return "copilot" }

func (a *copilotAgent) Path() string { return a.bin }

func (a *copilotAgent) SigninCmd() string { return "copilot login" }

func (a *copilotAgent) command(ctx context.Context, s turnSpec) (*exec.Cmd, func(), error) {
	switch s.profile {
	case ExecutionProfileSafeguarded, ExecutionProfileFullLocal:
	default:
		return nil, nil, fmt.Errorf("ai: GitHub Copilot does not support execution profile %q", s.profile)
	}
	// Copilot has no system-prompt flag; the framing rides on the first turn's
	// prompt (the resumed session already carries it).
	prompt := s.prompt
	if s.systemPrompt != "" {
		prompt = s.systemPrompt + "\n\n" + prompt
	}

	mcpCfg, err := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			copilotMCPServer: map[string]any{"type": "http", "url": s.mcpURL},
		},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("ai: copilot mcp config: %w", err)
	}

	args := []string{
		"-p", prompt,
		"--output-format", "json",
		"--additional-mcp-config", string(mcpCfg),
		// Headless runs have no TTY, so an ask_user call would block until the turn
		// times out. The agent can still ask in prose, which is what Radar wants.
		"--no-ask-user",
		// Reasoning summaries are off by default; without them the stream carries
		// only tool calls and the final message, and the UI shows no thinking.
		"--enable-reasoning-summaries",
		// Keep diagnostics out of the JSONL on stdout.
		"--log-level", "none",
		// The transcript contains cluster data — never publish it to GitHub's web
		// and mobile surfaces, in either profile.
		"--no-remote", "--no-remote-export",
	}

	if s.profile == ExecutionProfileSafeguarded {
		// The "=" form is used for the variadic options so a comma-joined list can
		// never be mistaken for several positional values.
		args = append(args,
			"--available-tools="+strings.Join(copilotToolNames(s.apply), ","),
			"--allow-tool="+copilotMCPServer,
			"--disable-builtin-mcps",
			"--no-custom-instructions",
		)
		servers, err := a.resolveUserMCPServers(ctx)
		if err != nil {
			return nil, nil, err
		}
		for _, name := range servers {
			args = append(args, "--disable-mcp-server", name)
		}
	} else {
		// Full-local: the user's own tools, MCP servers and instructions are live.
		// Cluster writes stay gated by the read-only MCP mount.
		args = append(args, "--allow-all-tools")
	}

	if s.model != "" {
		args = append(args, "--model", s.model)
	}
	// Default to medium rather than Copilot's bare default, matching the other
	// backends: it gives a solid investigation without an unbounded token bill.
	effort := s.effort
	if effort == "" {
		effort = "medium"
	}
	args = append(args, "--effort", effort)

	if s.sessionID != "" {
		// --resume takes an OPTIONAL value, so it must use the "=" form: passed as a
		// separate argument the id would be read as a positional prompt instead.
		args = append(args, "--resume="+s.sessionID)
	}

	cmd := exec.CommandContext(ctx, a.bin, args...)

	cleanup := func() {}
	if s.profile == ExecutionProfileSafeguarded {
		// Empty working dir so the model can't pick up a workspace .mcp.json or
		// AGENTS.md, and so nothing in radar's cwd is reachable. Copilot's session
		// store is global, so resume works from any directory — no per-run workdir
		// is needed (unlike Cursor).
		dir, err := os.MkdirTemp("", "radar-copilot-")
		if err != nil {
			return nil, nil, fmt.Errorf("ai: copilot workdir: %w", err)
		}
		cleanup = func() { _ = os.RemoveAll(dir) }
		cmd.Dir = dir
		cmd.Env = copilotEnv()
	}
	// Full-local: inherit radar's cwd + full env so the user's auth/config work.

	return cmd, cleanup, nil
}

// copilotToolNames is the allowlist handed to --available-tools: Radar's MCP tools
// and nothing else. Write tools are added only on a user-confirmed apply turn.
func copilotToolNames(apply bool) []string {
	names := make([]string, 0, len(radarReadTools)+len(radarWriteTools))
	for _, t := range radarReadTools {
		names = append(names, copilotMCPServer+"-"+t)
	}
	if apply {
		for _, t := range radarWriteTools {
			names = append(names, copilotMCPServer+"-"+t)
		}
	}
	return names
}

// copilotEnv is the minimal environment Copilot needs: auth (HOME/COPILOT_HOME
// hold the credential store, or an explicit token var), the host overrides for
// GHE, proxy settings, and enough to exec. Cloud-provider secrets are deliberately
// omitted — the agent reaches the cluster only through Radar's MCP.
//
// COPILOT_HOME is passed through but never set by Radar: auth AND the session
// store both live there, so redirecting it would break login and resume together.
func copilotEnv() []string {
	keep := map[string]bool{
		"HOME": true, "PATH": true, "TMPDIR": true,
		"TERM": true, "LANG": true, "USER": true, "LOGNAME": true, "SHELL": true,
		"COPILOT_HOME": true, "COPILOT_GITHUB_TOKEN": true,
		"GH_TOKEN": true, "GITHUB_TOKEN": true,
		"GH_HOST": true, "COPILOT_GH_HOST": true,
		"HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true,
		"http_proxy": true, "https_proxy": true, "no_proxy": true,
		"SSL_CERT_FILE": true, "SSL_CERT_DIR": true,
	}
	var out []string
	for _, kv := range os.Environ() {
		k, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if keep[k] || strings.HasPrefix(k, "LC_") {
			out = append(out, kv)
		}
	}
	return out
}

// resolveUserMCPServers lists the MCP servers Copilot would otherwise load and
// returns every one that isn't Radar's, so a safeguarded turn can disable them by
// name. Copilot has no --strict-mcp-config / --ignore-user-config equivalent, so
// the set has to be enumerated. Probed once and cached, like the Cursor trust flag.
//
// An unreadable or timed-out probe is an ERROR, not an empty list: silently
// treating "we couldn't tell" as "the user has no servers" would quietly downgrade
// safeguarded to a run with the user's other servers attached.
func (a *copilotAgent) resolveUserMCPServers(ctx context.Context) ([]string, error) {
	a.serversMu.Lock()
	defer a.serversMu.Unlock()
	if a.serversKnown {
		return a.userServers, nil
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, a.bin, "mcp", "list", "--json")
	// Probe from a neutral directory so a .mcp.json in radar's launch dir doesn't
	// show up in a list that's cached for the process lifetime. A safeguarded turn
	// runs in an empty temp dir anyway, where no workspace config applies.
	cmd.Dir = os.TempDir()
	out, err := cmd.Output()
	if cctx.Err() != nil {
		return nil, fmt.Errorf("ai: GitHub Copilot MCP-server probe timed out")
	}
	if err != nil {
		return nil, fmt.Errorf("ai: GitHub Copilot MCP-server probe failed: %w", err)
	}
	var parsed struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &parsed); err != nil {
		return nil, fmt.Errorf("ai: GitHub Copilot MCP-server probe returned unreadable output: %w", err)
	}
	names := make([]string, 0, len(parsed.MCPServers))
	for name := range parsed.MCPServers {
		if name == copilotMCPServer {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names) // stable args across turns
	a.userServers = names
	a.serversKnown = true
	log.Printf("[ai] copilot user MCP servers disabled in safeguarded runs: %v", names)
	return names, nil
}

// Copilot JSONL event shapes (`copilot -p --output-format json`). Only the fields
// we consume. Every event is wrapped in {type, data, …} EXCEPT the terminal
// "result" event, which carries sessionId/exitCode at the top level.
type copilotEvent struct {
	Type      string          `json:"type"`
	Data      json.RawMessage `json:"data"`
	SessionID string          `json:"sessionId"` // "result" only
}

type copilotToolStart struct {
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Arguments  json.RawMessage `json:"arguments"`
}

// copilotToolComplete carries NO tool name — it has to be correlated back to the
// matching tool.execution_start via toolCallId.
type copilotToolComplete struct {
	ToolCallID string `json:"toolCallId"`
	Success    bool   `json:"success"`
	Result     *struct {
		Content string `json:"content"`
	} `json:"result"`
}

// copilotMessage is emitted several times per turn. Intermediate ones carry tool
// requests with EMPTY content; only phase=="final_answer" holds the reply.
type copilotMessage struct {
	Content string `json:"content"`
	Phase   string `json:"phase"`
}

type copilotServersLoaded struct {
	Servers []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Error  string `json:"error"`
	} `json:"servers"`
}

type copilotWarning struct {
	Message     string `json:"message"`
	WarningType string `json:"warningType"`
}

func (a *copilotAgent) parseStream(r io.Reader, onEvent func(StreamEvent)) Diagnosis {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	var sessionID string
	var finalText string
	toolNames := map[string]string{} // toolCallId → toolName, from the start event

	// MCP attachment is tracked explicitly: a run whose MCP server never attached
	// still exits 0 with empty stderr, and the agent will happily produce a
	// confident verdict having read nothing from the cluster.
	sawServers := false
	mcpAttached := false
	mcpErr := ""

	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var e copilotEvent
		if json.Unmarshal(line, &e) != nil {
			continue
		}
		switch e.Type {
		case "assistant.reasoning_delta":
			var d struct {
				DeltaContent string `json:"deltaContent"`
			}
			if json.Unmarshal(e.Data, &d) == nil && d.DeltaContent != "" {
				onEvent(StreamEvent{Type: "thinking", Token: d.DeltaContent})
			}
		case "tool.execution_start":
			var d copilotToolStart
			if json.Unmarshal(e.Data, &d) != nil || d.ToolCallID == "" {
				continue
			}
			toolNames[d.ToolCallID] = d.ToolName
			onEvent(StreamEvent{Type: "step", Step: &StepInfo{
				ID: d.ToolCallID, Tool: copilotDisplayTool(d.ToolName), Status: "running",
				Summary: copilotArgsText(d.Arguments),
			}})
		case "tool.execution_complete":
			var d copilotToolComplete
			if json.Unmarshal(e.Data, &d) != nil || d.ToolCallID == "" {
				continue
			}
			res, trunc := capPayload(copilotResultText(d))
			onEvent(StreamEvent{Type: "step", Step: &StepInfo{
				ID: d.ToolCallID, Tool: copilotDisplayTool(toolNames[d.ToolCallID]), Status: "done",
				Result: res, Truncated: trunc,
			}})
		case "assistant.message":
			var d copilotMessage
			if json.Unmarshal(e.Data, &d) == nil && d.Phase == "final_answer" && d.Content != "" {
				finalText = d.Content
			}
		case "session.mcp_servers_loaded":
			var d copilotServersLoaded
			if json.Unmarshal(e.Data, &d) != nil {
				continue
			}
			sawServers = true
			for _, srv := range d.Servers {
				if srv.Name != copilotMCPServer {
					continue
				}
				if srv.Status == "connected" {
					mcpAttached = true
					break
				}
				mcpErr = strings.TrimSpace(srv.Error)
				if mcpErr == "" {
					mcpErr = "server status: " + srv.Status
				}
			}
		case "session.warning":
			var d copilotWarning
			if json.Unmarshal(e.Data, &d) == nil && d.WarningType == "policy" && mcpErr == "" {
				mcpErr = strings.TrimSpace(d.Message)
			}
		case "result":
			if e.SessionID != "" {
				sessionID = e.SessionID
			}
		}
	}

	d := diagnosisFromText(finalText)
	d.SessionID = sessionID
	// Only fail on affirmative evidence that Radar's server didn't attach. A CLI
	// that never emits the event at all is left alone rather than assumed broken.
	if sawServers && !mcpAttached {
		if mcpErr == "" {
			mcpErr = "Radar's MCP server was not loaded"
		}
		d.mcpErrText = mcpErr
	}
	return d
}

// copilotDisplayTool strips the "<server>-" namespace Copilot prefixes onto MCP
// tool names, so the transcript shows "get_events" like every other backend.
func copilotDisplayTool(name string) string {
	return strings.TrimPrefix(name, copilotMCPServer+"-")
}

func copilotArgsText(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" || s == "{}" {
		return ""
	}
	args, _ := capPayload(s)
	return args
}

// copilotResultText pulls the text out of a completed tool call. Copilot flattens
// MCP content into a single result.content string. Capping happens at the call
// site so the truncated flag can be surfaced.
func copilotResultText(d copilotToolComplete) string {
	if d.Result == nil {
		return ""
	}
	return d.Result.Content
}
