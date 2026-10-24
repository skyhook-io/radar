package config

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStartupConfigDoesNotDiscardMCPPolicy(t *testing.T) {
	for _, raw := range []string{
		`{"mcpReadOnly":"true"}`,
		`{"mcpReadOnly":true,"port":"9280"}`,
		`{"mcp":false,"mcpReadOnly":`,
	} {
		t.Run(raw, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			if err := os.MkdirAll(filepath.Dir(Path()), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(Path(), []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadForStartup(); err == nil || !strings.Contains(err.Error(), "invalid Radar config") {
				t.Fatalf("malformed startup config accepted: %v", err)
			}
		})
	}
}

func TestStartupConfigMissingUsesDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	c, err := LoadForStartup()
	if err != nil {
		t.Fatal(err)
	}
	mode, err := resolveMCP(c, func(string) (string, bool) { return "", false }, nil, nil)
	if err != nil || !mode.Enabled || mode.ReadOnly {
		t.Fatalf("missing config mode: %+v, %v", mode, err)
	}
}

func TestStartupConfigUnreadableStopsStartup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := os.MkdirAll(Path(), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadForStartup(); err == nil || !strings.Contains(err.Error(), "cannot read Radar config") {
		t.Fatalf("unreadable startup config accepted: %v", err)
	}
}

func TestMCPModePrecedence(t *testing.T) {
	ptr := func(v bool) *bool { return &v }
	for _, tt := range []struct {
		name string
		file Config
		env  string
		args []string
		want MCPMode
		err  string
	}{
		{name: "default", want: MCPMode{Enabled: true}},
		{name: "disabled flag", args: []string{"--no-mcp"}},
		{name: "read-only flag", args: []string{"--mcp-read-only"}, want: MCPMode{true, true}},
		{name: "explicit contradiction", args: []string{"--no-mcp", "--mcp-read-only"}, err: "--no-mcp and --mcp-read-only"},
		{name: "config disabled", file: Config{MCP: ptr(false)}},
		{name: "config read-only", file: Config{MCPReadOnly: ptr(true)}, want: MCPMode{true, true}},
		{name: "config false", file: Config{MCPReadOnly: ptr(false)}, want: MCPMode{true, false}},
		{name: "config contradiction", file: Config{MCP: ptr(false), MCPReadOnly: ptr(true)}, err: "config"},
		{name: "env 1", env: "1", want: MCPMode{true, true}},
		{name: "env TRUE", env: "TRUE", want: MCPMode{true, true}},
		{name: "env false", file: Config{MCPReadOnly: ptr(true)}, env: "false", want: MCPMode{true, false}},
		{name: "env zero preserves disabled", file: Config{MCP: ptr(false)}, env: "0"},
		{name: "env selects over config disabled", file: Config{MCP: ptr(false)}, env: "true", want: MCPMode{true, true}},
		{name: "cli disables env", env: "true", args: []string{"--no-mcp"}},
		{name: "cli clears env", env: "1", args: []string{"--mcp-read-only=false"}, want: MCPMode{true, false}},
		{name: "cli read-only overrides disabled file", file: Config{MCP: ptr(false)}, args: []string{"--mcp-read-only"}, want: MCPMode{true, true}},
		{name: "cli disabled overrides read-only file", file: Config{MCPReadOnly: ptr(true)}, args: []string{"--no-mcp"}},
		{name: "cli enables disabled file", file: Config{MCP: ptr(false)}, args: []string{"--no-mcp=false"}, want: MCPMode{true, false}},
		{name: "cli false preserves readonly", env: "true", args: []string{"--no-mcp=false"}, want: MCPMode{true, true}},
		{name: "both explicit false", file: Config{MCP: ptr(false)}, env: "true", args: []string{"--no-mcp=false", "--mcp-read-only=false"}, want: MCPMode{true, false}},
		{name: "invalid env", env: "yes", err: "RADAR_MCP_READ_ONLY must be a boolean"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("RADAR_MCP_READ_ONLY", tt.env)
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			flags := RegisterMCPFlags(fs, tt.file)
			if err := fs.Parse(tt.args); err != nil {
				t.Fatal(err)
			}
			got, err := flags.Resolve()
			if tt.err != "" {
				if err == nil || !strings.Contains(err.Error(), tt.err) {
					t.Fatalf("error = %v, want %q", err, tt.err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("mode = %+v, %v; want %+v", got, err, tt.want)
			}
		})
	}
}

func TestMCPReadOnlyPersistence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, value := range []bool{true, false} {
		if err := Save(Config{MCPReadOnly: &value}); err != nil {
			t.Fatal(err)
		}
		got := Load()
		if got.MCPReadOnly == nil || *got.MCPReadOnly != value {
			t.Fatalf("round trip: %+v", got)
		}
	}
}
