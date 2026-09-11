package ai

import (
	"context"
	"strings"
	"testing"
)

func TestClaudeExecutionProfiles(t *testing.T) {
	a := &claudeAgent{bin: "claude"}
	safeguarded, cleanup, err := a.command(context.Background(), turnSpec{
		mcpURL: "http://localhost:1/mcp-readonly", prompt: "go",
		profile: ExecutionProfileSafeguarded, maxTurns: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	safeguardedArgs := strings.Join(safeguarded.Args, " ")
	for _, want := range []string{"--strict-mcp-config", "--tools ", "--allowedTools", "--permission-mode acceptEdits"} {
		if !strings.Contains(safeguardedArgs, want) {
			t.Errorf("safeguarded Claude command missing %q: %q", want, safeguardedArgs)
		}
	}
	if safeguarded.Env == nil {
		t.Error("safeguarded Claude must use a minimized environment")
	}

	fullLocal, cleanupFullLocal, err := a.command(context.Background(), turnSpec{
		mcpURL: "http://localhost:1/mcp-readonly", prompt: "go",
		profile: ExecutionProfileFullLocal, maxTurns: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupFullLocal()
	fullLocalArgs := strings.Join(fullLocal.Args, " ")
	if !strings.Contains(fullLocalArgs, "--mcp-config") {
		t.Errorf("full-local Claude must still mount Radar MCP: %q", fullLocalArgs)
	}
	for _, excluded := range []string{"--strict-mcp-config", "--tools", "--allowedTools", "--permission-mode"} {
		if strings.Contains(fullLocalArgs, excluded) {
			t.Errorf("full-local Claude must inherit normal setup, found %q in %q", excluded, fullLocalArgs)
		}
	}
	if fullLocal.Env != nil {
		t.Error("full-local Claude must inherit the user's environment")
	}

	if _, _, err := a.command(context.Background(), turnSpec{profile: ""}); err == nil {
		t.Fatal("Claude must reject an empty profile rather than fail open")
	}
}

func TestClaudeSafeguardedApplyAllowsEveryRadarWriteTool(t *testing.T) {
	a := &claudeAgent{bin: "claude"}
	cmd, cleanup, err := a.command(context.Background(), turnSpec{
		mcpURL: "http://localhost:1/mcp", prompt: "apply",
		profile: ExecutionProfileSafeguarded, apply: true, maxTurns: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	args := strings.Join(cmd.Args, " ")
	for _, tool := range radarWriteTools {
		if !strings.Contains(args, "mcp__radar__"+tool) {
			t.Errorf("safeguarded apply command missing write tool %q: %q", tool, args)
		}
	}
	if !strings.Contains(args, "mcp__radar__manage_rollout") {
		t.Errorf("real Rollout mutations must be available on apply turns: %q", args)
	}
}
