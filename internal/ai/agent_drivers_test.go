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
		mcpURL: url, prompt: "investigate", workdir: dir, model: "claude-3-5-sonnet",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	args := strings.Join(cmd.Args, " ")
	if !strings.Contains(args, "--format json") || !strings.Contains(args, "--auto") {
		t.Errorf("expected --format json and --auto in args; got %q", args)
	}
	if !strings.Contains(args, "--model claude-3-5-sonnet") {
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

func TestAntigravityConfigAndCommand(t *testing.T) {
	a := &antigravityAgent{bin: "agy"}
	dir := t.TempDir()
	const url = "http://localhost:9280/mcp-readonly"

	cmd, cleanup, err := a.command(context.Background(), turnSpec{
		mcpURL: url, prompt: "investigate", workdir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	args := strings.Join(cmd.Args, " ")
	if !strings.Contains(args, "-p") || !strings.Contains(args, "investigate") {
		t.Errorf("expected -p flag and prompt in args; got %q", args)
	}

	for _, relPath := range []string{"mcp.json", ".mcp.json", ".gemini/mcp.json", ".cursor/mcp.json", ".antigravity/mcp.json", ".antigravitycli/mcp.json"} {
		cfgPath := filepath.Join(dir, relPath)
		b, err := os.ReadFile(cfgPath)
		if err != nil {
			t.Fatalf("expected config file at %s: %v", cfgPath, err)
		}
		var cfg struct {
			MCPServers map[string]struct {
				URL string `json:"url"`
			} `json:"mcpServers"`
		}
		if err := json.Unmarshal(b, &cfg); err != nil {
			t.Fatalf("config at %s not valid JSON: %v", cfgPath, err)
		}
		if cfg.MCPServers["radar"].URL != url {
			t.Errorf("config at %s radar url = %q, want %q", cfgPath, cfg.MCPServers["radar"].URL, url)
		}
	}
}

func TestPiConfigAndCommand(t *testing.T) {
	a := &piAgent{bin: "pi"}
	dir := t.TempDir()
	const url = "http://localhost:9280/mcp-readonly"

	cmd, cleanup, err := a.command(context.Background(), turnSpec{
		mcpURL: url, prompt: "investigate", workdir: dir,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	args := strings.Join(cmd.Args, " ")
	if !strings.Contains(args, "-p") || !strings.Contains(args, "investigate") {
		t.Errorf("expected -p flag and prompt in args; got %q", args)
	}

	for _, relPath := range []string{"mcp.json", ".mcp.json", ".pi/mcp.json"} {
		cfgPath := filepath.Join(dir, relPath)
		b, err := os.ReadFile(cfgPath)
		if err != nil {
			t.Fatalf("expected config file at %s: %v", cfgPath, err)
		}
		var cfg struct {
			MCPServers map[string]struct {
				URL string `json:"url"`
			} `json:"mcpServers"`
		}
		if err := json.Unmarshal(b, &cfg); err != nil {
			t.Fatalf("config at %s not valid JSON: %v", cfgPath, err)
		}
		if cfg.MCPServers["radar"].URL != url {
			t.Errorf("config at %s radar url = %q, want %q", cfgPath, cfg.MCPServers["radar"].URL, url)
		}
	}
}

func TestResolveAgentPi(t *testing.T) {
	if a := resolveAgent("pi"); a.Name() != "pi" {
		t.Errorf("resolveAgent(pi) = %s, want pi", a.Name())
	}
	if a := resolveAgent("pi-coding-agent"); a.Name() != "pi" {
		t.Errorf("resolveAgent(pi-coding-agent) = %s, want pi", a.Name())
	}
	if a := resolveAgent("pip"); a.Name() == "pi" {
		t.Errorf("resolveAgent(pip) should not resolve to pi agent")
	}
}
