package ai

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testCopilotAgent points the backend at a throwaway home so a test never creates
// (or reads) the developer's real ~/.radar/copilot-home.
func testCopilotAgent(home string) *copilotAgent {
	return &copilotAgent{
		bin:     "copilot",
		homeDir: func() (string, error) { return home, nil },
	}
}

func envHas(env []string, kv string) bool {
	for _, e := range env {
		if e == kv {
			return true
		}
	}
	return false
}

// TestCopilotParseStream_FormatPin locks the `copilot -p --output-format json`
// JSONL schema, captured from a live run against an MCP server. Three properties
// here are easy to get wrong and silently break the panel:
//   - tool.execution_complete carries NO toolName, so the name must be correlated
//     from the matching tool.execution_start via toolCallId;
//   - intermediate assistant.message events have empty content and a populated
//     toolRequests — only phase=="final_answer" holds the verdict;
//   - the terminal "result" event is FLAT (sessionId at the top level), unlike
//     every other event, which nests under "data".
func TestCopilotParseStream_FormatPin(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"session.mcp_servers_loaded","data":{"servers":[{"name":"radar","status":"connected","transport":"http"}]},"ephemeral":true}`,
		`{"type":"assistant.reasoning_delta","data":{"reasoningId":"r0","deltaContent":"checking "},"ephemeral":true}`,
		`{"type":"assistant.reasoning_delta","data":{"reasoningId":"r0","deltaContent":"pods"},"ephemeral":true}`,
		`{"type":"tool.execution_start","data":{"toolCallId":"call_1","toolName":"radar-diagnose","arguments":{"name":"x"},"turnId":"0"}}`,
		`{"type":"assistant.message","data":{"messageId":"m0","content":"","toolRequests":[{"toolCallId":"call_1","name":"radar-diagnose"}]}}`,
		`{"type":"tool.execution_complete","data":{"toolCallId":"call_1","success":true,"result":{"content":"crashloop detail","contents":[{"type":"text","text":"crashloop detail"}]}}}`,
		"{\"type\":\"assistant.message\",\"data\":{\"messageId\":\"m1\",\"phase\":\"final_answer\",\"content\":\"bad tag.\\n\\n```json\\n{\\\"root_cause\\\":\\\"bad tag\\\"}\\n```\",\"toolRequests\":[]}}",
		`{"type":"assistant.turn_end","data":{"turnId":"0"}}`,
		`{"type":"result","timestamp":"2026-08-14T13:27:37.120Z","sessionId":"b95e250f-7264-4319-8f89-bdd6c850f9f1","exitCode":0}`,
	}, "\n")

	var running, done bool
	var thinking, doneResult, runningSummary, runningTool, doneTool string
	agent := &copilotAgent{bin: "copilot"}
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
				runningTool = ev.Step.Tool
				runningSummary = ev.Step.Summary
			case "done":
				done = true
				doneTool = ev.Step.Tool
				doneResult = ev.Step.Result
			}
		}
	})

	if !running || !done {
		t.Errorf("expected running+done steps; running=%v done=%v", running, done)
	}
	// The "radar-" server namespace is stripped so the transcript reads the same as
	// every other backend's.
	if runningTool != "diagnose" {
		t.Errorf("running step tool = %q, want %q", runningTool, "diagnose")
	}
	if doneTool != "diagnose" {
		t.Errorf("done step tool = %q, want %q (name must be correlated by toolCallId)", doneTool, "diagnose")
	}
	if thinking != "checking pods" {
		t.Errorf("expected reasoning deltas→thinking %q, got %q", "checking pods", thinking)
	}
	if runningSummary == "" {
		t.Error("expected args preview on running step")
	}
	if !strings.Contains(doneResult, "crashloop detail") {
		t.Errorf("expected tool result preview on done step, got %q", doneResult)
	}
	if diag.RootCause != "bad tag" {
		t.Errorf("root cause not parsed from the final_answer message: %q", diag.RootCause)
	}
	if diag.SessionID != "b95e250f-7264-4319-8f89-bdd6c850f9f1" {
		t.Errorf("session id not captured from the flat result event: %q", diag.SessionID)
	}
	if diag.mcpErrText != "" {
		t.Errorf("connected MCP server must not be reported as an error: %q", diag.mcpErrText)
	}
}

