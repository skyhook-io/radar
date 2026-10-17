package resourceapi

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"
)

func TestSetsListAllMembersOrNone(t *testing.T) {
	names := make([]string, 1000)
	for i := range names {
		names[i] = fmt.Sprintf("team-%04d", 999-i)
	}
	original := slices.Clone(names)
	if set := Names(names, MCPSetLimit); set.Count != 1000 || set.Names != nil {
		t.Fatalf("a set over the limit lists no names: %+v", set)
	}
	if !slices.Equal(names, original) {
		t.Fatal("Names mutated the caller's slice")
	}
	if got := Names([]string{"beta", "alpha", "alpha"}, RESTSetLimit); got.Count != 2 || !slices.Equal(got.Names, []string{"alpha", "beta"}) {
		t.Fatalf("set = %+v", got)
	}
	if got := Names(names[:MCPSetLimit], MCPSetLimit); got.Count != MCPSetLimit || len(got.Names) != MCPSetLimit {
		t.Fatalf("a set at the limit lists every name: %+v", got)
	}
	if got := Names(names[:64], RESTSetLimit).Capped(MCPSetLimit); got.Count != 64 || got.Names != nil {
		t.Fatalf("capping drops names over the smaller limit: %+v", got)
	}
}

// An empty set lists its (no) members; a set over the limit lists none.
func TestSetsMarshal(t *testing.T) {
	for _, v := range []any{Names(nil, 8), Kinds(nil, 8), Objects(nil, 0, 8)} {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != `{"count":0,"names":[]}` {
			t.Fatalf("%T = %s", v, raw)
		}
	}
	raw, _ := json.Marshal(Names([]string{"a", "b", "c"}, 2))
	if string(raw) != `{"count":3}` {
		t.Fatalf("over the limit = %s", raw)
	}
}

func TestObjectsListOnlyAKnownWholeSet(t *testing.T) {
	if set := Objects([]ObjectRef{{Kind: "Pod", Namespace: "a", Name: "x"}}, 37, 8); set.Count != 37 || set.Names != nil {
		t.Fatalf("a partly known set lists no names: %+v", set)
	}
	if set := Objects([]ObjectRef{{Kind: "Pod", Namespace: "a", Name: "x"}}, 1, 8); set.Count != 1 || len(set.Names) != 1 {
		t.Fatalf("set = %+v", set)
	}
}

func TestKindsDeduplicatesByKindAndGroup(t *testing.T) {
	set := Kinds([]KindRef{{Kind: "Service", Group: "serving.knative.dev"}, {Kind: "Service"}, {Kind: "Service"}}, 8)
	if set.Count != 2 || set.Names[0] != (KindRef{Kind: "Service"}) {
		t.Fatalf("set = %+v", set)
	}
}
