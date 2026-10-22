package cnpg

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestServiceDependencyBoundary(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, entry.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		imports := make(map[string]string)
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			name := path[strings.LastIndex(path, "/")+1:]
			if spec.Name != nil {
				name = spec.Name.Name
			}
			imports[name] = path
			if path == "github.com/skyhook-io/radar/internal/server" || strings.HasPrefix(path, "github.com/go-chi/chi") {
				t.Errorf("%s: service imports browser routing package %s", fset.Position(spec.Pos()), path)
			}
			if name == "." && path == "github.com/skyhook-io/radar/internal/k8s" {
				t.Errorf("%s: a dot import hides singleton access", fset.Position(spec.Pos()))
			}
		}
		checkSignature := func(signature ast.Node) {
			ast.Inspect(signature, func(node ast.Node) bool {
				selector, ok := node.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				owner, ok := selector.X.(*ast.Ident)
				if ok && imports[owner.Name] == "net/http" && (selector.Sel.Name == "Request" || selector.Sel.Name == "ResponseWriter") {
					t.Errorf("%s: service operation or port exposes browser HTTP type %s", fset.Position(selector.Pos()), selector.Sel.Name)
				}
				return true
			})
		}
		for _, declaration := range file.Decls {
			switch declaration := declaration.(type) {
			case *ast.FuncDecl:
				if declaration.Name.IsExported() {
					checkSignature(declaration.Type)
				}
			case *ast.GenDecl:
				for _, spec := range declaration.Specs {
					if typ, ok := spec.(*ast.TypeSpec); ok && typ.Name.IsExported() {
						checkSignature(typ.Type)
					}
				}
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			owner, ok := selector.X.(*ast.Ident)
			if !ok {
				return true
			}
			if imports[owner.Name] == "github.com/skyhook-io/radar/internal/prometheus" && selector.Sel.Name == "GetClient" {
				t.Errorf("%s: service resolves global metrics client; use caller-scoped dependencies", fset.Position(call.Pos()))
			}
			if imports[owner.Name] != "github.com/skyhook-io/radar/internal/k8s" {
				return true
			}
			name := selector.Sel.Name
			if (strings.HasPrefix(name, "Get") && name != "GetContainersForPod") || name == "IsConnected" || name == "SnapshotCaches" || name == "ClientFromContext" || name == "DynamicClientFromContext" || name == "ConfigFromContext" {
				t.Errorf("%s: service resolves global cluster state through %s; use caller-scoped dependencies", fset.Position(call.Pos()), name)
			}
			return true
		})
	}
}
