package ai

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCursorParseStream_FormatPin locks the Cursor `-p --output-format stream-json`
// JSONL schema we depend on, captured from a live MCP tool call: system/init carries
// the resumable session_id, thinking/delta is the reasoning channel, mcpToolCall
// items drive running/done steps (bare toolName, result nested at
// result.success.content[].text.text), and the result event carries the final report.
func TestCursorParseStream_FormatPin(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"sess-abc","model":"GPT-5.5"}`,
		`{"type":"user","message":{"content":[{"type":"text","text":"investigate"}]}}`,
		`{"type":"thinking","subtype":"delta","text":"checking "}`,
		`{"type":"thinking","subtype":"delta","text":"pods"}`,
		`{"type":"tool_call","subtype":"started","tool_call":{"toolCallId":"call_1","mcpToolCall":{"args":{"toolName":"get_resource","args":{"namespace":"dev"}}}}}`,
		`{"type":"tool_call","subtype":"completed","tool_call":{"toolCallId":"call_1","mcpToolCall":{"args":{"toolName":"get_resource","args":{"namespace":"dev"}},"result":{"success":{"isError":false,"content":[{"text":{"text":"crashloop detail"}}]}}}}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"bad tag."}]}}`,
		"{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"bad tag.\\n\\n```json\\n{\\\"root_cause\\\":\\\"bad tag\\\"}\\n```\"}",
	}, "\n")

	var running, done bool
	var thinking, doneResult, runningSummary string
	agent := &cursorAgent{bin: "cursor-agent"}
	diag := agent.parseStream(strings.NewReader(stream), func(ev StreamEvent) {
		switch ev.Type {
		case "thinking":
			thinking += ev.Token
		case "step":
			if ev.Step == nil {
				return
			}
			switch ev.Step.Status {
			case "running":
				running = true
				runningSummary = ev.Step.Summary
				if ev.Step.Tool != "get_resource" {
					t.Errorf("unexpected tool name: %q", ev.Step.Tool)
				}
			case "done":
				done = true
				doneResult = ev.Step.Result
			}
		}
	})

	if !running || !done {
		t.Errorf("expected running+done steps; running=%v done=%v", running, done)
	}
	if thinking != "checking pods" {
		t.Errorf("expected thinking deltas joined %q, got %q", "checking pods", thinking)
	}
	if runningSummary == "" {
		t.Errorf("expected args preview on running step")
	}
	if !strings.Contains(doneResult, "crashloop detail") {
		t.Errorf("expected nested tool result on done step, got %q", doneResult)
	}
	if diag.RootCause != "bad tag" {
		t.Errorf("root cause not parsed from result event: %q", diag.RootCause)
	}
	if diag.SessionID != "sess-abc" {
		t.Errorf("session id (system/init) not captured: %q", diag.SessionID)
	}
}

// TestCursorParseStream_ErrorResultNotVerdict ensures a failed turn (is_error:true)
// does not get its error string promoted to the diagnosis verdict — the run should
// surface failure (via exit code), not render an error message as a root cause.
func TestCursorParseStream_ErrorResultNotVerdict(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"sess-err"}`,
		"{\"type\":\"result\",\"subtype\":\"error\",\"is_error\":true,\"result\":\"```json\\n{\\\"root_cause\\\":\\\"should NOT surface\\\"}\\n```\"}",
	}, "\n")
	agent := &cursorAgent{bin: "cursor-agent"}
	diag := agent.parseStream(strings.NewReader(stream), func(ev StreamEvent) {})
	if diag.RootCause != "" {
		t.Errorf("error-result must not become a verdict; got rootCause=%q", diag.RootCause)
	}
	if diag.SessionID != "sess-err" {
		t.Errorf("session id should still be captured on a failed turn: %q", diag.SessionID)
	}
}

