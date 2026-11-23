package k8s

import (
	"go/ast"
	"go/build"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func TestNoClusterGlobals(t *testing.T) {
	denied := []string{
		"*k8s.io/client-go/kubernetes.Clientset", "k8s.io/client-go/kubernetes.Interface",
		"*k8s.io/client-go/rest.Config", "k8s.io/client-go/dynamic.Interface",
		"*k8s.io/client-go/discovery.DiscoveryClient", "k8s.io/client-go/discovery.DiscoveryInterface",
		"*sharedKubernetesClients", "*ResourceDiscovery",
	}
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, name := range pkg.GoFiles {
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}

	// Resolve constructor results (including multi-result initializers) and
	// import aliases using the same dependency versions the test was built with.
	out, err := exec.Command("go", "list", "-deps", "-export", "-f", "{{.ImportPath}} {{.Export}}", ".").Output()
	if err != nil {
		t.Fatal(err)
	}
	exports := map[string]string{}
	for line := range strings.SplitSeq(string(out), "\n") {
		path, archive, ok := strings.Cut(line, " ")
		if ok {
			exports[path] = archive
		}
	}
	conf := types.Config{IgnoreFuncBodies: true, Importer: importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
		return os.Open(exports[path])
	})}
	checked, err := conf.Check("github.com/skyhook-io/radar/internal/k8s", fset, files, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range checked.Scope().Names() {
		v, ok := checked.Scope().Lookup(name).(*types.Var)
		if !ok {
			continue
		}
		typ := types.Unalias(v.Type())
		typeName := types.TypeString(typ, func(p *types.Package) string {
			if p == checked {
				return ""
			}
			return p.Path()
		})
		// Also through atomic.Pointer[T], which holds a *T.
		wrapped := slices.ContainsFunc(denied, func(d string) bool {
			return strings.HasSuffix(typeName, "["+strings.TrimPrefix(d, "*")+"]")
		})
		if slices.Contains(denied, typeName) || wrapped {
			t.Errorf("%s: %s has type %s: cluster state belongs on *Connection", fset.Position(v.Pos()), name, typeName)
		}
	}
}
