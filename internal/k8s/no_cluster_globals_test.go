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
		"*sharedKubernetesClients", "*ResourceDiscovery", "*permissionState",
		"DynamicResourceCache", "*DynamicResourceCache", "connectionDynamicCache", "*connectionDynamicCache",
		"github.com/skyhook-io/radar/pkg/k8score.DynamicResourceCache", "*github.com/skyhook-io/radar/pkg/k8score.DynamicResourceCache",
		"*github.com/skyhook-io/radar/pkg/k8score.MetricsHistoryStore", "github.com/skyhook-io/radar/pkg/k8score.MetricsHistoryStore",
		"*ResourceCache", "connectionCache", "*connectionCache", "recreateStash", "*recreateStash", "recreateEntry", "*recreateEntry", "map[string]recreateEntry",
		"*github.com/skyhook-io/radar/pkg/timeline.TombstoneCache", "github.com/skyhook-io/radar/pkg/timeline.TombstoneCache",
		"connectionPolicyReports", "*connectionPolicyReports", "policyReportSnapshot", "*policyReportSnapshot", "PolicyReportStatus", "*PolicyReportStatus", "KyvernoStatus", "*KyvernoStatus",
		"github.com/skyhook-io/radar/pkg/policyreports.Index", "*github.com/skyhook-io/radar/pkg/policyreports.Index",
		"*PermissionCheckResult", "*Capabilities", "userCapEntry", "*userCapEntry", "userNSCapEntry", "*userNSCapEntry",
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
	conf := types.Config{Importer: importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
		return os.Open(exports[path])
	})}
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}}
	checked, err := conf.Check("github.com/skyhook-io/radar/internal/k8s", fset, files, info)
	if err != nil {
		t.Fatal(err)
	}
	checkConnectionMethodGetters(t, fset, files, checked, info)
	for _, name := range checked.Scope().Names() {
		v, ok := checked.Scope().Lookup(name).(*types.Var)
		if !ok {
			continue
		}
		typ := types.Unalias(v.Type())
		// A package may re-export the pointee (internal/timeline does).
		// Resolve it inside both *T and atomic.Pointer[T].
		if named, ok := typ.(*types.Named); ok && named.Obj().Pkg() != nil &&
			named.Obj().Pkg().Path() == "sync/atomic" && named.Obj().Name() == "Pointer" {
			typ = types.NewPointer(named.TypeArgs().At(0))
		}
		if ptr, ok := typ.(*types.Pointer); ok {
			typ = types.NewPointer(types.Unalias(ptr.Elem()))
		}
		typeName := types.TypeString(typ, func(p *types.Package) string {
			if p == checked {
				return ""
			}
			return p.Path()
		})
		// Reject other generic containers of the denied types as well.
		wrapped := slices.ContainsFunc(denied, func(d string) bool {
			return strings.HasSuffix(typeName, "["+strings.TrimPrefix(d, "*")+"]")
		})
		_, isMap := typ.Underlying().(*types.Map)
		cacheType := isMap || typeName == "sync.Map" || typeName == "golang.org/x/sync/singleflight.Group" || typeName == "*k8s.io/apimachinery/pkg/util/cache.Expiring"
		lowerName := strings.ToLower(name)
		cacheName := strings.Contains(lowerName, "capabilit") || strings.Contains(lowerName, "perm") || strings.Contains(lowerName, "nodepoollist")
		policyState := strings.Contains(lowerName, "kyverno") || strings.Contains(lowerName, "policyreport") || strings.Contains(lowerName, "warmup")
		policyCounter := policyState && (strings.HasSuffix(lowerName, "gen") || strings.HasSuffix(lowerName, "generation"))
		if policyCounter || strings.Contains(lowerName, "cachegeneration") || strings.Contains(lowerName, "cacheonce") || slices.Contains(denied, typeName) || wrapped || (cacheName && cacheType) {
			t.Errorf("%s: %s has type %s: cluster state belongs on *Connection", fset.Position(v.Pos()), name, typeName)
		}
	}
}

func checkConnectionMethodGetters(t *testing.T, fset *token.FileSet, files []*ast.File, pkg *types.Package, info *types.Info) {
	t.Helper()
	scope := pkg.Scope()
	current := scope.Lookup("Current")
	referencedObject := func(id *ast.Ident) types.Object {
		obj := info.Uses[id]
		if fn, ok := obj.(*types.Func); ok {
			return fn.Origin()
		}
		return obj
	}
	// References include taking a function or method as a value, even inside a closure.
	referrers := map[types.Object][]types.Object{}
	recordReferences := func(owner types.Object, body ast.Node) {
		for node := range ast.Preorder(body) {
			if id, ok := node.(*ast.Ident); ok {
				if obj := referencedObject(id); obj != nil && obj.Pkg() == pkg {
					referrers[obj] = append(referrers[obj], owner)
				}
			}
		}
	}

	connPtr := types.NewPointer(scope.Lookup("Connection").Type())
	var methods []*ast.FuncDecl
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			obj := info.Defs[fn.Name].(*types.Func)
			if recv := obj.Signature().Recv(); recv != nil && types.Identical(recv.Type(), connPtr) {
				methods = append(methods, fn)
				continue
			}
			recordReferences(obj, fn.Body)
		}
	}
	for _, init := range info.InitOrder {
		for _, v := range init.Lhs {
			if _, ok := v.Type().Underlying().(*types.Signature); ok {
				recordReferences(v, init.Rhs)
			}
		}
	}

	var pending []types.Object
	for _, name := range []string{"Current", "installedConn", "BoundOrCurrent"} {
		obj := scope.Lookup(name)
		if obj == nil {
			t.Fatalf("unknown installed-connection source %s", name)
		}
		pending = append(pending, obj)
	}
	denied := map[types.Object]bool{}
	for len(pending) > 0 {
		obj := pending[0]
		pending = pending[1:]
		if !denied[obj] {
			denied[obj] = true
			pending = append(pending, referrers[obj]...)
		}
	}

	for _, fn := range methods {
		receiver := info.Defs[fn.Name].(*types.Func).Signature().Recv()
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.BinaryExpr:
				if n.Op != token.EQL || !slices.Contains([]string{
					"cacheIsCurrent",           // Publication requires the cache's owner to still be installed.
					"accessibleNamespaces",     // A superseded namespace LIST must still fall back to its seeds.
					"permissionStateIsCurrent", // Permission checks report whether their result is still current.
				}, fn.Name.Name) {
					return true
				}
				left, right := n.X, n.Y
				if _, ok := right.(*ast.CallExpr); ok {
					left, right = right, left
				}
				call, ok := left.(*ast.CallExpr)
				recv, _ := right.(*ast.Ident)
				if ok && info.Uses[recv] == receiver {
					callee, _ := call.Fun.(*ast.Ident)
					return info.Uses[callee] != current
				}
			case *ast.Ident:
				if obj := referencedObject(n); denied[obj] {
					t.Errorf("%s: (*Connection).%s uses %s: use the connection's own state, not the installed connection's", fset.Position(n.Pos()), fn.Name, obj.Name())
				}
			}
			return true
		})
	}
}
