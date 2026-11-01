package mcp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// nonIntegrationWriteTools act on built-in kinds or edit arbitrary objects
// with whatever the caller's roles allow; they are not integration actions.
var nonIntegrationWriteTools = []string{"manage_cronjob", "manage_node", "apply_resource", "patch_resource", "delete_resource"}

// TestIntegrationWriteToolActionsHaveReviewedRows makes a new MCP write tool,
// or a new action on an integration tool, fail until it is classified here or
// listed in a row's actions in files/integration-write-baseline.yaml. It
// checks the action inventory only, not each row's resources and verbs.
func TestIntegrationWriteToolActionsHaveReviewedRows(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "deploy", "helm", "radar", "files", "integration-write-baseline.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var policy struct {
		Entries []struct {
			Actions []string `json:"actions"`
		} `json:"entries"`
	}
	if err := yaml.Unmarshal(raw, &policy); err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, entry := range policy.Entries {
		for _, action := range entry.Actions {
			if strings.HasPrefix(action, "MCP ") {
				listed[action] = true
			}
		}
	}

	readOnly := map[string]bool{}
	for _, tool := range listRegisteredToolsWith(t, false) {
		readOnly[tool.Name] = true
	}
	var required []string
	for _, tool := range listRegisteredToolsWith(t, true) {
		if readOnly[tool.Name] {
			continue
		}
		switch tool.Name {
		case "manage_gitops":
			fn := parseHandler(t, "tools_gitops.go", "handleManageGitOps")
			for gitopsTool, clause := range stringCases(t, findSwitch(t, fn.Body, "tool")) {
				for action := range stringCases(t, findSwitch(t, clause, "action")) {
					required = append(required, "MCP manage_gitops tool="+gitopsTool+" action="+action)
				}
			}
		case "manage_rollout":
			for action := range rolloutActions {
				required = append(required, "MCP manage_rollout action="+action)
			}
		case "manage_workload":
			if normalizeWorkloadKind("rollout") != "rollouts" {
				t.Fatal("manage_workload no longer accepts kind=rollout; update the table and this test")
			}
			fn := parseHandler(t, "tools_workloads.go", "handleManageWorkload")
			for action := range stringCases(t, findSwitch(t, fn.Body, "strings.ToLower(input.Action)")) {
				required = append(required, "MCP manage_workload kind=rollout action="+action)
			}
		default:
			if !slices.Contains(nonIntegrationWriteTools, tool.Name) {
				t.Errorf("write tool %q is unclassified: list its integration actions in files/integration-write-baseline.yaml or add it to nonIntegrationWriteTools", tool.Name)
			}
		}
	}
	if len(required) < 15 {
		t.Fatalf("found only %d integration tool actions; the enumeration is broken", len(required))
	}
	for _, action := range required {
		if !listed[action] {
			t.Errorf("MCP action %q has no row in files/integration-write-baseline.yaml", action)
		}
		delete(listed, action)
	}
	for action := range listed {
		t.Errorf("files/integration-write-baseline.yaml lists %q, which is not a registered MCP tool action", action)
	}
}

func parseHandler(t *testing.T, file, name string) *ast.FuncDecl {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range parsed.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == name {
			return fn
		}
	}
	t.Fatalf("%s not found in %s", name, file)
	return nil
}

// findSwitch returns the first switch under node whose tag reads as tag.
func findSwitch(t *testing.T, node ast.Node, tag string) *ast.SwitchStmt {
	t.Helper()
	var found *ast.SwitchStmt
	ast.Inspect(node, func(n ast.Node) bool {
		if sw, ok := n.(*ast.SwitchStmt); ok && found == nil && sw.Tag != nil && types.ExprString(sw.Tag) == tag {
			found = sw
		}
		return found == nil
	})
	if found == nil {
		t.Fatalf("no `switch %s` found; teach this test how the handler dispatches actions", tag)
	}
	return found
}

func stringCases(t *testing.T, sw *ast.SwitchStmt) map[string]*ast.CaseClause {
	t.Helper()
	cases := map[string]*ast.CaseClause{}
	for _, stmt := range sw.Body.List {
		clause := stmt.(*ast.CaseClause)
		for _, expr := range clause.List {
			lit, ok := expr.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				t.Fatalf("case %s is not a string literal", types.ExprString(expr))
			}
			value, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatal(err)
			}
			cases[value] = clause
		}
	}
	return cases
}