// TestCopilotParseStream_MCPNotAttached is the load-bearing safety test. When
// Radar's MCP server doesn't attach, Copilot still exits 0 with empty stderr and
// the model still answers — having read nothing from the cluster. Both captured
// causes must mark the turn failed even though a well-formed verdict parsed.
func TestCopilotParseStream_MCPNotAttached(t *testing.T) {
	verdict := "{\"type\":\"assistant.message\",\"data\":{\"phase\":\"final_answer\",\"content\":\"looks fine.\\n\\n```json\\n{\\\"healthy\\\":true}\\n```\"}}"
	result := `{"type":"result","sessionId":"s1","exitCode":0}`

	cases := []struct {
		name   string
		stream []string
		want   string
	}{
		{
			// Org policy: the warning fires and Radar's server is dropped silently.
			name: "org policy disables third-party MCP",
			stream: []string{
				`{"type":"session.warning","data":{"message":"Third-party MCP servers are disabled by your organization's Copilot policy. Only built-in servers are available.","warningType":"policy"},"ephemeral":true}`,
				`{"type":"session.mcp_servers_loaded","data":{"servers":[]},"ephemeral":true}`,
				verdict, result,
			},
			want: "organization's Copilot policy",
		},
		{
			// The server was configured but never connected.
			name: "server failed to connect",
			stream: []string{
				`{"type":"session.mcp_servers_loaded","data":{"servers":[{"name":"radar","status":"failed","transport":"http","error":"failed to initialize MCP client: connection refused"}]},"ephemeral":true}`,
				verdict, result,
			},
			want: "connection refused",
		},
		{
			// Captured from copilot 1.0.80: an unreachable server produces NO
			// mcp_servers_loaded event at all, only this per-server lifecycle one.
			// Reading just the "loaded" shape let this exact run through.
			name: "per-server status event only",
			stream: []string{
				`{"type":"session.mcp_server_status_changed","data":{"serverName":"radar","status":"pending"},"ephemeral":true}`,
				`{"type":"session.mcp_server_status_changed","data":{"serverName":"radar","status":"failed","error":"failed to initialize MCP client: Send message error Transport error: Client error: error sending request for url (http://127.0.0.1:9280/mcp-readonly), when send initialize request"},"ephemeral":true}`,
				verdict, result,
			},
			want: "when send initialize request",
		},
		{
			// A server left pending when the stream ends never attached either.
			name: "stuck pending",
			stream: []string{
				`{"type":"session.mcp_server_status_changed","data":{"serverName":"radar","status":"pending"},"ephemeral":true}`,
				verdict, result,
			},
			want: "not loaded",
		},
	}

	agent := &copilotAgent{bin: "copilot"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diag := agent.parseStream(strings.NewReader(strings.Join(tc.stream, "\n")), func(StreamEvent) {})
			// The verdict parsing itself still succeeds — that's exactly why the
			// separate signal is needed rather than relying on structured().
			if !diag.Healthy {
				t.Fatal("precondition: the toolless verdict should still parse, or this test proves nothing")
			}
			if diag.mcpErrText == "" {
				t.Fatal("a run whose MCP server never attached must be flagged, not returned as a verdict")
			}
			if !strings.Contains(diag.mcpErrText, tc.want) {
				t.Errorf("mcpErrText = %q, want it to mention %q", diag.mcpErrText, tc.want)
			}
		})
	}
}

// TestCopilotParseStream_NoServerEventIsNotAnError pins the conservative side of
// the guard: a CLI that never reports its MCP servers at all must not be treated
// as broken, or a future/older Copilot build would fail every investigation.
func TestCopilotParseStream_NoServerEventIsNotAnError(t *testing.T) {
	stream := strings.Join([]string{
		"{\"type\":\"assistant.message\",\"data\":{\"phase\":\"final_answer\",\"content\":\"```json\\n{\\\"root_cause\\\":\\\"x\\\"}\\n```\"}}",
		`{"type":"result","sessionId":"s1","exitCode":0}`,
	}, "\n")
	agent := &copilotAgent{bin: "copilot"}
	diag := agent.parseStream(strings.NewReader(stream), func(StreamEvent) {})
	if diag.mcpErrText != "" {
		t.Errorf("absent mcp_servers_loaded must not be inferred as a failure; got %q", diag.mcpErrText)
	}
	if diag.RootCause != "x" {
		t.Errorf("verdict should still parse, got %q", diag.RootCause)
	}
}

