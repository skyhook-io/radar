package resourcecontext

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"strconv"
	"testing"
)

// Every field Build clips must be a preview field, or MCP would cut it without
// saying so. The check reads the field argument of each filterBoundedRefs and
// selectRefs call in the package's non-test sources.
func TestRelationshipPreviewFieldsCoverEveryCappedField(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi fs.FileInfo) bool {
		name := fi.Name()
		return len(name) < 8 || name[len(name)-8:] != "_test.go"
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	capped := map[string]bool{}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				var name string
				switch fn := call.Fun.(type) {
				case *ast.Ident:
					name = fn.Name
				case *ast.SelectorExpr:
					name = fn.Sel.Name
				}
				// Both take the field as their fourth argument.
				const arg = 3
				if name != "filterBoundedRefs" && name != "selectRefs" || len(call.Args) <= arg {
					return true
				}
				lit, ok := call.Args[arg].(*ast.BasicLit)
				if !ok {
					// A forwarded parameter, as in filterBoundedRefs itself.
					return true
				}
				field, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("%s: %v", fset.Position(lit.Pos()), err)
				}
				capped[field] = true
				return true
			})
		}
	}
	if len(capped) == 0 {
		t.Fatal("found no capped fields; the call shapes changed")
	}
	for field := range capped {
		if !IsRelationshipPreviewField(field) {
			t.Errorf("%s is clipped but not in relationshipPreviewFields", field)
		}
	}
}
