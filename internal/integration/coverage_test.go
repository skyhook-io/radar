package integration

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestKindCoverageJSON(t *testing.T) {
	b, err := json.Marshal(KindAccess{State: KindCoverageUncached, Uncached: []string{"b"}}.Coverage())
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != `{"state":"uncached","uncachedNamespaces":["b"]}` {
		t.Errorf("json = %s", got)
	}
	b, _ = json.Marshal(KindAccess{State: KindCoverageFull, All: true}.Coverage())
	if strings.Contains(string(b), "Namespaces") {
		t.Errorf("full coverage carries namespace lists: %s", b)
	}
}
