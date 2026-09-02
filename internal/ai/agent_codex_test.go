package ai

import (
	"context"
	"strings"
	"testing"
)

// TestCodexParseStream_FormatPin locks the Codex `exec --json` JSONL schema we
// depend on, captured from a live MCP tool call: thread.started carries the
// resumable session id, mcp_tool_call items drive running/done steps (bare tool
// name, no prefix to strip), and the final agent_message is the report body.
func TestCodexParseStream_FormatPin(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"thread.started","thread_id":"019eef06-e99b-70f1-a25f-aba70f3ea57e"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"id":"r0","type":"reasoning","text":"checking pods"}}`,
		`{"type":"item.started","item":{"id":"item_0","type":"mcp_tool_call","server":"radar","tool":"diagnose","arguments":{"name":"x"},"status":"in_progress"}}`,
		`{"type":"item.completed","item":{"id":"item_0","type":"mcp_tool_call","server":"radar","tool":"diagnose","arguments":{"name":"x"},"result":{"content":[{"type":"text","text":"crashloop detail"}]},"status":"completed"}}`,
		"{\"type\":\"item.completed\",\"item\":{\"id\":\"item_1\",\"type\":\"agent_message\",\"text\":\"bad tag.\\n\\n```json\\n{\\\"root_cause\\\":\\\"bad tag\\\"}\\n```\"}}",
		`{"type":"turn.completed","usage":{"input_tokens":41789,"output_tokens":23}}`,
	}, "\n")

	var running, done bool
	var doneIsError *bool
	var thinking, doneResult, runningSummary string
	agent := &codexAgent{bin: "codex"}
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
				if ev.Step.Tool != "diagnose" {
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
		t.Errorf("expected reasoning→thinking %q, got %q", "checking pods", thinking)
	}
	if runningSummary == "" {
		t.Errorf("expected args preview on running step")
	}
	if !strings.Contains(doneResult, "crashloop detail") {
		t.Errorf("expected tool result preview on done step, got %q", doneResult)
	}
	if doneIsError == nil || *doneIsError {
		t.Errorf("completed Codex tool result should be confirmed success, got %v", doneIsError)
	}
	if diag.RootCause != "bad tag" {
		t.Errorf("root cause not parsed from agent_message: %q", diag.RootCause)
	}
	if diag.SessionID != "019eef06-e99b-70f1-a25f-aba70f3ea57e" {
		t.Errorf("session id (thread_id) not captured: %q", diag.SessionID)
	}
}

func TestCodexToolResultErrorState(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"item.completed","item":{"id":"ok","type":"mcp_tool_call","tool":"get_resource","status":"completed","result":{"content":[{"type":"text","text":"ready"}]}}}`,
		`{"type":"item.completed","item":{"id":"bad","type":"mcp_tool_call","tool":"get_resource","status":"failed","error":{"message":"permission denied"}}}`,
		`{"type":"item.completed","item":{"id":"unknown","type":"mcp_tool_call","tool":"get_resource","status":"in_progress"}}`,
	}, "\n")

	type observed struct {
		isError *bool
		result  string
	}
	got := map[string]observed{}
	agent := &codexAgent{bin: "codex"}
	agent.parseStream(strings.NewReader(stream), func(ev StreamEvent) {
		if ev.Step != nil {
			got[ev.Step.ID] = observed{isError: ev.Step.IsError, result: ev.Step.Result}
		}
	})
	if got["ok"].isError == nil || *got["ok"].isError {
		t.Errorf("completed status = %v, want confirmed false", got["ok"].isError)
	}
	if got["bad"].isError == nil || !*got["bad"].isError {
		t.Errorf("failed status = %v, want confirmed true", got["bad"].isError)
	}
	if got["bad"].result != "permission denied" {
		t.Errorf("failed result = %q, want the producer's error message", got["bad"].result)
	}
	if got["unknown"].isError != nil {
		t.Errorf("non-terminal status = %v, want unknown", got["unknown"].isError)
	}
}

// TestCodexExecutionProfiles pins the security-relevant difference between safeguarded
// and full-local: safeguarded drops the user's config + runs in an empty cwd; both
// modes keep the read-only sandbox.
func TestCodexExecutionProfiles(t *testing.T) {
	a := &codexAgent{bin: "codex"}
	ctx := context.Background()

	iso, cleanup, err := a.command(ctx, turnSpec{mcpURL: "http://localhost:1/mcp-readonly", prompt: "go", profile: ExecutionProfileSafeguarded})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	args := strings.Join(iso.Args, " ")
	if !strings.Contains(args, "--ignore-user-config") {
		t.Errorf("safeguarded mode must pass --ignore-user-config; got %q", args)
	}
	if !strings.Contains(args, "--sandbox read-only") {
		t.Errorf("safeguarded mode must keep --sandbox read-only; got %q", args)
	}
	if iso.Dir == "" {
		t.Error("safeguarded mode must run in a dedicated (empty) cwd")
	}
	if iso.Env == nil {
		t.Error("safeguarded mode must use a minimized env, not inherit nil")
	}

	my, cleanup2, err := a.command(ctx, turnSpec{mcpURL: "http://localhost:1/mcp-readonly", prompt: "go", profile: ExecutionProfileFullLocal})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup2()
	myArgs := strings.Join(my.Args, " ")
	if strings.Contains(myArgs, "--ignore-user-config") {
		t.Errorf("full-local mode must NOT drop user config; got %q", myArgs)
	}
	if !strings.Contains(myArgs, "--sandbox read-only") {
		t.Errorf("full-local mode must still keep --sandbox read-only; got %q", myArgs)
	}
	if my.Dir != "" {
		t.Error("full-local mode should inherit radar's cwd (no override)")
	}
	if _, _, err := a.command(ctx, turnSpec{profile: ""}); err == nil {
		t.Fatal("Codex must reject an empty profile rather than fail open")
	}
}

// TestResolveAgentCodex pins binary-name routing to the Codex backend.
func TestResolveAgentCodex(t *testing.T) {
	cases := map[string]string{
		"codex":                   "codex",
		"/usr/local/bin/codex":    "codex",
		"Codex":                   "codex",
		"claude":                  "claude",
		"/opt/claude-code/claude": "claude",
	}
	for bin, want := range cases {
		if got := resolveAgent(bin).Name(); got != want {
			t.Errorf("resolveAgent(%q).Name() = %q, want %q", bin, got, want)
		}
	}
}
