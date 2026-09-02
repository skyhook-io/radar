package ai

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// writeCursorShim writes a fake cursor-agent that mimics a Cursor release where
// --trust was removed: --help lists only -f/--force/--yolo, passing --trust aborts
// with "unknown option '--trust'", a headless run with no trust flag aborts at the
// workspace-trust gate, and --force/-f/--yolo let the run emit stream-json.
func writeCursorShim(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell shim not portable to windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "cursor-agent")
	const script = `#!/bin/sh
if [ "$1" = "--help" ]; then
  printf '%s\n' 'Options:' '  -f, --force  Force allow commands unless explicitly denied' '  --yolo  Alias for --force' '  --sandbox <mode>  sandbox' '  --approve-mcps  approve mcps'
  exit 0
fi
trusted=""
for a in "$@"; do
  case "$a" in
    --trust) echo "error: unknown option '--trust'" >&2; exit 1 ;;
    --force|-f|--yolo) trusted=1 ;;
  esac
done
if [ -z "$trusted" ]; then
  echo "cursor-agent stopped unexpectedly: Workspace Trust Required" >&2
  exit 1
fi
echo '{"type":"system","subtype":"init","session_id":"sess-shim"}'
echo '{"type":"result","subtype":"success","is_error":false,"result":"ok"}'
exit 0
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// Against a cursor-agent that advertises no --trust, command() must probe --help,
// pass --force alone, and the spawned run must clear the workspace-trust gate and
// produce stream-json — passing an unadvertised --trust aborts it with "unknown
// option", and passing no grant at all leaves it stuck at the gate.
func TestCursorForceGrantEndToEnd(t *testing.T) {
	a := &cursorAgent{bin: writeCursorShim(t)}
	cmd, cleanup, err := a.command(context.Background(), turnSpec{
		mcpURL: "http://localhost:9/mcp-readonly", prompt: "investigate",
		workdir: t.TempDir(), profile: ExecutionProfileFullLocal,
	})
	if err != nil {
		t.Fatalf("command(): %v", err)
	}
	defer cleanup()

	args := strings.Join(cmd.Args, " ")
	if strings.Contains(args, "--trust") || !strings.Contains(args, "--force") {
		t.Fatalf("expected --force fallback (no --trust); got %q", args)
	}
	out, runErr := cmd.CombinedOutput()
	if runErr != nil {
		t.Fatalf("run must succeed via --force; got %v\n%s", runErr, out)
	}
	if strings.Contains(string(out), "Workspace Trust Required") {
		t.Fatalf("run still hit the trust gate:\n%s", out)
	}
}

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
	var doneIsError *bool
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
				doneIsError = ev.Step.IsError
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
	if doneIsError == nil || *doneIsError {
		t.Errorf("Cursor success envelope should be confirmed success, got %v", doneIsError)
	}
	if diag.RootCause != "bad tag" {
		t.Errorf("root cause not parsed from result event: %q", diag.RootCause)
	}
	if diag.SessionID != "sess-abc" {
		t.Errorf("session id (system/init) not captured: %q", diag.SessionID)
	}
}

func TestCursorToolResultErrorState(t *testing.T) {
	tests := []struct {
		name       string
		resultJSON string
		wantError  bool
		wantText   []string
	}{
		{
			name:       "success",
			resultJSON: `{"success":{"isError":false,"content":[{"text":{"text":"resource detail"}}]}}`,
			wantText:   []string{"resource detail"},
		},
		{
			name:       "successful envelope carrying MCP error",
			resultJSON: `{"success":{"isError":true,"content":[{"text":{"text":"MCP returned an error"}}]}}`,
			wantError:  true,
			wantText:   []string{"MCP returned an error"},
		},
		{
			name:       "error",
			resultJSON: `{"error":{"error":"transport failed"}}`,
			wantError:  true,
			wantText:   []string{"transport failed"},
		},
		{
			name:       "rejected",
			resultJSON: `{"rejected":{"reason":"operator rejected it","isReadonly":true}}`,
			wantError:  true,
			wantText:   []string{"operator rejected it", "read-only tool"},
		},
		{
			name:       "permission denied",
			resultJSON: `{"permissionDenied":{"error":"policy denied access","isReadonly":true}}`,
			wantError:  true,
			wantText:   []string{"policy denied access", "read-only tool"},
		},
		{
			name:       "tool not found",
			resultJSON: `{"toolNotFound":{"name":"get_missing","availableTools":["get_resource","get_events"]}}`,
			wantError:  true,
			wantText:   []string{"get_missing", "get_resource", "get_events"},
		},
		{
			name:       "server not found",
			resultJSON: `{"serverNotFound":{"name":"missing-server","availableServers":["radar","grafana"]}}`,
			wantError:  true,
			wantText:   []string{"missing-server", "radar", "grafana"},
		},
		{
			name:       "approved",
			resultJSON: `{"approved":{}}`,
			wantText:   []string{"approved"},
		},
	}

	agent := &cursorAgent{bin: "cursor-agent"}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line := `{"type":"tool_call","subtype":"completed","tool_call":{"toolCallId":"result","mcpToolCall":{"args":{"toolName":"get_resource"},"result":` + tt.resultJSON + `}}}`
			var got *StepInfo
			agent.parseStream(strings.NewReader(line), func(ev StreamEvent) {
				if ev.Step != nil {
					got = ev.Step
				}
			})
			if got == nil {
				t.Fatal("completed MCP call did not emit a step")
			}
			if got.IsError == nil || *got.IsError != tt.wantError {
				t.Fatalf("IsError = %v, want confirmed %v", got.IsError, tt.wantError)
			}
			for _, want := range tt.wantText {
				if !strings.Contains(got.Result, want) {
					t.Errorf("result = %q, want producer payload %q", got.Result, want)
				}
			}
		})
	}

	unknown := `{"type":"tool_call","subtype":"completed","tool_call":{"toolCallId":"unknown","mcpToolCall":{"args":{"toolName":"get_resource"}}}}`
	var unknownState *bool
	agent.parseStream(strings.NewReader(unknown), func(ev StreamEvent) {
		if ev.Step != nil {
			unknownState = ev.Step.IsError
		}
	})
	if unknownState != nil {
		t.Errorf("missing result envelope = %v, want unknown", unknownState)
	}
}

// TestCursorParseStream_ErrorResultNotVerdict ensures a failed turn (is_error:true)
// does not get its error string promoted to the investigation conclusion — the run should
// surface failure (via exit code), not render an error message as a root cause.
func TestCursorParseStream_ErrorResultNotVerdict(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"sess-err"}`,
		"{\"type\":\"result\",\"subtype\":\"error\",\"is_error\":true,\"result\":\"```json\\n{\\\"root_cause\\\":\\\"should NOT surface\\\"}\\n```\"}",
	}, "\n")
	agent := &cursorAgent{bin: "cursor-agent"}
	diag := agent.parseStream(strings.NewReader(stream), func(ev StreamEvent) {})
	if diag.RootCause != "" {
		t.Errorf("error-result must not become a conclusion; got rootCause=%q", diag.RootCause)
	}
	if diag.SessionID != "sess-err" {
		t.Errorf("session id should still be captured on a failed turn: %q", diag.SessionID)
	}
}

