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

// A gap that can't list an axis says how much of it it reaches, never by
// leaving the axis out, and carries the action its code calls for.
func TestGapsStateExtentAndAction(t *testing.T) {
	for _, tc := range []struct {
		gap  Gap
		want string
	}{
		{Gap{Code: "kind_sync_pending", Namespaces: AllNames()}, `{"code":"kind_sync_pending","action":"retry","namespaces":{"extent":"all"}}`},
		{Gap{Code: "radar_namespace_not_collected", Namespaces: UnknownNames()}, `{"code":"radar_namespace_not_collected","action":"radar_config","namespaces":{"extent":"unknown"}}`},
		{Gap{Code: "read_error", Kinds: UnknownKinds()}, `{"code":"read_error","action":"retry","kinds":{"extent":"unknown"}}`},
		{Gap{Code: "user_forbidden", Namespaces: Names([]string{"b"}, MCPSetLimit)}, `{"code":"user_forbidden","action":"user_access","namespaces":{"count":1,"names":["b"]}}`},
		{Gap{Code: "some_future_code", Namespaces: AllNames()}, `{"code":"some_future_code","namespaces":{"extent":"all"}}`},
	} {
		raw, err := json.Marshal(tc.gap)
		if err != nil || string(raw) != tc.want {
			t.Errorf("marshal = %s, %v; want %s", raw, err, tc.want)
		}
	}
	if AllNames().Capped(1).Extent != ExtentAll || UnknownKinds().Capped(1).Extent != ExtentUnknown {
		t.Error("capping keeps an extent set as it is")
	}
}

func TestValidate(t *testing.T) {
	ns := func(names ...string) *NameSet { return Names(names, MCPSetLimit) }
	scoped := func(set *NameSet) Scope { return Scope{Type: ScopeNamespaces, Namespaces: set} }
	all := Scope{Type: ScopeAllNamespaces}
	valid := []Coverage{
		{Status: CoverageComplete, Scope: all, Read: &all},
		{Status: CoverageComplete, Scope: Scope{Type: ScopeCluster}, Read: &Scope{Type: ScopeCluster}},
		{Status: CoveragePartial, Scope: scoped(ns("a", "g")), Read: ptr(scoped(ns("a"))), Gaps: []Gap{{Code: "kind_sync_pending", Namespaces: ns("g")}}},
		{Status: CoveragePartial, Scope: scoped(&NameSet{Count: 20}), Read: ptr(scoped(ns("a", "b"))), Gaps: []Gap{{Code: "kind_sync_pending", Namespaces: &NameSet{Count: 18}}}},
		{Status: CoveragePartial, Scope: all, Gaps: []Gap{{Code: "kind_sync_pending", Namespaces: AllNames()}}},
		{Status: CoveragePartial, Scope: scoped(ns("a", "b")), Gaps: []Gap{{Code: "user_forbidden", Namespaces: ns("a")}, {Code: "radar_kind_not_collected", Namespaces: ns("b")}}},
		{Status: CoveragePartial, Scope: all, Read: ptr(scoped(ns("a"))), Gaps: []Gap{{Code: "radar_namespace_not_collected", Namespaces: UnknownNames()}}},
	}
	for i, c := range valid {
		if err := c.Validate(); err != nil {
			t.Errorf("valid[%d]: %v", i, err)
		}
	}
	invalid := map[string]Coverage{
		"complete with a gap":          {Status: CoverageComplete, Scope: all, Read: &all, Gaps: []Gap{{Code: "kind_sync_pending", Namespaces: AllNames()}}},
		"partial with no gap":          {Status: CoveragePartial, Scope: all, Read: &all},
		"gap leaves namespaces out":    {Status: CoveragePartial, Scope: scoped(ns("a", "g")), Read: ptr(scoped(ns("a"))), Gaps: []Gap{{Code: "kind_sync_pending"}}},
		"namespaces on a cluster":      {Status: CoveragePartial, Scope: Scope{Type: ScopeCluster}, Gaps: []Gap{{Code: "kind_sync_pending", Namespaces: AllNames()}}},
		"namespace in two places":      {Status: CoveragePartial, Scope: scoped(ns("a", "g")), Read: ptr(scoped(ns("a", "g"))), Gaps: []Gap{{Code: "kind_sync_pending", Namespaces: ns("g")}}},
		"parts don't add up":           {Status: CoveragePartial, Scope: scoped(&NameSet{Count: 20}), Read: ptr(scoped(ns("a"))), Gaps: []Gap{{Code: "kind_sync_pending", Namespaces: &NameSet{Count: 18}}}},
		"scope namespace unread":       {Status: CoveragePartial, Scope: scoped(ns("a", "b", "g")), Read: ptr(scoped(ns("a"))), Gaps: []Gap{{Code: "kind_sync_pending", Namespaces: ns("g")}, {Code: "user_forbidden", Namespaces: ns("x")}}},
		"whole-scope gap beside read":  {Status: CoveragePartial, Scope: scoped(ns("a", "b")), Read: ptr(scoped(ns("a"))), Gaps: []Gap{{Code: "kind_sync_pending", Namespaces: AllNames()}}},
		"whole-scope gap beside a gap": {Status: CoveragePartial, Scope: scoped(ns("a", "b")), Gaps: []Gap{{Code: "user_forbidden", Namespaces: ns("a")}, {Code: "kind_sync_pending", Namespaces: AllNames()}}},
		"complete read isn't scope":    {Status: CoverageComplete, Scope: all, Read: ptr(scoped(ns("a")))},
		"gap outside scope":            {Status: CoveragePartial, Scope: scoped(ns("a", "b")), Read: ptr(scoped(&NameSet{Count: 1})), Gaps: []Gap{{Code: "kind_sync_pending", Namespaces: ns("x")}}},
		"extent on scope":              {Status: CoverageComplete, Scope: scoped(UnknownNames()), Read: ptr(scoped(UnknownNames()))},
		"unsorted names":               {Status: CoverageComplete, Scope: scoped(&NameSet{Count: 2, Names: []string{"b", "a"}}), Read: ptr(scoped(&NameSet{Count: 2, Names: []string{"b", "a"}}))},
	}
	for name, c := range invalid {
		if err := c.Validate(); err == nil {
			t.Errorf("%s: Validate passed %+v", name, c)
		}
	}
}

func ptr[T any](v T) *T { return &v }