// TestCopilotParseStream_MCPRecovers pins that a retry which ends connected is a
// healthy run: the server's LAST reported state is what decides, not its first.
func TestCopilotParseStream_MCPRecovers(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"session.mcp_server_status_changed","data":{"serverName":"radar","status":"pending"},"ephemeral":true}`,
		`{"type":"session.mcp_server_status_changed","data":{"serverName":"radar","status":"failed","error":"transient"},"ephemeral":true}`,
		`{"type":"session.mcp_server_status_changed","data":{"serverName":"radar","status":"connected"},"ephemeral":true}`,
		"{\"type\":\"assistant.message\",\"data\":{\"phase\":\"final_answer\",\"content\":\"```json\\n{\\\"root_cause\\\":\\\"x\\\"}\\n```\"}}",
		`{"type":"result","sessionId":"s1","exitCode":0}`,
	}, "\n")
	diag := (&copilotAgent{bin: "copilot"}).parseStream(strings.NewReader(stream), func(StreamEvent) {})
	if diag.mcpErrText != "" {
		t.Errorf("a server that ended up connected must not fail the turn; got %q", diag.mcpErrText)
	}
}

// TestCopilotParseStream_OtherServerIgnored pins that another server's failure is
// not Radar's: in full-local the user's own servers are attached too, and one of
// them being down says nothing about our cluster access.
func TestCopilotParseStream_OtherServerIgnored(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"session.mcp_server_status_changed","data":{"serverName":"radar","status":"connected"},"ephemeral":true}`,
		`{"type":"session.mcp_server_status_changed","data":{"serverName":"github","status":"failed","error":"nope"},"ephemeral":true}`,
		"{\"type\":\"assistant.message\",\"data\":{\"phase\":\"final_answer\",\"content\":\"```json\\n{\\\"root_cause\\\":\\\"x\\\"}\\n```\"}}",
		`{"type":"result","sessionId":"s1","exitCode":0}`,
	}, "\n")
	diag := (&copilotAgent{bin: "copilot"}).parseStream(strings.NewReader(stream), func(StreamEvent) {})
	if diag.mcpErrText != "" {
		t.Errorf("another server's failure must not fail the turn; got %q", diag.mcpErrText)
	}
}