// TestCursorCommandFlags pins the headless containment flags and per-run workspace:
// stream-json output, sandboxed shell, MCP auto-approval, a workspace-local
// mcp.json pointed at radar, and --resume only on a continued session.
func TestCursorCommandFlags(t *testing.T) {
	a := &cursorAgent{bin: "cursor-agent", approvalKnown: true, approvalArgs: []string{"--force", "--trust"}}
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
		"--approve-mcps", "--force", "--trust", "--workspace " + dir, "--model sonnet-4.5",
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

// Only the flags the installed CLI actually supports may be passed — an
// unsupported one aborts the run with "unknown option". A Cursor advertising the
// force grant alone gets exactly that one.
func TestCursorCommandPassesOnlySupportedApprovalFlags(t *testing.T) {
	for _, tc := range []struct {
		name     string
		resolved []string
		absent   string
	}{
		{"force only", []string{"--force"}, "--trust"},
		{"yolo only", []string{"--yolo"}, "--trust"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &cursorAgent{bin: "cursor-agent", approvalKnown: true, approvalArgs: tc.resolved}
			cmd, cleanup, err := a.command(context.Background(), turnSpec{
				mcpURL: "http://localhost:9/mcp-readonly", prompt: "go", workdir: t.TempDir(),
				profile: ExecutionProfileFullLocal,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			args := strings.Join(cmd.Args, " ")
			if strings.Contains(args, tc.absent) {
				t.Errorf("must not pass unsupported %s: %q", tc.absent, args)
			}
			if !strings.Contains(args, tc.resolved[0]) {
				t.Errorf("must pass supported %s: %q", tc.resolved[0], args)
			}
		})
	}
}

// A CLI advertising --trust but no force grant must be refused, not spawned: trust
// clears the workspace gate, so the run starts, auto-denies every MCP call and
// ends with no evidence — the failure this whole resolver exists to prevent.
// Guards against future flag churn renaming or relocating --force/--yolo.
func TestCursorCommandErrorsWhenTrustGrantIsAlone(t *testing.T) {
	for _, tc := range []struct {
		name     string
		resolved []string
	}{
		{"no approval flag at all", nil},
		{"trust only", []string{"--trust"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &cursorAgent{bin: "cursor-agent", approvalKnown: true, approvalArgs: tc.resolved}
			_, _, err := a.command(context.Background(), turnSpec{
				mcpURL: "http://localhost:9/mcp-readonly", prompt: "go", workdir: t.TempDir(),
				profile: ExecutionProfileFullLocal,
			})
			if err == nil || !strings.Contains(err.Error(), "tool-approval flag") {
				t.Fatalf("resolved %v = %v, want tool-approval-flag error", tc.resolved, err)
			}
		})
	}
}

func TestCursorCommandRejectsUnsupportedProfile(t *testing.T) {
	a := &cursorAgent{bin: "cursor-agent", approvalKnown: true, approvalArgs: []string{"--force"}}
	if _, _, err := a.command(context.Background(), turnSpec{
		mcpURL: "http://localhost:9/mcp-readonly", prompt: "go",
		profile: ExecutionProfileSafeguarded,
	}); err == nil {
		t.Fatal("Cursor must reject safeguarded until the driver can enforce it")
	}
}

func TestCursorCommandRejectsInconclusiveApprovalProbe(t *testing.T) {
	a := &cursorAgent{bin: filepath.Join(t.TempDir(), "missing-cursor-agent")}
	if _, _, err := a.command(context.Background(), turnSpec{
		mcpURL: "http://localhost:9/mcp-readonly", prompt: "go",
		workdir: t.TempDir(), profile: ExecutionProfileFullLocal,
	}); err == nil || !strings.Contains(err.Error(), "capability probe") {
		t.Fatalf("inconclusive probe = %v, want capability-probe error", err)
	}
}

func TestCursorHelpApprovalFlags(t *testing.T) {
	// The two grants are independent — trust clears the workspace gate, force
	// auto-approves MCP tool calls — so a CLI offering both gets both.
	both := "  -f, --force  Force allow commands unless explicitly denied\n  --yolo  Alias for --force\n  --trust  Trust the current workspace"
	if got := cursorHelpApprovalFlags(both); !slices.Equal(got, []string{"--force", "--trust"}) {
		t.Fatalf("expected both grants, got %v", got)
	}
	// Only one advertised => only that one; passing the other aborts the run.
	for help, want := range map[string][]string{
		"  -f, --force  Force allow commands unless explicitly denied": {"--force"},
		"  --yolo  Alias for --force (Run Everything)":                 {"--yolo"},
		"  --trust  Trust the current workspace without prompting":     {"--trust"},
	} {
		if got := cursorHelpApprovalFlags(help); !slices.Equal(got, want) {
			t.Fatalf("from %q: got %v, want %v", help, got, want)
		}
	}
	// Neither a real trust nor a force flag => empty (caller errors).
	for _, help := range []string{
		"  --trusted-domains  Trust listed domains",
		"  --no-trust  Disable workspace trust",
		"Removed: --trust is no longer supported",
		"  --enforce  something unrelated",
	} {
		if got := cursorHelpApprovalFlags(help); len(got) != 0 {
			t.Fatalf("must not infer an approval flag from %q, got %v", help, got)
		}
	}
	// The parser reports what is advertised; sufficiency is a separate question.
	// Only the force grant approves MCP tool calls, so trust alone is not enough.
	for _, tc := range []struct {
		flags []string
		want  bool
	}{
		{[]string{"--force", "--trust"}, true},
		{[]string{"--force"}, true},
		{[]string{"--yolo"}, true},
		{[]string{"--trust"}, false},
		{nil, false},
	} {
		if got := hasCursorForceGrant(tc.flags); got != tc.want {
			t.Errorf("hasCursorForceGrant(%v) = %v, want %v", tc.flags, got, tc.want)
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
