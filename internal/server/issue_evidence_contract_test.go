package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUserIssueProjectionsSupplyEvidenceAuthorizer(t *testing.T) {
	for _, dir := range []string{".", "../mcp"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(file, func(node ast.Node) bool {
				literal, ok := node.(*ast.CompositeLit)
				if !ok {
					return true
				}
				selector, ok := literal.Type.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				owner, ok := selector.X.(*ast.Ident)
				if !ok || owner.Name != "issues" || (selector.Sel.Name != "Filters" && selector.Sel.Name != "RelatedIssueOptions") {
					return true
				}
				authorized := false
				for _, field := range literal.Elts {
					kv, ok := field.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, ok := kv.Key.(*ast.Ident)
					if !ok {
						continue
					}
					if key.Name == "CanReadEvidence" {
						authorized = true
					}
					if key.Name == "AllowUnfilteredEvidence" && entry.Name() == "capacity_issues.go" {
						authorized = true
					}
				}
				if !authorized {
					t.Errorf("%s: issue projection lacks an evidence authorizer", fset.Position(literal.Pos()))
				}
				return true
			})
		}
	}
}
