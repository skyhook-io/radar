package ai

import (
	"context"
	"strings"
	"testing"
)

func TestClaudeExecutionProfile(t *testing.T) {
	a := &claudeAgent{bin: "claude"}
	cmd, cleanup, err := a.command(context.Background(), turnSpec{
		mcpURL: "http://localhost:1/mcp-readonly", prompt: "go",
		profile: ExecutionProfileSafeguarded, maxTurns: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	args := strings.Join(cmd.Args, " ")
	for _, want := range []string{"--strict-mcp-config", "--tools ", "--allowedTools"} {
		if !strings.Contains(args, want) {
			t.Errorf("safeguarded Claude command missing %q: %q", want, args)
		}
	}
	if _, _, err := a.command(context.Background(), turnSpec{
		profile: ExecutionProfileFullLocal,
	}); err == nil {
		t.Fatal("Claude must reject full-local until the driver implements it")
	}
}
