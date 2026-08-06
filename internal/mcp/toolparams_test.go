package mcp

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerToolsOnce populates toolParamNames/toolRequired the same way a real
// server start does, so these tests exercise the registry that ships rather
// than a hand-written fixture.
func registerToolsOnce(t *testing.T) {
	t.Helper()
	if len(toolParamNames) > 0 {
		return
	}
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "radar", Version: "test"}, nil)
	registerTools(server, true)
}

func TestSubjectPermissionsAliasRepairsTheBenchmarkFailure(t *testing.T) {
	registerToolsOnce(t)

	// The exact arguments an agent sent in the SREGym benchmark. They were
	// rejected outright, and the agent then reported "no RBAC RoleBindings at
	// all" for a ServiceAccount that had one.
	in := json.RawMessage(`{"namespace":"hotel-reservation","subject":"cleanup-controller","subjectKind":"ServiceAccount"}`)

	fixed, repairs := repairToolArgs("get_subject_permissions", in)
	if len(repairs) == 0 {
		t.Fatal("expected the subject/subjectKind call to be repaired; it was left unchanged")
	}

	var got map[string]string
	if err := json.Unmarshal(fixed, &got); err != nil {
		t.Fatalf("repaired args are not valid JSON: %v", err)
	}
	want := map[string]string{
		"namespace": "hotel-reservation",
		"name":      "cleanup-controller",
		"kind":      "ServiceAccount",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("arg %q = %q, want %q (full: %s)", k, got[k], v, fixed)
		}
	}
	for _, gone := range []string{"subject", "subjectKind"} {
		if _, still := got[gone]; still {
			t.Errorf("alias %q survived repair: %s", gone, fixed)
		}
	}
}

func TestOrthographicRepairCrossesTheSnakeCamelSplit(t *testing.T) {
	registerToolsOnce(t)

	// radar is snake_case nearly everywhere but manage_gitops uses camelCase.
	// An agent that learned dry_run from apply_resource must not be rejected by
	// manage_gitops for the same concept, and vice versa.
	cases := []struct{ tool, supplied, want string }{
		{"manage_gitops", "dry_run", "dryRun"},
		{"apply_resource", "dryRun", "dry_run"},
		{"patch_resource", "dryRun", "dry_run"},
	}
	for _, tc := range cases {
		raw := json.RawMessage(`{"` + tc.supplied + `":true}`)
		fixed, repairs := repairToolArgs(tc.tool, raw)
		if len(repairs) == 0 {
			t.Errorf("%s: %q was not repaired to %q", tc.tool, tc.supplied, tc.want)
			continue
		}
		var got map[string]any
		if err := json.Unmarshal(fixed, &got); err != nil {
			t.Fatalf("%s: bad JSON after repair: %v", tc.tool, err)
		}
		if _, ok := got[tc.want]; !ok {
			t.Errorf("%s: expected key %q after repair, got %s", tc.tool, tc.want, fixed)
		}
	}
}

func TestRepairNeverInventsOrClobbers(t *testing.T) {
	registerToolsOnce(t)

	t.Run("unknown name with no accepted target is left for the validator", func(t *testing.T) {
		raw := json.RawMessage(`{"kind":"ServiceAccount","name":"x","totally_made_up":1}`)
		fixed, repairs := repairToolArgs("get_subject_permissions", raw)
		if len(repairs) != 0 {
			t.Fatalf("unexpected repair %v", repairs)
		}
		if string(fixed) != string(raw) {
			t.Errorf("args were rewritten: %s", fixed)
		}
	})

	t.Run("an alias never overwrites a correctly-named value", func(t *testing.T) {
		// Both spellings supplied: the canonical one must win untouched.
		raw := json.RawMessage(`{"kind":"ServiceAccount","name":"real","subject":"decoy"}`)
		fixed, _ := repairToolArgs("get_subject_permissions", raw)
		var got map[string]string
		if err := json.Unmarshal(fixed, &got); err != nil {
			t.Fatal(err)
		}
		if got["name"] != "real" {
			t.Errorf("name = %q, want %q — alias clobbered a supplied value", got["name"], "real")
		}
	})

	t.Run("non-object arguments are passed through", func(t *testing.T) {
		raw := json.RawMessage(`["not","an","object"]`)
		fixed, repairs := repairToolArgs("get_subject_permissions", raw)
		if len(repairs) != 0 || string(fixed) != string(raw) {
			t.Errorf("array args should be untouched, got %s %v", fixed, repairs)
		}
	})

	t.Run("unregistered tool is passed through", func(t *testing.T) {
		raw := json.RawMessage(`{"subject":"x"}`)
		fixed, repairs := repairToolArgs("no_such_tool", raw)
		if len(repairs) != 0 || string(fixed) != string(raw) {
			t.Errorf("unknown tool should be untouched, got %s %v", fixed, repairs)
		}
	})
}

