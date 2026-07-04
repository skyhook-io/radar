package prune

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func fixture() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "x", "managedFields": []any{map[string]any{"manager": "kubectl"}}},
		"status": map[string]any{
			"keep":    "value",
			"heavy":   []any{map[string]any{"a": "1"}, map[string]any{"b": "2"}},
			"history": []any{map[string]any{"rev": "1", "deployedAt": "t1"}, map[string]any{"rev": "2", "deployedAt": "t2"}},
		},
	}}
}

func TestApply_DropsAndTrimsWithoutMutatingInput(t *testing.T) {
	in := fixture()
	out := Apply(in, Profile{
		Drop:      [][]string{{"metadata", "managedFields"}, {"status", "heavy"}},
		TailTrims: []TailTrim{{Path: []string{"status", "history"}, KeepField: "deployedAt"}},
	})
	if out == in {
		t.Fatalf("Apply must return a copy")
	}
	if _, found, _ := unstructured.NestedSlice(out.Object, "status", "heavy"); found {
		t.Errorf("heavy not dropped")
	}
	if v, _, _ := unstructured.NestedString(out.Object, "status", "keep"); v != "value" {
		t.Errorf("keep field lost")
	}
	hist, _, _ := unstructured.NestedSlice(out.Object, "status", "history")
	if len(hist) != 1 {
		t.Fatalf("history len = %d, want 1", len(hist))
	}
	if last, _ := hist[0].(map[string]any); last["deployedAt"] != "t2" || last["rev"] != nil {
		t.Errorf("tail trim wrong: %+v", hist[0])
	}
	// Input untouched.
	if _, found, _ := unstructured.NestedSlice(in.Object, "status", "heavy"); !found {
		t.Errorf("input mutated: heavy gone")
	}
	if orig, _, _ := unstructured.NestedSlice(in.Object, "status", "history"); len(orig) != 2 {
		t.Errorf("input mutated: history trimmed")
	}
}

func TestApply_TailTrimEdgeCases(t *testing.T) {
	// Missing slice: untouched, no panic.
	in := &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{}}}
	out := Apply(in, Profile{TailTrims: []TailTrim{{Path: []string{"status", "history"}, KeepField: "deployedAt"}}})
	if _, found, _ := unstructured.NestedSlice(out.Object, "status", "history"); found {
		t.Errorf("missing slice materialized")
	}
	// Tail lacks the field: empty object survives.
	in2 := &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{"history": []any{map[string]any{"rev": "1"}}},
	}}
	out2 := Apply(in2, Profile{TailTrims: []TailTrim{{Path: []string{"status", "history"}, KeepField: "deployedAt"}}})
	hist, _, _ := unstructured.NestedSlice(out2.Object, "status", "history")
	if len(hist) != 1 {
		t.Fatalf("want 1 entry, got %d", len(hist))
	}
	if m, _ := hist[0].(map[string]any); len(m) != 0 {
		t.Errorf("want empty object, got %+v", m)
	}
	if Apply(nil, Profile{}) != nil {
		t.Errorf("nil in, nil out")
	}
}

func TestApply_TailTrimNonMapTailLeavesSliceUntouched(t *testing.T) {
	in := &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{"history": []any{map[string]any{"deployedAt": "t1"}, "not-a-map"}},
	}}
	out := Apply(in, Profile{TailTrims: []TailTrim{{Path: []string{"status", "history"}, KeepField: "deployedAt"}}})
	hist, _, _ := unstructured.NestedFieldNoCopy(out.Object, "status", "history")
	items, ok := hist.([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("non-map tail must leave the slice untouched, got %v", hist)
	}
	if items[1] != "not-a-map" {
		t.Errorf("tail element rewritten: %v", items[1])
	}
}

func TestApply_ElementDrop(t *testing.T) {
	in := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{
			"containers": []any{
				map[string]any{"name": "app", "image": "nginx:1.25", "terminationMessagePath": "/dev/tl", "livenessProbe": map[string]any{"httpGet": map[string]any{}}},
				"not-a-map",
				map[string]any{"name": "sidecar", "terminationMessagePath": "/dev/tl"},
			},
		},
	}}
	out := Apply(in, Profile{ElementDrops: []ElementDrop{
		{Path: []string{"spec", "containers"}, Keys: []string{"terminationMessagePath", "livenessProbe"}},
		{Path: []string{"spec", "initContainers"}, Keys: []string{"terminationMessagePath"}},
	}})
	containers, _, _ := unstructured.NestedFieldNoCopy(out.Object, "spec", "containers")
	items := containers.([]any)
	first := items[0].(map[string]any)
	if _, ok := first["terminationMessagePath"]; ok {
		t.Errorf("terminationMessagePath not dropped")
	}
	if _, ok := first["livenessProbe"]; ok {
		t.Errorf("livenessProbe not dropped")
	}
	if first["name"] != "app" || first["image"] != "nginx:1.25" {
		t.Errorf("kept keys damaged: %v", first)
	}
	if items[1] != "not-a-map" {
		t.Errorf("non-map element rewritten: %v", items[1])
	}
	third := items[2].(map[string]any)
	if _, ok := third["terminationMessagePath"]; ok {
		t.Errorf("later elements not processed")
	}
	// Missing path (spec.initContainers) is a silent no-op.
	if _, found, _ := unstructured.NestedFieldNoCopy(out.Object, "spec", "initContainers"); found {
		t.Errorf("missing path materialized")
	}
	// Input untouched (Apply deep-copies).
	origFirst := in.Object["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	if _, ok := origFirst["terminationMessagePath"]; !ok {
		t.Errorf("input mutated")
	}
}

