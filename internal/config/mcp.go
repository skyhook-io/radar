package config

import (
	"flag"
	"fmt"
	"os"
	"strconv"
)

// MCPMode is the effective server-wide MCP exposure policy.
type MCPMode struct {
	Enabled  bool
	ReadOnly bool
}

// MCPFlags shares flag names and mode resolution between CLI and Desktop.
type MCPFlags struct {
	fs        *flag.FlagSet
	file      Config
	noMCP     *bool
	readOnly  *bool
	execTools *bool
}

// RegisterMCPFlags registers the shared CLI and Desktop MCP mode switches.
func RegisterMCPFlags(fs *flag.FlagSet, file Config) MCPFlags {
	return MCPFlags{
		fs: fs, file: file,
		execTools: fs.Bool("mcp-exec-tools", file.MCPExecTools != nil && *file.MCPExecTools, "Enable exec_pod: run a non-interactive container command as the caller (off by default; ignored with --disable-exec or --mcp-read-only)"),
		noMCP:     fs.Bool("no-mcp", !file.MCPEnabledOr(true), "Disable all MCP endpoints"),
		readOnly: fs.Bool("mcp-read-only", file.MCPReadOnly != nil && *file.MCPReadOnly,
			"Expose only read-only MCP tools (env: RADAR_MCP_READ_ONLY)"),
	}
}

// Resolve applies CLI overrides to environment and persisted MCP defaults.
func (f MCPFlags) Resolve() (MCPMode, error) {
	var noMCP, readOnly *bool
	f.fs.Visit(func(v *flag.Flag) {
		switch v.Name {
		case "no-mcp":
			noMCP = f.noMCP
		case "mcp-read-only":
			readOnly = f.readOnly
		}
	})
	return resolveMCP(f.file, os.LookupEnv, noMCP, readOnly)
}

// ValidateMCPFile checks persisted MCP settings without environment or CLI overrides.
func ValidateMCPFile(c Config) error {
	_, err := resolveMCP(c, func(string) (string, bool) { return "", false }, nil, nil)
	return err
}

// resolveMCP merges explicit settings in source order. Selecting a mode clears
// the competing lower-priority mode; false only clears the named setting.
func resolveMCP(file Config, lookup func(string) (string, bool), noMCP, readOnly *bool) (MCPMode, error) {
	disabled, ro := false, false
	apply := func(disable, read *bool, source string) error {
		if disable != nil && *disable && read != nil && *read {
			return fmt.Errorf("%s cannot enable both disabled and read-only MCP modes; choose one", source)
		}
		if disable != nil {
			disabled = *disable
			if disabled {
				ro = false
			}
		}
		if read != nil {
			ro = *read
			if ro {
				disabled = false
			}
		}
		return nil
	}
	var fileDisabled *bool
	if file.MCP != nil {
		v := !*file.MCP
		fileDisabled = &v
	}
	if err := apply(fileDisabled, file.MCPReadOnly, "config (mcp=false, mcpReadOnly=true)"); err != nil {
		return MCPMode{}, err
	}
	if raw, ok := lookup("RADAR_MCP_READ_ONLY"); ok && raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return MCPMode{}, fmt.Errorf("RADAR_MCP_READ_ONLY must be a boolean: %w", err)
		}
		if err := apply(nil, &v, "environment"); err != nil {
			return MCPMode{}, err
		}
	}
	if err := apply(noMCP, readOnly, "--no-mcp and --mcp-read-only"); err != nil {
		return MCPMode{}, err
	}
	return MCPMode{Enabled: !disabled, ReadOnly: ro}, nil
}

// ExecToolsEnabled reports the operator opt-in; server policy applies the final gate.
func (f MCPFlags) ExecToolsEnabled() bool { return *f.execTools }
