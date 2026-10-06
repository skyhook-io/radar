package k8s

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func conditionEncodingResource(status, reason string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "example.com/v1",
		"kind":       "Widget",
		"status": map[string]any{"conditions": []any{map[string]any{
			"type": "Ready", "status": status, "reason": reason,
		}}},
	}}
}

func TestComputeDiff_ConditionSignalEncoding(t *testing.T) {
	tests := []struct {
		name string
		old  [2]string
		new  [2]string
	}{
		{"unchanged", [2]string{"True", "Ready"}, [2]string{"True", "Ready"}},
		{"status change", [2]string{"False", "Ready"}, [2]string{"True", "Ready"}},
		{"reason change", [2]string{"True", "Waiting"}, [2]string{"True", "Ready"}},
		{"NUL separator collision", [2]string{"a\x00b", "c"}, [2]string{"a", "b\x00c"}},
		{"colon separator collision", [2]string{"a:b", "c"}, [2]string{"a", "b:c"}},
		{"quote separator collision", [2]string{"a\":\"b", "c"}, [2]string{"a", "b\":\"c"}},
		{"escaped NUL collision", [2]string{"True", "\x00"}, [2]string{"True", `\x00`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldU := conditionEncodingResource(tt.old[0], tt.old[1])
			newU := conditionEncodingResource(tt.new[0], tt.new[1])
			for _, obj := range []*unstructured.Unstructured{oldU, newU} {
				signal := genericConditionSignalMap(obj.Object, "status", "conditions")["Ready"]
				if strings.ContainsRune(signal, '\x00') {
					t.Fatalf("condition signal contains a PostgreSQL-incompatible NUL: %q", signal)
				}
			}
			diff := ComputeDiffFromUnstructured("Widget", oldU, newU)
			if tt.old == tt.new {
				if diff != nil {
					t.Fatalf("unchanged condition produced a diff: %+v", diff)
				}
				return
			}
			if diff == nil || len(diff.Fields) != 1 || diff.Fields[0].Path != "status.conditions[Ready]" {
				t.Fatalf("changed condition did not produce one condition field: %+v", diff)
			}
			if diff.Fields[0].OldValue == diff.Fields[0].NewValue {
				t.Fatalf("distinct condition pairs encoded identically: %+v", diff.Fields[0])
			}
		})
	}
}
