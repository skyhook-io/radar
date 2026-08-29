package mcp

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/meaningfulchanges"
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

func TestIssuesToolPreservesEvidenceBoundaries(t *testing.T) {
	// Exact reason-token behavior is covered by meaningfulchanges tests. This
	// pins the agent-facing interpretation that must survive copy reductions.
	var issuesTool *mcpsdk.Tool
	for _, tool := range listRegisteredTools(t) {
		if tool.Name == "issues" {
			issuesTool = tool
			break
		}
	}
	if issuesTool == nil || issuesTool.Description == "" {
		t.Fatal("issues tool is not registered or has no description")
	}
	for _, token := range []string{
		meaningfulchanges.ChangesReasonNoCriticalIssues,
		meaningfulchanges.ChangesReasonWithAllCreationTimeCriticalIssues,
		"`recent_changes`",
		"`recent_changes_guidance`",
		"`not_linked_to_returned_issues`",
		"`recent_changes_truncated=true`",
		"`no_recent_changes.window_seconds`",
		"`correlated_changes`",
		"`diagnostic_context.role`",
		"`related_issues[].count`",
	} {
		if !strings.Contains(issuesTool.Description, token) {
			t.Errorf("issues tool description lost response contract %q", token)
		}
	}
	for _, boundary := range []string{
		"not healthy",
		"linkage was not evaluated",
		"not evidence of cause",
		"does not strengthen causality",
		"absence from the feed is not evidence",
		"evidence against, not disproof",
		"external dependencies",
		"correlation is unknown",
		"affected subset",
		"not the linked issue total",
	} {
		if !strings.Contains(issuesTool.Description, boundary) {
			t.Errorf("issues tool description lost interpretation boundary %q", boundary)
		}
	}

	schema, err := json.Marshal(issuesTool.InputSchema)
	if err != nil {
		t.Fatalf("marshal issues input schema: %v", err)
	}
	for _, want := range []string{
		"timing_summary",
		"plain-language",
		"raw timing fields",
		"filtering",
		"resource age",
		"issue age",
	} {
		if !strings.Contains(issuesTool.Description, want) {
			t.Errorf("issues description lost self-explanatory timing contract %q", want)
		}
	}
	for _, want := range []string{
		"started_at_resource_creation",
		"started_after_resource_was_healthy",
		"timing evidence",
		"root-cause verdict",
	} {
		if !strings.Contains(string(schema), want) {
			t.Errorf("issues input schema lost issue_timing filter contract %q", want)
		}
	}
}

func TestTrimmedToolsPreserveLoadBearingSteers(t *testing.T) {
	descriptions := map[string]string{}
	for _, tool := range listRegisteredTools(t) {
		descriptions[tool.Name] = tool.Description
	}

	for _, want := range []string{
		"CrashLoopBackOff",
		"OOMKilled",
		"image-pull",
		"readiness",
		"scheduling",
		"one round-trip",
		"`crashCause`",
		"`" + crashLineFatalPattern + "`",
		"`" + crashLineHeaderOnly + "`",
		"`" + crashLineLastMatchedLine + "`",
		"`" + crashLineLogTail + "`",
		"`auditSummary.highestSeverity`",
		"critical|high|medium|low",
		"`issueSummary` critical|warning",
	} {
		if !strings.Contains(descriptions["diagnose"], want) {
			t.Errorf("diagnose tool description lost interpretation boundary %q", want)
		}
	}
	for _, boundary := range []string{
		"not a root-cause verdict",
		"capped sample",
		"exhaustive set",
		"low-confidence",
	} {
		if !strings.Contains(descriptions["diagnose"], boundary) {
			t.Errorf("diagnose tool description lost evidence boundary %q", boundary)
		}
	}

	for _, want := range []string{
		"`sourcesErrored`",
		"results are partial",
		"missing packages",
		"not installed",
		"not source errors",
		"manually merging list_helm_releases and list_resources",
		"Helm-only deployment debugging",
	} {
		if !strings.Contains(descriptions["list_packages"], want) {
			t.Errorf("list_packages tool description lost partial-result boundary %q", want)
		}
	}

	for _, want := range []string{
		"Supply both verb and resource",
		"`usedByPods`",
		"For ServiceAccount subjects",
		"`system:authenticated`",
		"`system:serviceaccounts`",
		"`inheritedFromGroup`",
		"`flatRules`",
		"without provenance",
		"User and Group subjects",
		"bindings that name them directly",
		"empty string for cluster-scoped or cluster-wide access",
		"create SubjectAccessReviews",
	} {
		if !strings.Contains(descriptions["get_subject_permissions"], want) {
			t.Errorf("get_subject_permissions tool description lost authorization boundary %q", want)
		}
	}
	for _, boundary := range []string{"best-effort", "absence is not proof", "never retries"} {
		if !strings.Contains(descriptions["get_subject_permissions"], boundary) {
			t.Errorf("get_subject_permissions tool description lost uncertainty boundary %q", boundary)
		}
	}

	for _, want := range []string{
		"Helm storage namespace",
		"`storageNamespace`",
		"release namespace",
		"key-aware redacted",
	} {
		if !strings.Contains(descriptions["get_helm_release"], want) {
			t.Errorf("get_helm_release tool description lost call or secrecy boundary %q", want)
		}
	}
}

