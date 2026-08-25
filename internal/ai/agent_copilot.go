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
	"strings"
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
//   - COPILOT_HOME points at a Radar-owned directory, so NOTHING of the user's own
//     Copilot setup loads: not their MCP servers ($COPILOT_HOME/mcp-config.json),
//     not their settings, and — decisively — not their user-level hooks, which run
//     shell in -p mode and which no flag can disable (the documented
//     disableAllHooks setting does not take effect from a workspace in 1.0.80);
//   - --disable-builtin-mcps drops github-mcp-server, which is built in rather
//     than user config and so survives the COPILOT_HOME redirect;
//   - cluster WRITE access is gated by the read-only MCP MOUNT, the same
//     server-side gate the other backends rely on.
//
// Both profiles pass --no-remote --no-remote-export: Copilot otherwise exports the
// session to GitHub web and mobile, and these transcripts carry cluster data.
type copilotAgent struct {
	bin string

	// homeDir resolves the user's home. Overridden in tests so a safeguarded run
	// never writes into the developer's real ~/.radar.
	homeDir func() (string, error)
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
		// Copilot self-updates by default and its CI-environment detection can't
		// fire under a minimized env: pin the binary for the duration of the run
		// rather than let it swap itself mid-investigation.
		"--no-auto-update",
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
	} else {
		// Full-local: the user's own tools, MCP servers and instructions are live.
		// Cluster writes stay gated by the read-only MCP mount.
		args = append(args, "--allow-all-tools")
	}

	if s.model != "" {
		args = append(args, "--model", s.model)
	}
	// Only when the user picked one. Copilot rejects --effort outright on accounts
	// whose model resolves to "auto" ("Model \"auto\" does not support reasoning
	// effort configuration", exit 1) — which is the free tier's default, so a Radar
	// default here would fail every out-of-the-box run.
	if s.effort != "" {
		args = append(args, "--effort", s.effort)
	}

	if s.sessionID != "" {
		// --resume takes an OPTIONAL value, so it must use the "=" form: passed as a
		// separate argument the id would be read as a positional prompt instead.
		args = append(args, "--resume="+s.sessionID)
	}

	cmd := exec.CommandContext(ctx, a.bin, args...)

	cleanup := func() {}
	if s.profile == ExecutionProfileSafeguarded {
		home, err := a.copilotHome()
		if err != nil {
			return nil, nil, err
		}
		// Empty working dir so the model can't pick up a workspace .mcp.json,
		// .github/hooks/*.json or AGENTS.md, and so nothing in radar's cwd is
		// reachable. The session store lives under COPILOT_HOME rather than the
		// cwd, so resume still works from anywhere — no per-run workdir is needed
		// (unlike Cursor).
		dir, err := os.MkdirTemp("", "radar-copilot-")
		if err != nil {
			return nil, nil, fmt.Errorf("ai: copilot workdir: %w", err)
		}
		cleanup = func() { _ = os.RemoveAll(dir) }
		cmd.Dir = dir
		cmd.Env = append(copilotEnv(), "COPILOT_HOME="+home)
	}
	// Full-local: inherit radar's cwd + full env so the user's auth/config work.

	return cmd, cleanup, nil
}

// CopilotHomeDir is the Radar-owned COPILOT_HOME used by safeguarded runs. It
// holds Copilot's config AND its session store, so it has to be a stable path
// rather than per-run scratch: follow-up turns and the terminal hand-off both
// resume sessions that live here, and the hand-off can come after a restart.
//
// The frontend reproduces this path as a shell expression when it builds the
// hand-off command (web/src/components/diagnose/launch.ts) — keep the two in step.
func CopilotHomeDir(home string) string {
	return filepath.Join(home, ".radar", "copilot-home")
}

// copilotHome resolves and prepares the Radar-owned COPILOT_HOME. Failing to
// create it is a hard error: the alternative is running against the user's own
// Copilot home, which is the very thing safeguarded mode exists to exclude.
//
// Auth is NOT stored here on the platforms we've checked (macOS keeps the
// `copilot login` credential in the keychain, and an explicit token var still
// takes precedence), so the redirect does not force a second login.
func (a *copilotAgent) copilotHome() (string, error) {
	homeFn := a.homeDir
	if homeFn == nil {
		homeFn = os.UserHomeDir
	}
	home, err := homeFn()
	if err != nil {
		return "", fmt.Errorf("ai: copilot home: %w", err)
	}
	dir := CopilotHomeDir(home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("ai: copilot home: %w", err)
	}
	seedCopilotConfig(dir)
	return dir, nil
}

