package substrate

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"go.ate.dev/substrate/api/ateapi/v1"
)

// readRPCs are the only Substrate API methods Radar may call. Radar reads
// Substrate as ate-client, usually a global owner there, so a write added
// here is a write as that owner: adding one must be a deliberate change to
// this list.
var readRPCs = []string{
	"GetActor", "GetActorEgressPolicy", "GetActorTemplate",
	"ListActorTemplates", "ListActors", "ListAtespaces", "ListWorkers",
}

// Scanned files in this package and internal/server use only ControlClient and call only readRPCs on it.
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
	constructor := regexp.MustCompile(`\bateapi\.New([[:word:]]+)Client\s*\(`)
	files, _ := filepath.Glob("*.go")
	server, _ := filepath.Glob("../server/*.go")
	scanned := 0
	for _, f := range append(files, server...) {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(data)
		if !strings.Contains(src, `"go.ate.dev/substrate/api/ateapi/v1"`) && filepath.Dir(f) != "." {
			continue
		}
		scanned++
		for _, m := range constructor.FindAllStringSubmatch(src, -1) {
			if m[1] != "Control" {
				t.Errorf("%s calls ateapi.New%sClient, but Radar may only construct a ControlClient", f, m[1])
			}
		}
		for _, m := range call.FindAllStringSubmatch(src, -1) {
			t.Errorf("%s calls %s, which is not a read RPC Radar may make", f, m[1])
		}
	}
	if scanned < 3 {
		t.Errorf("scanned only %d files; the glob no longer finds the callers", scanned)
	}
}