func TestToolCatalogContextBudget(t *testing.T) {
	// These caps guard against description accretion, not against new tools or
	// load-bearing routing and uncertainty contracts. Raise them deliberately.
	const (
		maxCatalogBytes         = 53500
		maxToolDescriptionBytes = 3000
	)

	total := 0
	for _, tool := range listRegisteredTools(t) {
		if len(tool.Description) > maxToolDescriptionBytes {
			t.Errorf("%s description uses %d bytes, per-tool budget is %d; trim field-by-field manuals but preserve routing, uncertainty, and response fields that need interpretation",
				tool.Name, len(tool.Description), maxToolDescriptionBytes)
		}
		schema, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("marshal %s input schema: %v", tool.Name, err)
		}
		total += len(tool.Description) + len(schema)
	}
	if total > maxCatalogBytes {
		t.Fatalf("MCP tool descriptions and schemas use %d bytes, budget is %d; raise deliberately for new tools or load-bearing contracts, and trim response-field manuals rather than routing or uncertainty boundaries",
			total, maxCatalogBytes)
	}
}

func TestToolDescriptionsPreserveCrossSurfaceRouting(t *testing.T) {
	descriptions := map[string]string{}
	for _, tool := range listRegisteredTools(t) {
		descriptions[tool.Name] = tool.Description
	}

	for _, want := range []string{
		"`group=" + issues.NativeHelmGroup + "` routes to get_helm_release",
		"`group=helm.toolkit.fluxcd.io` routes to diagnose",
		"`correlated_changes`",
		"tracked edits on the issue subject",
		"directly referenced ConfigMaps",
	} {
		if !strings.Contains(descriptions["issues"], want) {
			t.Errorf("issues tool description lost routing or evidence scope %q", want)
		}
	}
	for _, want := range []string{
		"Kyverno PolicyReport",
		"not included",
		"resourceContext",
	} {
		if !strings.Contains(descriptions["get_cluster_audit"], want) {
			t.Errorf("get_cluster_audit tool description lost policy-report boundary %q", want)
		}
	}
}

func TestToolsEmittingApplicationConfigurationMarkerDocumentRankingBoundary(t *testing.T) {
	descriptions := map[string]string{}
	for _, tool := range listRegisteredTools(t) {
		switch tool.Name {
		case "get_changes", "get_resource", "diagnose", "issues":
			descriptions[tool.Name] = tool.Description
		}
	}
	for _, name := range []string{"get_changes", "get_resource", "diagnose", "issues"} {
		description := descriptions[name]
		if description == "" {
			t.Fatalf("%s tool is not registered or has no description", name)
		}
		for _, want := range []string{
			"application_configuration_change",
			"not a causal or universal relevance verdict",
		} {
			if !strings.Contains(description, want) {
				t.Errorf("%s tool description does not document %q", name, want)
			}
		}
	}
	if !strings.Contains(descriptions["get_changes"], "Helm operation entries rank by category and recency") {
		t.Error("get_changes tool description does not document Helm ranking")
	}
}

func TestClusterAuditToolAndSetupCatalogUseCanonicalSeverity(t *testing.T) {
	var auditTool *mcpsdk.Tool
	for _, tool := range listRegisteredTools(t) {
		if tool.Name == "get_cluster_audit" {
			auditTool = tool
			break
		}
	}
	if auditTool == nil {
		t.Fatal("get_cluster_audit tool is not registered")
	}
	schema, err := json.Marshal(auditTool.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	for label, text := range map[string]string{
		"tool description": auditTool.Description,
		"input schema":     string(schema),
	} {
		if !strings.Contains(text, "critical") || !strings.Contains(text, "high") ||
			!strings.Contains(text, "medium") || !strings.Contains(text, "low") {
			t.Errorf("%s does not document the canonical severity ladder: %s", label, text)
		}
		if strings.Contains(text, "danger-severity") || strings.Contains(text, "danger or warning") {
			t.Errorf("%s exposes raw audit severity vocabulary: %s", label, text)
		}
	}

	catalog, err := os.ReadFile(setupDialogCatalogPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "posture priority: critical, high, medium, or low (built-ins use high or medium)"
	if !strings.Contains(string(catalog), want) {
		t.Fatalf("setup dialog catalog does not contain exact audit severity copy %q", want)
	}
}

func TestSearchToolSchemaIncludesNamespace(t *testing.T) {
	var searchTool *mcpsdk.Tool
	for _, tool := range listRegisteredTools(t) {
		if tool.Name == "search" {
			searchTool = tool
			break
		}
	}
	if searchTool == nil {
		t.Fatal("search tool is not registered")
	}

	raw, err := json.Marshal(searchTool.InputSchema)
	if err != nil {
		t.Fatalf("marshal search input schema: %v", err)
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("unmarshal search input schema: %v", err)
	}
	if _, ok := schema.Properties["namespace"]; !ok {
		t.Fatalf("search input schema does not accept namespace: %s", raw)
	}
}

func TestRegisteredToolAnnotations(t *testing.T) {
	tools := listRegisteredTools(t)
	writeTools := map[string]bool{
		"manage_workload": true,
		"manage_rollout":  true,
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
		// diagnose is read-only EXCEPT its optional in_cluster=true arg, which creates
		// ONE transient, self-destructing probe pod - so it is non-read-only but NOT
		// destructive (additive + self-deleting). It is the only such tool.
		if tool.Name == "diagnose" {
			if tool.Annotations.ReadOnlyHint {
				t.Errorf("diagnose must NOT set readOnlyHint=true - in_cluster=true creates a transient pod")
			}
			if tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint {
				t.Errorf("diagnose should set destructiveHint=false (the probe pod self-deletes; nothing existing is mutated)")
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
	"manage_workload", "manage_rollout", "manage_cronjob", "manage_gitops",
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
