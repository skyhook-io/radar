package server

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// file → functions allowed to read a listenAddress field, and why.
var postureListenAddressReaders = map[string]map[string]string{
	"posture.go":     {"posture": "the posture facts", "configManagement": "input to the exported classifier internal/app also calls"},
	"server.go":      {"StartWithReady": "normalizes and binds the address", "ActualAddr": "dialable address for local clients"},
	"startup_log.go": {"logStartupSummaryBlock": "prints the address", "formatStartupLogSummary": "prints the summary's copy"},
}

// internal/cloud function → files allowed to call it.
var postureRestrictedCloudCalls = map[string]map[string]bool{
	"IsAuthenticatedTunnelRequest": {"posture.go": true},
	"IsLoopbackHostname":           {"posture.go": true, "listen_address.go": true}, // listen_address.go: request hostnames, not the listener
}

// Package helpers that expose a posture fact → files allowed to call them. Others
// call the named decisions instead of rebuilding one from the facts.
var posturePackageCalls = map[string]map[string]bool{
	"posture":               {"posture.go": true},                         // the Server method
	"viaTunnel":             {"posture.go": true, "cross_site.go": true},  // cross_site.go: the tunnel's own origin boundary
	"exposureOf":            {"posture.go": true, "startup_log.go": true}, // startup_log.go: which URL to print
	"requestHostIsLoopback": {"posture.go": true, "listen_address.go": true, "cross_site.go": true},
}

// posturePrimitiveUses reports reads of a listenAddress field and calls to the
// restricted posture helpers and internal/cloud functions outside their
// allowlists, whatever the receiver or import alias is called.
func posturePrimitiveUses(fset *token.FileSet, path string, f *ast.File) []string {
	var out []string
	cloudNames := map[string]bool{}
	for _, imp := range f.Imports {
		if imp.Path.Value != `"github.com/skyhook-io/radar/internal/cloud"` {
			continue
		}
		name := "cloud"
		if imp.Name != nil {
			name = imp.Name.Name
		}
		cloudNames[name] = true
	}
	// Each function body, and each package-level initializer under its
	// variable's name (no variable is an allowed reader).
	type scope struct {
		name string
		node ast.Node
	}
	var scopes []scope
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Body != nil {
				scopes = append(scopes, scope{d.Name.Name, d.Body})
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				if vs, ok := spec.(*ast.ValueSpec); ok && len(vs.Values) > 0 {
					scopes = append(scopes, scope{vs.Names[0].Name, vs})
				}
			}
		}
	}
	for _, sc := range scopes {
		ast.Inspect(sc.node, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.SelectorExpr:
				if n.Sel.Name == "listenAddress" {
					if _, ok := postureListenAddressReaders[path][sc.name]; !ok {
						out = append(out, fmt.Sprintf("%s: %s reads a listenAddress field; decide in posture.go (exposureOf / posture) instead", fset.Position(n.Pos()), sc.name))
					}
				}
			case *ast.CallExpr:
				var name string
				switch fn := n.Fun.(type) {
				case *ast.Ident:
					name = fn.Name
				case *ast.SelectorExpr:
					name = fn.Sel.Name
				}
				if allowed, restricted := posturePackageCalls[name]; restricted && !allowed[path] {
					out = append(out, fmt.Sprintf("%s: %s calls %s; call a named decision in posture.go instead", fset.Position(n.Pos()), sc.name, name))
				}
				sel, ok := n.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok || !cloudNames[pkg.Name] {
					return true
				}
				if allowed, restricted := postureRestrictedCloudCalls[sel.Sel.Name]; restricted && !allowed[path] {
					out = append(out, fmt.Sprintf("%s: %s calls cloud.%s; use posture.go (viaTunnel / exposureOf) instead", fset.Position(n.Pos()), sc.name, sel.Sel.Name))
				}
			}
			return true
		})
	}
	return out
}

// TestPostureFactsReadOnlyHere keeps deployment-posture decisions in
// posture.go: outside it, code in this package may not interpret the listen
// address or the tunnel marker itself. It cannot tell whether a new capability
// should have asked for a decision at all; the review checklist in CLAUDE.md
// covers that.
func TestPostureFactsReadOnlyHere(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, msg := range posturePrimitiveUses(fset, path, f) {
			t.Error(msg)
		}
	}
}

func TestPosturePrimitiveUsesCatchesRenames(t *testing.T) {
	src := `package server

import cl "github.com/skyhook-io/radar/internal/cloud"

func (srv *Server) probe(r *http.Request) bool {
	return srv.listenAddress == "0.0.0.0" || cl.IsAuthenticatedTunnelRequest(r.Context()) || cl.IsLoopbackHostname(srv.listenAddress)
}

var onSharedListener = func(srv *Server) bool {
	return !cl.IsLoopbackHostname(srv.listenAddress)
}

func (srv *Server) runScript(r *http.Request) bool {
	return !srv.posture().openToOthers() && !viaTunnel(r) && exposureOf("") == listenerLoopback
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "handlers.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := posturePrimitiveUses(fset, "handlers.go", f); len(got) != 9 {
		t.Fatalf("got %d findings, want 9 (listenAddress reads, aliased cloud calls, and a decision rebuilt from posture helpers, in methods and a package-level initializer):\n%s", len(got), strings.Join(got, "\n"))
	}
	if got := posturePrimitiveUses(fset, "posture.go", f); len(got) != 3 {
		t.Fatalf("in posture.go the cloud calls are allowed but neither probe nor onSharedListener reads the listener there; got %d findings, want 3:\n%s", len(got), strings.Join(got, "\n"))
	}
}
