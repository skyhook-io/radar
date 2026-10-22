package resourceapi

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
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

func TestObjectsSortAndDeduplicate(t *testing.T) {
	b := ObjectRef{Kind: "Pod", Namespace: "beta", Name: "web"}
	a := ObjectRef{Kind: "Pod", Namespace: "alpha", Name: "web"}
	set := Objects([]ObjectRef{b, a, b}, 2, 8)
	if set.Count != 2 || len(set.Names) != 2 || set.Names[0] != a || set.Names[1] != b {
		t.Fatalf("set = %+v, want alpha/web then beta/web", set)
	}
}

func TestCoverageGapsMarshalAsAnArray(t *testing.T) {
	for name, v := range map[string]any{"value": Coverage{}, "pointer": &Coverage{}, "error body": ReadError{Coverage: &Coverage{}}, "search": SearchCoverage{}, "search pointer": &SearchCoverage{}} {
		b, err := json.Marshal(v)
		if err != nil || !strings.Contains(string(b), `"gaps":[]`) {
			t.Fatalf("%s: %s %v, want gaps as []", name, b, err)
		}
	}
}

// Narrowing values are wire contracts: a released Radar's values must keep
// meaning the same thing to every later consumer.
func TestNarrowingSpellings(t *testing.T) {
	got := []Narrowing{NarrowedByUserAccess, NarrowedByNamespacePin, NarrowedByNamespaceSelection, NarrowedByNamespaceCollection, NarrowedByKindCollection}
	want := []Narrowing{"user_access", "namespace_pin", "namespace_selection", "namespace_collection", "kind_collection"}
	if !slices.Equal(got, want) {
		t.Fatalf("narrowings = %v, want %v", got, want)
	}
}