// TestCopilotExecutionProfiles pins the security-relevant differences between the
// two profiles, plus the flags that must appear in BOTH.
func TestCopilotExecutionProfiles(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	newAgent := func() *copilotAgent { return testCopilotAgent(home) }

	iso, cleanup, err := newAgent().command(ctx, turnSpec{
		mcpURL: "http://localhost:1/mcp-readonly", prompt: "go", profile: ExecutionProfileSafeguarded,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	args := strings.Join(iso.Args, " ")
	if !strings.Contains(args, "--available-tools=radar-") {
		t.Errorf("safeguarded mode must restrict the model to Radar's tools; got %q", args)
	}
	if strings.Contains(args, "radar-apply_resource") {
		t.Error("a non-apply turn must not expose Radar's write tools")
	}
	if !strings.Contains(args, "--allow-tool=radar") {
		t.Errorf("safeguarded mode must approve Radar's server without --allow-all-tools; got %q", args)
	}
	if strings.Contains(args, "--allow-all-tools") {
		t.Errorf("safeguarded mode must NOT allow every tool; got %q", args)
	}
	if !strings.Contains(args, "--disable-builtin-mcps") {
		t.Errorf("safeguarded mode must drop the built-in GitHub MCP server; got %q", args)
	}
	if !strings.Contains(args, "--no-custom-instructions") {
		t.Errorf("safeguarded mode must not load AGENTS.md; got %q", args)
	}
	if iso.Dir == "" {
		t.Error("safeguarded mode must run in a dedicated (empty) cwd")
	}
	if iso.Env == nil {
		t.Error("safeguarded mode must use a minimized env, not inherit nil")
	}
	// The user's own Copilot home holds their MCP servers, settings and — with no
	// flag to disable them — their hooks, which run shell in -p mode.
	wantHome := "COPILOT_HOME=" + CopilotHomeDir(home)
	if !envHas(iso.Env, wantHome) {
		t.Errorf("safeguarded mode must redirect COPILOT_HOME to %q; got %v", wantHome, iso.Env)
	}
	for _, kv := range iso.Env {
		if strings.HasPrefix(kv, "COPILOT_HOME=") && kv != wantHome {
			t.Errorf("the user's COPILOT_HOME must not leak into a safeguarded run; got %q", kv)
		}
	}

	apply, cleanupApply, err := newAgent().command(ctx, turnSpec{
		mcpURL: "http://localhost:1/mcp", prompt: "fix", profile: ExecutionProfileSafeguarded, apply: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupApply()
	if !strings.Contains(strings.Join(apply.Args, " "), "radar-apply_resource") {
		t.Error("a confirmed apply turn must expose Radar's write tools")
	}

	my, cleanup2, err := newAgent().command(ctx, turnSpec{
		mcpURL: "http://localhost:1/mcp-readonly", prompt: "go", profile: ExecutionProfileFullLocal,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup2()
	myArgs := strings.Join(my.Args, " ")
	if !strings.Contains(myArgs, "--allow-all-tools") {
		t.Errorf("full-local mode runs with the user's full toolset; got %q", myArgs)
	}
	if strings.Contains(myArgs, "--available-tools") {
		t.Errorf("full-local mode must not constrain the user's setup; got %q", myArgs)
	}
	if my.Dir != "" {
		t.Error("full-local mode should inherit radar's cwd (no override)")
	}
	if my.Env != nil {
		t.Error("full-local mode must inherit the user's env, including their own COPILOT_HOME")
	}
	// The redirect replaced the probe: no run of either profile enumerates servers.
	for name, cmdArgs := range map[string]string{"safeguarded": args, "full-local": myArgs} {
		if strings.Contains(cmdArgs, "--disable-mcp-server") {
			t.Errorf("%s mode should not enumerate MCP servers by name; got %q", name, cmdArgs)
		}
	}

	// The transcript carries cluster data: it must never be published to GitHub's
	// web/mobile surfaces, in either profile.
	for name, cmdArgs := range map[string]string{"safeguarded": args, "full-local": myArgs} {
		if !strings.Contains(cmdArgs, "--no-remote-export") || !strings.Contains(cmdArgs, "--no-remote") {
			t.Errorf("%s mode must disable session export to GitHub; got %q", name, cmdArgs)
		}
		if !strings.Contains(cmdArgs, "--no-ask-user") {
			t.Errorf("%s mode must disable ask_user (no TTY to answer it); got %q", name, cmdArgs)
		}
		// A CLI that swaps its own binary mid-investigation is a mid-run behavior
		// change; the CI detection that would disable it can't fire under our env.
		if !strings.Contains(cmdArgs, "--no-auto-update") {
			t.Errorf("%s mode must pin the CLI version for the run; got %q", name, cmdArgs)
		}
	}

	if _, _, err := newAgent().command(ctx, turnSpec{profile: ""}); err == nil {
		t.Fatal("Copilot must reject an empty profile rather than fail open")
	}
}

// TestCopilotResumeUsesEqualsForm pins a parsing trap: --resume takes an OPTIONAL
// value, so a space-separated id would be read as a positional prompt instead.
func TestCopilotResumeUsesEqualsForm(t *testing.T) {
	a := testCopilotAgent(t.TempDir())
	cmd, cleanup, err := a.command(context.Background(), turnSpec{
		mcpURL: "http://localhost:1/mcp-readonly", prompt: "go",
		profile: ExecutionProfileFullLocal, sessionID: "sess-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	var found bool
	for _, arg := range cmd.Args {
		if arg == "--resume=sess-1" {
			found = true
		}
		if arg == "--resume" {
			t.Error("--resume must use the =value form; bare --resume would swallow the id as a prompt")
		}
	}
	if !found {
		t.Errorf("expected --resume=sess-1 in %v", cmd.Args)
	}
}

// TestCopilotEffortOnlyWhenChosen pins that Radar passes NO --effort unless the
// user picked one. Copilot rejects the flag outright when the model resolves to
// "auto" — the free tier's default — so a Radar-chosen default would fail every
// out-of-the-box run with exit 1.
func TestCopilotEffortOnlyWhenChosen(t *testing.T) {
	a := testCopilotAgent(t.TempDir())
	base := turnSpec{
		mcpURL: "http://localhost:1/mcp-readonly", prompt: "go", profile: ExecutionProfileFullLocal,
	}
	cmd, cleanup, err := a.command(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if strings.Contains(strings.Join(cmd.Args, " "), "--effort") {
		t.Errorf("an unset effort must leave the CLI's own default alone; got %v", cmd.Args)
	}

	chosen := base
	chosen.effort = "xhigh"
	cmd2, cleanup2, err := a.command(context.Background(), chosen)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup2()
	if !strings.Contains(strings.Join(cmd2.Args, " "), "--effort xhigh") {
		t.Errorf("a chosen effort must be passed through; got %v", cmd2.Args)
	}
}

// TestCopilotHomeSeeded pins that the Radar-owned COPILOT_HOME is created with a
// config of our own. The redirect is what keeps the user's hooks and MCP servers
// out of a safeguarded run, so it must not depend on the directory pre-existing.
func TestCopilotHomeSeeded(t *testing.T) {
	home := t.TempDir()
	dir, err := testCopilotAgent(home).copilotHome()
	if err != nil {
		t.Fatal(err)
	}
	if dir != CopilotHomeDir(home) {
		t.Errorf("copilotHome() = %q, want %q", dir, CopilotHomeDir(home))
	}
	cfg, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatalf("expected a Radar-owned Copilot config: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(cfg, &parsed); err != nil {
		t.Fatalf("seeded config is not valid JSON: %v", err)
	}
	if parsed["disableAllHooks"] != true {
		t.Errorf("the Radar-owned config must disable hooks; got %v", parsed)
	}

	// A config the user edited afterwards is theirs — seeding must not stomp it.
	custom := []byte(`{"disableAllHooks":true,"banner":"always"}`)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), custom, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := testCopilotAgent(home).copilotHome(); err != nil {
		t.Fatal(err)
	}
	again, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil || string(again) != string(custom) {
		t.Errorf("an existing config must be left alone; got %q (%v)", again, err)
	}
}

// TestResolveAgentCopilot pins binary-name routing to the Copilot backend — it must
// match before the Claude default, or every Copilot run would be driven as Claude.
func TestResolveAgentCopilot(t *testing.T) {
	cases := map[string]string{
		"copilot":                   "copilot",
		"/opt/homebrew/bin/copilot": "copilot",
		"Copilot":                   "copilot",
		"codex":                     "codex",
		"cursor-agent":              "cursor-agent",
		"claude":                    "claude",
	}
	for bin, want := range cases {
		if got := resolveAgent(bin).Name(); got != want {
			t.Errorf("resolveAgent(%q).Name() = %q, want %q", bin, got, want)
		}
	}
}

// TestCopilotReasoningEfforts pins that the effort vocabularies stay distinct:
// Copilot's extra tiers are rejected by Codex, and agents without the knob accept
// only the empty default.
func TestCopilotReasoningEfforts(t *testing.T) {
	if !SupportsEffort("copilot", "xhigh") || !SupportsEffort("copilot", "max") {
		t.Error("Copilot must accept its own wider effort levels")
	}
	if SupportsEffort("codex", "xhigh") {
		t.Error("Codex must not be handed Copilot's effort levels")
	}
	if SupportsEffort("claude", "high") {
		t.Error("agents with no effort knob must accept only the default")
	}
	for _, agent := range []string{"claude", "codex", "copilot", "cursor-agent"} {
		if !SupportsEffort(agent, "") {
			t.Errorf("%s must accept the empty (default) effort", agent)
		}
	}
}

// TestCopilotParseStream_PolicyWarningAlone pins the case with no per-server event
// at all: a policy that blocks third-party MCP can drop Radar's server silently, so
// the warning is the only evidence. Unrelated policy warnings must stay harmless —
// they say nothing about cluster access.
func TestCopilotParseStream_PolicyWarningAlone(t *testing.T) {
	verdict := "{\"type\":\"assistant.message\",\"data\":{\"phase\":\"final_answer\",\"content\":\"```json\\n{\\\"healthy\\\":true}\\n```\"}}"
	result := `{"type":"result","sessionId":"s1","exitCode":0}`
	agent := &copilotAgent{bin: "copilot"}

	blocked := strings.Join([]string{
		`{"type":"session.warning","data":{"message":"Third-party MCP servers are disabled by your organization's Copilot policy.","warningType":"policy"},"ephemeral":true}`,
		verdict, result,
	}, "\n")
	if diag := agent.parseStream(strings.NewReader(blocked), func(StreamEvent) {}); diag.mcpErrText == "" {
		t.Error("a policy that blocks MCP must fail the turn even with no per-server event")
	}

	unrelated := strings.Join([]string{
		`{"type":"session.warning","data":{"message":"Your organization restricts the models available to this account.","warningType":"policy"},"ephemeral":true}`,
		verdict, result,
	}, "\n")
	if diag := agent.parseStream(strings.NewReader(unrelated), func(StreamEvent) {}); diag.mcpErrText != "" {
		t.Errorf("an unrelated policy warning must not fail the turn; got %q", diag.mcpErrText)
	}
}
