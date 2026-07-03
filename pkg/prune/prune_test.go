package prune

import (
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