func TestApply_ElementDropNonSlicePathUntouched(t *testing.T) {
	in := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"containers": map[string]any{"name": "not-a-slice"}},
	}}
	out := Apply(in, Profile{ElementDrops: []ElementDrop{{Path: []string{"spec", "containers"}, Keys: []string{"name"}}}})
	c, _, _ := unstructured.NestedFieldNoCopy(out.Object, "spec", "containers")
	if m, ok := c.(map[string]any); !ok || m["name"] != "not-a-slice" {
		t.Errorf("non-slice path must be left untouched, got %v", c)
	}
}

func TestApplyInPlace_EmptyPathsDoNotPanic(t *testing.T) {
	m := map[string]any{"spec": map[string]any{"keep": "value"}}
	ApplyInPlace(m, Profile{
		Drop:         [][]string{{}, nil},
		ElementDrops: []ElementDrop{{Path: nil, Keys: []string{"x"}}, {Path: []string{}, Keys: []string{"x"}}},
		TailTrims:    []TailTrim{{Path: nil, KeepField: "x"}, {Path: []string{}, KeepField: "x"}},
	})
	if m["spec"].(map[string]any)["keep"] != "value" {
		t.Errorf("empty-path ops damaged the object: %v", m)
	}
}

func TestApply_DropRunsBeforeElementDrops(t *testing.T) {
	// Drop removes the whole subtree first; ElementDrops targeting inside it
	// must fail open without rematerializing anything.
	in := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{"containers": []any{map[string]any{"name": "app", "junk": "x"}}},
			},
		},
	}}
	out := Apply(in, Profile{
		Drop:         [][]string{{"spec", "template"}},
		ElementDrops: []ElementDrop{{Path: []string{"spec", "template", "spec", "containers"}, Keys: []string{"junk"}}},
	})
	if _, found, _ := unstructured.NestedFieldNoCopy(out.Object, "spec", "template"); found {
		t.Errorf("dropped subtree resurrected by a later ElementDrop")
	}
}

func TestFromDropTables(t *testing.T) {
	p := FromDropTables(map[string]map[string]bool{
		"metadata":           {"uid": true},
		"spec.template.spec": {"nodeName": true, "tolerations": true},
	})
	want := [][]string{
		{"metadata", "uid"},
		{"spec", "template", "spec", "nodeName"},
		{"spec", "template", "spec", "tolerations"},
	}
	if len(p.Drop) != len(want) {
		t.Fatalf("Drop = %v, want %v", p.Drop, want)
	}
	for i := range want {
		if strings.Join(p.Drop[i], ".") != strings.Join(want[i], ".") {
			t.Errorf("Drop[%d] = %v, want %v", i, p.Drop[i], want[i])
		}
	}
}

func TestFromDropTables_PanicsOnMalformedTables(t *testing.T) {
	cases := []struct {
		name   string
		tables map[string]map[string]bool
	}{
		{"empty subtree", map[string]map[string]bool{"": {"key": true}}},
		{"empty path segment", map[string]map[string]bool{"spec..template": {"key": true}}},
		{"empty leaf key", map[string]map[string]bool{"spec": {"": true}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("FromDropTables(%v) did not panic", tc.tables)
				}
			}()
			FromDropTables(tc.tables)
		})
	}
}

func TestApplyTailTrim_NonJSONScalarDoesNotPanic(t *testing.T) {
	// A hand-built map with a plain Go int in the tail element — the classic
	// trap that made the old NestedSlice path panic via DeepCopyJSONValue.
	m := map[string]any{"status": map[string]any{"history": []any{
		map[string]any{"id": 1, "deployedAt": "t1"},
		map[string]any{"id": 2, "deployedAt": "t2"},
	}}}
	ApplyInPlace(m, Profile{TailTrims: []TailTrim{{Path: []string{"status", "history"}, KeepField: "deployedAt"}}})
	hist, _, _ := unstructured.NestedSlice(m, "status", "history")
	if len(hist) != 1 {
		t.Fatalf("history len = %d, want 1", len(hist))
	}
	last, _ := hist[0].(map[string]any)
	if last["deployedAt"] != "t2" || last["id"] != nil {
		t.Errorf("trim wrong: %+v", last)
	}
}

func TestApplyTailTrim_FreshSliceNoBackingArrayRetention(t *testing.T) {
	m := map[string]any{"status": map[string]any{"history": []any{
		map[string]any{"deployedAt": "t1"}, map[string]any{"deployedAt": "t2"},
		map[string]any{"deployedAt": "t3"}, map[string]any{"deployedAt": "t4"},
	}}}
	ApplyInPlace(m, Profile{TailTrims: []TailTrim{{Path: []string{"status", "history"}, KeepField: "deployedAt"}}})
	hist, _, _ := unstructured.NestedFieldNoCopy(m, "status", "history")
	sl := hist.([]any)
	if len(sl) != 1 || cap(sl) != 1 {
		t.Errorf("trimmed slice len=%d cap=%d, want 1/1 (fresh slice, no retained backing array)", len(sl), cap(sl))
	}
}
