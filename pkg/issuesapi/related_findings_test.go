package issuesapi_test

import (
	"encoding/json"
	"testing"

	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/resourceapi"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	"github.com/skyhook-io/radar/pkg/topology"
)

func TestRelatedFindingsWireCarriers(t *testing.T) {
	groups := []issuesapi.RelatedFindingsGroup{{
		AssociatedVia: issuesapi.Ref{Group: "postgresql.cnpg.io", Kind: "Cluster", Namespace: "db", Name: "orders"},
		Kinds:         *resourceapi.Kinds([]resourceapi.KindRef{{Group: "postgresql.cnpg.io", Kind: "Backup"}}, 5),
		Checked:       2, Unknown: 1,
	}}
	for name, carrier := range map[string]any{
		"REST":    topology.ResourceWithRelationships{RelatedFindings: groups},
		"context": resourcecontext.ResourceContext{RelatedFindings: groups},
	} {
		t.Run(name, func(t *testing.T) {
			data, err := json.Marshal(carrier)
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]json.RawMessage
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			var list []map[string]json.RawMessage
			if err := json.Unmarshal(got["relatedFindings"], &list); err != nil {
				t.Fatal(err)
			}
			if len(list) != 1 {
				t.Fatalf("groups: %s", data)
			}
			g := list[0]
			if string(g["gaps"]) != "[]" {
				t.Fatalf("empty gaps must be an array: %s", data)
			}
			var anchor issuesapi.Ref
			if err := json.Unmarshal(g["associatedVia"], &anchor); err != nil {
				t.Fatal(err)
			}
			if anchor != groups[0].AssociatedVia {
				t.Fatalf("anchor: %#v", anchor)
			}
			var kinds resourceapi.KindSet
			if err := json.Unmarshal(g["kinds"], &kinds); err != nil {
				t.Fatal(err)
			}
			if kinds.Extent != "" || kinds.Count != 1 || len(kinds.Names) != 1 || kinds.Names[0].Group != "postgresql.cnpg.io" {
				t.Fatalf("kinds: %#v", kinds)
			}
			for _, key := range []string{"coverage", "note", "source"} {
				if _, ok := g[key]; ok {
					t.Fatalf("unexpected %s: %s", key, data)
				}
			}
		})
	}
}