// seedCopilotConfig writes Radar's own Copilot config on first use. The redirect
// already excludes the user's hooks; disableAllHooks makes it explicit for anything
// that lands in this directory later. Best-effort: a config Copilot can start
// without is not worth failing an investigation over.
func seedCopilotConfig(dir string) {
	path := filepath.Join(dir, "config.json")
	if _, err := os.Stat(path); err == nil {
		return
	}
	cfg, err := json.Marshal(map[string]any{
		"disableAllHooks": true,
		"autoUpdate":      false,
		"banner":          "never",
	})
	if err != nil {
		return
	}
	if err := os.WriteFile(path, cfg, 0o600); err != nil {
		log.Printf("[ai] could not seed Copilot config in %s: %v", dir, err)
	}
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

// copilotEnv is the minimal environment Copilot needs: auth (an explicit token var,
// or the OS credential store the CLI reaches on its own), the host overrides for
// GHE, proxy settings, and enough to exec. Cloud-provider secrets are deliberately
// omitted — the agent reaches the cluster only through Radar's MCP.
//
// The caller appends Radar's own COPILOT_HOME; the user's value is NOT kept, since
// that directory is exactly what a safeguarded run must not load.
func copilotEnv() []string {
	return minimalEnv(
		[]string{
			"TERM", "LANG", "USER", "LOGNAME", "SHELL",
			"COPILOT_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN",
			"GH_HOST", "COPILOT_GH_HOST",
			"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY",
			"http_proxy", "https_proxy", "no_proxy",
			"SSL_CERT_FILE", "SSL_CERT_DIR",
		},
		[]string{"LC_"},
	)
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

// copilotServerStatus is the per-server lifecycle event. It is the ONLY signal
// emitted when a server fails to attach (1.0.80 sends no mcp_servers_loaded in
// that case), so the MCP guard has to read both shapes.
type copilotServerStatus struct {
	ServerName string `json:"serverName"`
	Status     string `json:"status"` // pending | connected | failed
	Error      string `json:"error"`
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
	// noteMCPStatus folds one status report for Radar's server into that state.
	// The LAST report wins: a server can go pending → failed → connected across a
	// retry, and only where it ended up says whether the turn had cluster access.
	noteMCPStatus := func(status, errText string) {
		sawServers = true
		switch status {
		case "connected":
			mcpAttached, mcpErr = true, ""
		case "pending", "starting":
			// Not an outcome yet. A stream that ends here never attached, which the
			// !mcpAttached check below already treats as a failure.
		default:
			mcpAttached = false
			if mcpErr = strings.TrimSpace(errText); mcpErr == "" {
				mcpErr = "server status: " + status
			}
		}
	}

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
			// The list is authoritative, so an EMPTY one is itself the answer:
			// Radar's server was enumerated away (an org policy dropping
			// third-party MCP servers looks exactly like this).
			sawServers = true
			for _, srv := range d.Servers {
				if srv.Name == copilotMCPServer {
					noteMCPStatus(srv.Status, srv.Error)
				}
			}
		case "session.mcp_server_status_changed":
			var d copilotServerStatus
			if json.Unmarshal(e.Data, &d) != nil || d.ServerName != copilotMCPServer {
				continue
			}
			noteMCPStatus(d.Status, d.Error)
		case "session.warning":
			var d copilotWarning
			if json.Unmarshal(e.Data, &d) != nil || d.WarningType != "policy" {
				continue
			}
			// A policy that blocks third-party MCP can drop Radar's server without
			// any per-server event to report it, so this warning has to count as
			// evidence on its own. Narrowed to MCP-mentioning messages: other policy
			// warnings say nothing about cluster access and must not fail the turn.
			msg := strings.TrimSpace(d.Message)
			if strings.Contains(strings.ToLower(msg), "mcp") {
				sawServers = true
				if mcpErr == "" {
					mcpErr = msg
				}
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
