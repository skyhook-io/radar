package topology

import (
	"encoding/csv"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

func TestEveryFamilyProjectsExactlyItsDeclaredFields(t *testing.T) {
	for _, f := range relations.Families("cluster.local") {
		t.Run(f.Name, func(t *testing.T) {
			validateDisposition(t, f.Projection)
			targets := f.Targets
			if f.IsTarget != nil {
				switch f.Name {
				case "nodeClasses":
					targets = []resourceid.GroupKind{{Group: "karpenter.k8s.aws", Kind: "EC2NodeClass"}}
				case "knativeEventDestinations":
					targets = []resourceid.GroupKind{{Kind: "Service"}, {Group: "eventing.knative.dev", Kind: "Broker"}}
				default:
					t.Fatal("open target family needs a synthetic target")
				}
			}
			hits := make([]bool, len(f.Projection.Rules))
			for _, src := range f.Sources {
				for _, dst := range targets {
					if len(src.Targets) > 0 && !slices.Contains(src.Targets, dst) {
						continue
					}
					checkDispositionPair(t, f.Name, f.Projection, src.Kind, dst, hits, func(s, d resourceid.Ref) (relationEdge, bool) { return edgeFor(f.Name, s, d) })
				}
			}
			requireRuleHits(t, hits)
		})
	}
	for _, f := range relations.SelectorFamilies() {
		t.Run(f.Name, func(t *testing.T) {
			validateDisposition(t, f.Projection)
			hits := make([]bool, len(f.Projection.Rules))
			for _, src := range f.Selectors {
				for _, dst := range f.Selectables {
					if len(src.Selects) > 0 && !slices.Contains(src.Selects, dst.Kind) {
						continue
					}
					checkDispositionPair(t, f.Name, f.Projection, src.Kind, dst.Kind, hits, func(s, d resourceid.Ref) (relationEdge, bool) { return projectedEdge(selectorEdges[f.Name], s, d) })
				}
			}
			requireRuleHits(t, hits)
		})
	}
	for _, f := range relations.Compositions() {
		t.Run(f.Name, func(t *testing.T) {
			validateDisposition(t, f.Projection)
			hits := make([]bool, len(f.Projection.Rules))
			for _, src := range f.Producer.Sources {
				for _, consumer := range f.Consumers {
					for _, dst := range consumer.Sources {
						if len(f.ConsumerKinds) > 0 && !slices.Contains(f.ConsumerKinds, dst.Kind) {
							continue
						}
						checkDispositionPair(t, f.Name, f.Projection, src.Kind, dst.Kind, hits, func(s, d resourceid.Ref) (relationEdge, bool) { return projectedEdge(f.Projection, s, d) })
					}
				}
			}
			requireRuleHits(t, hits)
		})
	}
}
func validateDisposition(t *testing.T, d relations.Disposition) {
	t.Helper()
	modes := 0
	if len(d.Rules) > 0 {
		modes++
	}
	if d.DirectRead != "" {
		modes++
	}
	if modes != 1 {
		t.Fatalf("need exactly one disposition: %+v", d)
	}
	for _, r := range d.Rules {
		switch EdgeType(r.Edge) {
		case EdgeManages, EdgeUses, EdgeProtects, EdgeExposes, EdgeRoutesTo, EdgeConfigures:
		default:
			t.Fatalf("invalid edge: %+v", r)
		}
		if len(r.ReferenceFields) == 0 || len(r.ReferrerFields) == 0 {
			t.Fatalf("missing endpoint fields: %+v", r)
		}
	}
}
func requireRuleHits(t *testing.T, hits []bool) {
	t.Helper()
	for i, hit := range hits {
		if !hit {
			t.Errorf("disposition rule %d was not exercised", i)
		}
	}
}
func checkDispositionPair(t *testing.T, name string, d relations.Disposition, source, target resourceid.GroupKind, hits []bool, edgeFor func(resourceid.Ref, resourceid.Ref) (relationEdge, bool)) {
	t.Helper()
	if len(d.Rules) == 0 {
		return
	}
	var rule *relations.ProjectionRule
	for i := range d.Rules {
		if d.Rules[i].Matches(source, target) {
			rule = &d.Rules[i]
			hits[i] = true
			break
		}
	}
	if rule == nil {
		t.Fatalf("unmapped %v → %v", source, target)
	}
	src := projectionCR(source.Group, source.Kind, "shop", "source", nil)
	dst := projectionCR(target.Group, target.Kind, "shop", "target", nil)
	if source.Group == "" {
		src.SetAPIVersion("v1")
	}
	if target.Group == "" {
		dst.SetAPIVersion("v1")
	}
	s := newProjectionSession(t, src, dst)
	n := &neighborhood{nodes: map[resourceid.Ref]bool{}, edges: map[string]bool{}, access: ownership.Unrestricted()}
	edge, ok := edgeFor(projectionRef(src), projectionRef(dst))
	if !ok {
		t.Fatal("no generated edge")
	}
	n.edge(projectionRef(src), projectionRef(dst), edge)
	for i, obj := range []runtime.Object{src, dst} {
		rel := GetRelationshipsWithObject(projectionRef(obj).Kind, "shop", projectionRef(obj).Name, obj, &n.topo, nil, nil, nil)
		ov := ownership.NewView(s, ownership.Unrestricted())
		m, _ := apimeta.Accessor(obj)
		ApplyOwnership(rel, ov, ownership.Observe(projectionRef(obj), m))
		other := dst
		want := rule.ReferenceFields
		if i == 1 {
			other = src
			want = rule.ReferrerFields
		}
		got := fieldsContainingRef(reflect.ValueOf(rel).Elem(), refOf(projectionRef(other)), "")
		slices.Sort(got)
		want = slices.Sorted(slices.Values(want))
		if !slices.Equal(got, want) {
			t.Fatalf("%s %v → %v endpoint %d: fields=%v want=%v; rel=%+v", name, source, target, i, got, want, rel)
		}
	}
}
func fieldsContainingRef(v reflect.Value, ref ResourceRef, prefix string) []string {
	var out []string
	for i := 0; i < v.NumField(); i++ {
		field := v.Type().Field(i)
		if !field.IsExported() || field.Name == "ManagedBy" || field.Name == "Ownership" || field.Name == "ControllerChain" {
			continue
		}
		f := v.Field(i)
		name := prefix + field.Name
		if f.Type() == reflect.TypeOf([]ResourceRef{}) {
			refs := f.Interface().([]ResourceRef)
			if slices.Contains(refs, ref) {
				out = append(out, name)
			}
		} else if f.Type() == reflect.TypeOf((*ResourceRef)(nil)) {
			if !f.IsNil() && f.Elem().Interface().(ResourceRef) == ref {
				out = append(out, name)
			}
		} else if f.Kind() == reflect.Pointer && !f.IsNil() && f.Elem().Kind() == reflect.Struct {
			out = append(out, fieldsContainingRef(f.Elem(), ref, name+".")...)
		}
	}
	return out
}

// The row identities and family names are transcribed from topology-families.md
// at the cutover handoff. This independent inventory catches missing producers
// that a registry-only test cannot see.
func TestRelationshipFamilyInventoryHasBothDirections(t *testing.T) {
	file, err := os.Open("testdata/relationship-family-inventory.tsv")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader := csv.NewReader(file)
	reader.Comma = '\t'
	rows, err := reader.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	const wantFamilies = 78
	if len(rows)-1 != wantFamilies {
		t.Fatalf("inventory has %d families; want %d", len(rows)-1, wantFamilies)
	}
	producers := map[string]bool{}
	for _, f := range relations.Families("") {
		producers[f.Name] = true
	}
	for _, f := range relations.SelectorFamilies() {
		producers[f.Name] = true
	}
	for _, f := range relations.Compositions() {
		producers[f.Name] = true
	}
	// Function references make renamed or removed direct producers fail to build.
	for name := range map[string]any{
		"ownership":       ApplyOwnership,
		"olderRevisions":  SplitOlderRevisions,
		"healthChecks":    (*neighborhood).healthChecks,
		"nodePool":        (*neighborhood).nodePool,
		"crossplane":      (*neighborhood).crossplane,
		"knativeServing":  (*neighborhood).knativeServing,
		"longhornStorage": (*neighborhood).longhornStorage,
	} {
		producers[name] = true
	}
	seen := map[string]bool{}
	for _, row := range rows[1:] {
		if len(row) != 6 {
			t.Fatalf("bad inventory row: %v", row)
		}
		id := row[0]
		if seen[id] {
			t.Fatalf("duplicate %s", id)
		}
		seen[id] = true
		if row[1] == "" {
			t.Errorf("%s has no family name", id)
		}
		if row[4] != "" {
			if row[2] != "" || row[3] != "" {
				t.Errorf("%s has both producers and NotProjected", id)
			}
			continue
		}
		for _, direction := range []int{2, 3} {
			if row[direction] == "" {
				t.Errorf("%s has no %s producer or NotProjected reason", id, rows[0][direction])
				continue
			}
			for _, producer := range strings.Split(row[direction], ",") {
				if !producers[producer] {
					t.Errorf("%s: unknown %s producer %s", id, rows[0][direction], producer)
				}
			}
		}
	}
	for i := 1; i <= wantFamilies; i++ {
		if !seen[fmt.Sprintf("F%02d", i)] {
			t.Errorf("missing inventory family F%02d", i)
		}
	}
}