// TestCursorCommandFlags pins the headless containment flags and per-run workspace:
// stream-json output, sandboxed shell, MCP auto-approval, a workspace-local
// mcp.json pointed at radar, and --resume only on a continued session.
func TestCursorCommandFlags(t *testing.T) {
	a := &cursorAgent{bin: "cursor-agent", trustKnown: true, trust: true}
	dir := t.TempDir()
	const url = "http://localhost:9/mcp-readonly"

	cmd, cleanup, err := a.command(context.Background(), turnSpec{
		mcpURL: url, prompt: "go", workdir: dir, model: "sonnet-4.5",
		profile: ExecutionProfileFullLocal,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	args := strings.Join(cmd.Args, " ")
	for _, want := range []string{
		"-p", "--output-format stream-json", "--sandbox enabled",
		"--approve-mcps", "--trust", "--workspace " + dir, "--model sonnet-4.5",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("expected flag %q in args; got %q", want, args)
		}
	}
	if strings.Contains(args, "--resume") {
		t.Errorf("fresh session must not pass --resume; got %q", args)
	}
	if cmd.Dir != dir {
		t.Errorf("cmd.Dir = %q, want the per-run workdir %q", cmd.Dir, dir)
	}
	if cmd.Env != nil {
		t.Error("cursor must inherit the full env (auth lives under ~/.cursor); cmd.Env should be nil")
	}

	// The workspace-local MCP config must point Cursor at radar's mount.
	cfgPath := filepath.Join(dir, ".cursor", "mcp.json")
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("expected mcp.json at %s: %v", cfgPath, err)
	}
	var cfg struct {
		MCPServers map[string]struct {
			URL string `json:"url"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("mcp.json not valid JSON: %v", err)
	}
	if cfg.MCPServers["radar"].URL != url {
		t.Errorf("mcp.json radar url = %q, want %q", cfg.MCPServers["radar"].URL, url)
	}

	// A continued session passes --resume <id>.
	resumed, cleanup2, err := a.command(context.Background(), turnSpec{
		mcpURL: url, prompt: "more", workdir: dir, sessionID: "sess-xyz",
		profile: ExecutionProfileFullLocal,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup2()
	if !strings.Contains(strings.Join(resumed.Args, " "), "--resume sess-xyz") {
		t.Errorf("continued session must pass --resume sess-xyz; got %q", resumed.Args)
	}
}

func TestCursorCommandOmitsTrustWhenUnsupported(t *testing.T) {
	a := &cursorAgent{bin: "cursor-agent", trustKnown: true}
	cmd, cleanup, err := a.command(context.Background(), turnSpec{
		mcpURL: "http://localhost:9/mcp-readonly", prompt: "go", workdir: t.TempDir(),
		profile: ExecutionProfileFullLocal,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if strings.Contains(strings.Join(cmd.Args, " "), "--trust") {
		t.Errorf("unsupported Cursor must not receive --trust: %q", cmd.Args)
	}
}

func TestCursorCommandRejectsUnsupportedProfile(t *testing.T) {
	a := &cursorAgent{bin: "cursor-agent", trustKnown: true}
	if _, _, err := a.command(context.Background(), turnSpec{
		mcpURL: "http://localhost:9/mcp-readonly", prompt: "go",
		profile: ExecutionProfileSafeguarded,
	}); err == nil {
		t.Fatal("Cursor must reject safeguarded until the driver can enforce it")
	}
}

func TestCursorCommandRejectsInconclusiveTrustProbe(t *testing.T) {
	a := &cursorAgent{bin: filepath.Join(t.TempDir(), "missing-cursor-agent")}
	if _, _, err := a.command(context.Background(), turnSpec{
		mcpURL: "http://localhost:9/mcp-readonly", prompt: "go",
		workdir: t.TempDir(), profile: ExecutionProfileFullLocal,
	}); err == nil || !strings.Contains(err.Error(), "capability probe") {
		t.Fatalf("inconclusive probe = %v, want capability-probe error", err)
	}
}

func TestCursorHelpSupportsTrust(t *testing.T) {
	if !cursorHelpSupportsTrust("  --trust  Trust the current workspace without prompting") {
		t.Fatal("expected --trust to be detected from Cursor help")
	}
	for _, help := range []string{
		"  --force  Run everything",
		"  --trusted-domains  Trust listed domains",
		"  --no-trust  Disable workspace trust",
		"Removed: --trust is no longer supported",
	} {
		if cursorHelpSupportsTrust(help) {
			t.Fatalf("must not infer --trust from %q", help)
		}
	}
}

// TestResolveAgentCursor pins binary-name routing to the Cursor backend.
func TestResolveAgentCursor(t *testing.T) {
	cases := map[string]string{
		"cursor-agent":                     "cursor-agent",
		"/Users/x/.local/bin/cursor-agent": "cursor-agent",
		"codex":                            "codex",
		"claude":                           "claude",
	}
	for bin, want := range cases {
		if got := resolveAgent(bin).Name(); got != want {
			t.Errorf("resolveAgent(%q).Name() = %q, want %q", bin, got, want)
		}
	}
}
