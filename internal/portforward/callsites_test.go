package portforward

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// Every place that can open a pods/portforward connection. A forward Radar
// opens on its own must honour AutoDisabled (--disable-auto-port-forward).
// When this test fails, decide which kind the new caller is, gate it if it is
// automatic, then add it here.
var portForwardCallSites = map[string]string{
	"pkg/portforward/portforward.go":        "the primitives themselves",
	"internal/portforward/portforward.go":   "automatic, gated in StartWithClients",
	"internal/argocd/discovery.go":          "automatic, gated in discover",
	"internal/runtimeevidence/collector.go": "explicit: evidence collection a user or agent asked for",
	"internal/server/portforward.go":        "explicit: the Port Forward feature",
}

var portForwardPrimitive = regexp.MustCompile(`RunPortForward\(|NewOnAddresses\(|SubResource\("portforward"\)|"k8s.io/client-go/tools/portforward"`)

func TestPortForwardCallSitesAreKnown(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == "node_modules" || name == ".git" || name == "web" || name == "packages" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if portForwardPrimitive.Match(src) {
			rel, _ := filepath.Rel(root, path)
			found = append(found, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range found {
		if _, ok := portForwardCallSites[f]; !ok {
			t.Errorf("%s opens port-forwards but is not a known call site; gate it with AutoDisabled if Radar opens it on its own, then list it here", f)
		}
	}
	for f := range portForwardCallSites {
		if !slices.Contains(found, f) {
			t.Errorf("%s is listed as a port-forward call site but no longer opens one; remove it from the list", f)
		}
	}
}
