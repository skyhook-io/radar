package insights

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// liveWith builds an Unstructured with the given last-applied-configuration
// (raw JSON string) and live spec map. Lets each test case express drift
// inputs without boilerplate.
func liveWith(lastApplied string, liveSpec map[string]any) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Resource",
		"metadata": map[string]any{
			"name":      "x",
			"namespace": "y",
			"annotations": map[string]any{
				kubectlLastAppliedAnnotation: lastApplied,
			},
		},
	}}
	if liveSpec != nil {
		obj.Object["spec"] = liveSpec
	}
	return obj
}

func TestComputeDrift_NoAnnotation_ReturnsNil(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"a": 1}}}
	if got := computeDriftFromLastApplied(obj); got != nil {
		t.Fatalf("expected nil when annotation missing, got %#v", got)
	}
}
func TestComputeDrift_InvalidJSON_ReturnsNil(t *testing.T) {
	if got := computeDriftFromLastApplied(liveWith("not-json", map[string]any{"a": 1})); got != nil {
		t.Fatalf("expected nil on parse failure, got %#v", got)
	}
}

func TestComputeDrift_NoDrift_ReturnsNil(t *testing.T) {
	desired := `{"spec":{"a":1,"b":"two"}}`
	got := computeDriftFromLastApplied(liveWith(desired, map[string]any{"a": int64(1), "b": "two"}))
	if got != nil {
		t.Fatalf("expected nil when desired and live match, got %d entries", len(got.Entries))
	}
}

func TestComputeDrift_KarpenterStyleSchemaMigration(t *testing.T) {
	// This is the actual case the user hit in the cluster: NodePool's
	// expireAfter moved from spec.disruption to spec.template.spec, and
	// budgets got defaulted in. Verify all three drift entries surface.
	desired := `{"spec":{"disruption":{"consolidateAfter":"30s","consolidationPolicy":"WhenEmptyOrUnderutilized","expireAfter":"720h"},"template":{"spec":{"requirements":[]}}}}`
	live := map[string]any{
		"disruption": map[string]any{
			"budgets":             []any{map[string]any{"nodes": "10%"}},
			"consolidateAfter":    "30s",
			"consolidationPolicy": "WhenEmptyOrUnderutilized",
		},
		"template": map[string]any{
			"spec": map[string]any{
				"expireAfter":  "720h",
				"requirements": []any{},
			},
		},
	}
	got := computeDriftFromLastApplied(liveWith(desired, live))
	if got == nil {
		t.Fatal("expected drift, got nil")
	}
	wantPaths := map[string]DriftOp{
		"spec.disruption.expireAfter":    DriftOpRemoved,
		"spec.disruption.budgets":        DriftOpAdded,
		"spec.template.spec.expireAfter": DriftOpAdded,
	}
	for _, e := range got.Entries {
		want, ok := wantPaths[e.Path]
		if !ok {
			continue
		}
		if e.Op != want {
			t.Errorf("path %s: op = %q, want %q", e.Path, e.Op, want)
		}
		delete(wantPaths, e.Path)
	}
	for path, op := range wantPaths {
		t.Errorf("missing expected entry: %s (op=%s); entries=%v", path, op, got.Entries)
	}
}

func TestComputeDrift_ScalarChange(t *testing.T) {
	desired := `{"spec":{"replicas":3}}`
	got := computeDriftFromLastApplied(liveWith(desired, map[string]any{"replicas": int64(5)}))
	if got == nil || len(got.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %v", got)
	}
	e := got.Entries[0]
	if e.Path != "spec.replicas" || e.Op != DriftOpChanged {
		t.Errorf("entry = %+v, want path=spec.replicas op=changed", e)
	}
	if !strings.Contains(e.Desired, "3") || !strings.Contains(e.Live, "5") {
		t.Errorf("expected desired to contain 3 and live to contain 5, got desired=%q live=%q", e.Desired, e.Live)
	}
}

func TestComputeDrift_TreatsEmptyAsNil(t *testing.T) {
	// Defaulted-in empty maps and arrays shouldn't show as drift.
	desired := `{"spec":{"a":{}}}`
	got := computeDriftFromLastApplied(liveWith(desired, map[string]any{"a": map[string]any{}}))
	if got != nil {
		t.Errorf("empty map vs empty map should not produce drift, got %v", got.Entries)
	}
}

// TestComputeDrift_NilishCrossSide pins the cross-side behavior: "nil/empty
// on one side, real value on the other" emits a single entry at the parent
// path rather than recursing into the other side. This keeps the diff
// readable when an entire subtree was added or removed.
func TestComputeDrift_NilishCrossSide(t *testing.T) {
	cases := []struct {
		name        string
		desired     string
		live        map[string]any
		wantOp      DriftOp
		wantPath    string
		wantInValue string // substring expected in the populated side's JSON
	}{
		{
			name:        "empty desired map vs non-empty live map → added at parent path",
			desired:     `{"spec":{"a":{}}}`,
			live:        map[string]any{"a": map[string]any{"x": "1"}},
			wantOp:      DriftOpAdded,
			wantPath:    "spec.a",
			wantInValue: `"x"`,
		},
		{
			name:        "non-empty desired map vs empty live map → removed at parent path",
			desired:     `{"spec":{"a":{"x":"1"}}}`,
			live:        map[string]any{"a": map[string]any{}},
			wantOp:      DriftOpRemoved,
			wantPath:    "spec.a",
			wantInValue: `"x"`,
		},
		{
			name:        "missing-on-desired vs present-on-live → added at the closest containing path",
			desired:     `{"spec":{}}`,
			live:        map[string]any{"replicas": int64(3)},
			wantOp:      DriftOpAdded,
			wantPath:    "spec",
			wantInValue: `"replicas"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := computeDriftFromLastApplied(liveWith(tc.desired, tc.live))
			if got == nil {
				t.Fatalf("expected drift, got nil")
			}
			var entry *DriftEntry
			for i := range got.Entries {
				if got.Entries[i].Path == tc.wantPath {
					entry = &got.Entries[i]
					break
				}
			}
			if entry == nil {
				t.Fatalf("expected entry at %s; got entries=%+v", tc.wantPath, got.Entries)
			}
			if entry.Op != tc.wantOp {
				t.Errorf("path %s: op = %q, want %q", tc.wantPath, entry.Op, tc.wantOp)
			}
			payload := entry.Live + entry.Desired
			if !strings.Contains(payload, tc.wantInValue) {
				t.Errorf("path %s: payload should contain %q; live=%q desired=%q", tc.wantPath, tc.wantInValue, entry.Live, entry.Desired)
			}
		})
	}
}