func TestSchemaErrorNamesTheAcceptedArguments(t *testing.T) {
	registerToolsOnce(t)

	help := describeToolParams("get_subject_permissions")
	if help == "" {
		t.Fatal("no parameter help produced for get_subject_permissions")
	}
	for _, want := range []string{"kind", "name", "required", "verb", "resource"} {
		if !strings.Contains(help, want) {
			t.Errorf("parameter help missing %q: %s", want, help)
		}
	}

	// The annotation must attach to a schema-validation failure...
	res := &mcpsdk.CallToolResult{
		IsError: true,
		Content: []mcpsdk.Content{&mcpsdk.TextContent{
			Text: `validating "arguments": validating root: unexpected additional properties ["subject"]`,
		}},
	}
	annotateSchemaError("get_subject_permissions", res)
	if txt := res.Content[0].(*mcpsdk.TextContent).Text; !strings.Contains(txt, "accepts:") {
		t.Errorf("validation error was not annotated: %s", txt)
	}

	// ...and must NOT rewrite a tool's own domain error.
	domain := &mcpsdk.CallToolResult{
		IsError: true,
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "ServiceAccount requires a namespace"}},
	}
	annotateSchemaError("get_subject_permissions", domain)
	if txt := domain.Content[0].(*mcpsdk.TextContent).Text; strings.Contains(txt, "accepts:") {
		t.Errorf("domain error should not be annotated: %s", txt)
	}
}

// TestEveryToolRegistersItsParameters guards the registry itself: a tool added
// with mcpsdk.AddTool instead of addTool would silently lose both repair and
// error help, which is invisible until an agent guesses a name wrong.
func TestEveryToolRegistersItsParameters(t *testing.T) {
	registerToolsOnce(t)

	if len(toolParamNames) < 20 {
		t.Fatalf("only %d tools registered parameters; did a tool skip addTool()?", len(toolParamNames))
	}
	for name, params := range toolParamNames {
		if len(params) == 0 {
			continue // genuinely argument-free tools are fine
		}
		if describeToolParams(name) == "" {
			t.Errorf("tool %q has params %v but produces no parameter help", name, params)
		}
	}
}

// TestParameterNamingStaysConsistent stops the footgun class from growing.
//
// radar is snake_case nearly everywhere (dry_run, tail_lines, resource_namespace).
// manage_gitops is the lone camelCase holdout, and that split is exactly what
// makes an agent that learned `dry_run` from apply_resource get hard-rejected by
// manage_gitops. Repair papers over the existing cases; this keeps new tools from
// adding more.
//
// If this fails on a tool you just added: rename the parameter to snake_case.
// Only extend the grandfathered list to keep a published schema stable.
func TestParameterNamingStaysConsistent(t *testing.T) {
	registerToolsOnce(t)

	grandfathered := map[string][]string{
		"manage_gitops": {"dryRun", "applyOnly", "syncOptions", "historyId"},
	}

	for tool, params := range toolParamNames {
		for _, p := range params {
			if p != toSnake(p) { // contains an uppercase letter
				if slices.Contains(grandfathered[tool], p) {
					continue
				}
				t.Errorf("%s.%s is camelCase; radar parameters are snake_case (want %q). "+
					"Mixed spellings across tools cause hard schema rejections for agents.",
					tool, p, toSnake(p))
			}
		}
	}
}

// TestAliasTargetsExist keeps perToolAliases honest: an alias pointing at a
// parameter that no longer exists silently stops working.
func TestAliasTargetsExist(t *testing.T) {
	registerToolsOnce(t)

	for tool, aliases := range perToolAliases {
		accepted, ok := toolParamNames[tool]
		if !ok {
			t.Errorf("perToolAliases references unknown tool %q", tool)
			continue
		}
		for alias, target := range aliases {
			if !slices.Contains(accepted, target) {
				t.Errorf("%s: alias %q -> %q, but %q is not a parameter (have %v)", tool, alias, target, target, accepted)
			}
			if slices.Contains(accepted, alias) {
				t.Errorf("%s: alias %q shadows a real parameter of the same name", tool, alias)
			}
		}
	}
}
