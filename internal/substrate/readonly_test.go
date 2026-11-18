package substrate

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/substrate/ateapi"
)

// readRPCs are the only Substrate API methods Radar may call. Radar reads
// Substrate as ate-client, usually a global owner there, so a write added
// here is a write as that owner: adding one must be a deliberate change to
// this list.
var readRPCs = []string{
	"GetActor", "GetActorEgressPolicy", "GetActorTemplate",
	"ListActorTemplates", "ListActors", "ListAtespaces", "ListWorkers",
}

// Every non-test file that imports the generated client, in this package and
// the server, calls only readRPCs on it.
func TestRadarCallsOnlyReadRPCs(t *testing.T) {
	control := reflect.TypeFor[ateapi.ControlClient]()
	var forbidden []string
	for i := range control.NumMethod() {
		if name := control.Method(i).Name; !slices.Contains(readRPCs, name) {
			forbidden = append(forbidden, name)
		}
	}
	if len(forbidden) == 0 {
		t.Fatal("found no ControlClient methods")
	}
	// An RPC takes a context; a protobuf getter of the same name (a
	// message's GetWorker()) takes nothing.
	call := regexp.MustCompile(`\.(` + strings.Join(forbidden, "|") + `)\([^)]`)
	files, _ := filepath.Glob("*.go")
	server, _ := filepath.Glob("../server/*.go")
	scanned := 0
	for _, f := range append(files, server...) {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(src), `"github.com/skyhook-io/radar/internal/substrate/ateapi"`) && filepath.Dir(f) != "." {
			continue
		}
		scanned++
		for _, m := range call.FindAllStringSubmatch(string(src), -1) {
			t.Errorf("%s calls %s, which is not a read RPC Radar may make", f, m[1])
		}
	}
	if scanned < 3 {
		t.Errorf("scanned only %d files; the glob no longer finds the callers", scanned)
	}
}
