package mcp

import (
	"context"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// setupDialogCatalogPath is the human-facing tool catalog rendered by the MCP
// setup dialog. It must list exactly the tools registered here.
const setupDialogCatalogPath = "../../web/src/components/home/mcpToolCatalog.ts"

// toolNameInCatalog matches the `name: 'tool_name'` entries in the catalog.
// Anchored to line start (catalog entries always sit at line-start
// indentation) so a `name: '...'` substring appearing mid-line inside a
// description string can't false-match. Tool names use the `name:` key;
// parameters use `arg:`; the TS interface decl `name: string` has no quote
// after the colon. None of those match.
var toolNameInCatalog = regexp.MustCompile(`(?m)^\s*name:\s*'([a-z][a-z0-9_]*)'`)

// TestSetupDialogCoversAllTools fails when the registered MCP tools and the
// setup-dialog catalog drift apart — a tool added to registerTools without a
// catalog entry (or vice versa). Keep both in sync; users discover tools
// through that dialog.
func TestSetupDialogCoversAllTools(t *testing.T) {
	registered := map[string]bool{}
	for _, tool := range listRegisteredTools(t) {
		registered[tool.Name] = true
	}

	raw, err := os.ReadFile(setupDialogCatalogPath)
	if err != nil {
		t.Fatalf("read %s: %v", setupDialogCatalogPath, err)
	}
	catalog := map[string]bool{}
	for _, m := range toolNameInCatalog.FindAllStringSubmatch(string(raw), -1) {
		catalog[m[1]] = true
	}
	if len(catalog) == 0 {
		t.Fatalf("no tool names parsed from %s — did the catalog format change?", setupDialogCatalogPath)
	}

	var missingFromDialog, staleInDialog []string
	for name := range registered {
		if !catalog[name] {
			missingFromDialog = append(missingFromDialog, name)
		}
	}
	for name := range catalog {
		if !registered[name] {
			staleInDialog = append(staleInDialog, name)
		}
	}
	sort.Strings(missingFromDialog)
	sort.Strings(staleInDialog)

	if len(missingFromDialog) > 0 {
		t.Errorf("MCP tools registered but missing from the setup dialog catalog (%s) — add them: %s",
			setupDialogCatalogPath, strings.Join(missingFromDialog, ", "))
	}
	if len(staleInDialog) > 0 {
		t.Errorf("setup dialog catalog (%s) lists tools that are not registered — remove them: %s",
			setupDialogCatalogPath, strings.Join(staleInDialog, ", "))
	}
}

func TestRegisteredToolAnnotations(t *testing.T) {
	tools := listRegisteredTools(t)
	writeTools := map[string]bool{
		"manage_workload": true,
		"manage_cronjob":  true,
		"manage_gitops":   true,
		"apply_resource":  true,
		"patch_resource":  true,
		"manage_node":     true,
	}

	seenWriteTools := map[string]bool{}
	for _, tool := range tools {
		if tool.Annotations == nil {
			t.Fatalf("tool %q missing annotations", tool.Name)
		}
		if tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
			t.Errorf("tool %q should set openWorldHint=false", tool.Name)
		}
		if writeTools[tool.Name] {
			seenWriteTools[tool.Name] = true
			if tool.Annotations.ReadOnlyHint {
				t.Errorf("write tool %q should not set readOnlyHint=true", tool.Name)
			}
			if tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint {
				t.Errorf("write tool %q should set destructiveHint=true", tool.Name)
			}
			continue
		}
		if !tool.Annotations.ReadOnlyHint {
			t.Errorf("read tool %q should set readOnlyHint=true", tool.Name)
		}
		if tool.Annotations.DestructiveHint != nil && *tool.Annotations.DestructiveHint {
			t.Errorf("read tool %q should not set destructiveHint=true", tool.Name)
		}
	}

	for name := range writeTools {
		if !seenWriteTools[name] {
			t.Errorf("write tool %q was not registered", name)
		}
	}
}

// writeToolNames is the mutating tool set the read-only mount must exclude.
var writeToolNames = []string{
	"manage_workload", "manage_cronjob", "manage_gitops",
	"apply_resource", "patch_resource", "manage_node",
}

// TestReadOnlyServerExcludesWriteTools is the load-bearing guarantee of the
// read-only MCP mount: an investigation pointed at it can't even discover a
// mutating tool. If a tool is reclassified, this fails until the lists agree.
func TestReadOnlyServerExcludesWriteTools(t *testing.T) {
	ro := map[string]bool{}
	for _, tool := range listRegisteredToolsWith(t, false) {
		ro[tool.Name] = true
	}
	for _, w := range writeToolNames {
		if ro[w] {
			t.Errorf("read-only server exposes write tool %q", w)
		}
	}
	// Sanity: read tools are still present.
	if !ro["get_resource"] || !ro["diagnose"] {
		t.Error("read-only server is missing expected read tools")
	}

	full := map[string]bool{}
	for _, tool := range listRegisteredToolsWith(t, true) {
		full[tool.Name] = true
	}
	for _, w := range writeToolNames {
		if !full[w] {
			t.Errorf("full server is missing write tool %q", w)
		}
	}
}

func listRegisteredTools(t *testing.T) []*mcpsdk.Tool {
	return listRegisteredToolsWith(t, true)
}

func listRegisteredToolsWith(t *testing.T, includeWrites bool) []*mcpsdk.Tool {
	t.Helper()

	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "radar-test", Version: "test"}, nil)
	registerTools(server, includeWrites)

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "radar-test-client", Version: "test"}, nil)
	ctx := context.Background()
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	t.Cleanup(func() {
		_ = clientSession.Close()
		_ = serverSession.Wait()
	})

	result, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(result.Tools) == 0 {
		t.Fatal("no MCP tools registered")
	}
	return result.Tools
}
