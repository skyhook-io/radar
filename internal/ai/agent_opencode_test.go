package ai

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpencodeConfigAndCommand(t *testing.T) {
	a := &opencodeAgent{bin: "opencode"}
	dir := t.TempDir()
	const url = "http://localhost:9280/mcp-readonly"

	cmd, cleanup, err := a.command(context.Background(), turnSpec{
		mcpURL: url, prompt: "investigate", workdir: dir, model: "opencode-1.5-pro", profile: ExecutionProfileFullLocal,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	args := strings.Join(cmd.Args, " ")
	if !strings.Contains(args, "--format json") || !strings.Contains(args, "--auto") {
		t.Errorf("expected --format json and --auto in args; got %q", args)
	}
	if !strings.Contains(args, "--model opencode-1.5-pro") {
		t.Errorf("expected --model flag in args; got %q", args)
	}
	if !strings.Contains(args, "investigate") {
		t.Errorf("expected prompt in args; got %q", args)
	}

	cfgPath := filepath.Join(dir, "opencode.json")
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("expected opencode.json at %s: %v", cfgPath, err)
	}

	var cfg struct {
		MCP map[string]struct {
			Type string `json:"type"`
			URL  string `json:"url"`
		} `json:"mcp"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("opencode.json not valid JSON: %v", err)
	}
	srv, ok := cfg.MCP["radar"]
	if !ok {
		t.Fatalf("opencode.json missing radar server entry: %s", string(b))
	}
	if srv.Type != "remote" || srv.URL != url {
		t.Errorf("opencode.json radar entry = %+v, want type=remote, url=%s", srv, url)
	}
}
