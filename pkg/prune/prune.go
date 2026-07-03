// Package prune subtracts heavy or noisy subtrees from unstructured Kubernetes
// objects according to declarative per-kind profiles. It is the shared
// mechanism under consumer-specific policies: the resources API's
// include=summary keep-lists (internal/server/resource_summary.go) and —
// planned — pkg/ai/context's verbosity pruning. Policies (WHICH paths drop)
// stay with their consumers; only the tree surgery lives here.
package prune

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// TailTrim reduces a slice at Path to only its LAST element, itself reduced
// to only KeepField (e.g. an Argo Application's status.history → the last
// entry's deployedAt). A missing or empty slice is left untouched; a tail
// element missing KeepField yields an empty object.
type TailTrim struct {
	Path      []string
	KeepField string
}

// Profile declares the subtractions for one object shape.
type Profile struct {
	Drop      [][]string
	TailTrims []TailTrim
}

// Apply returns a deep copy of obj with the profile's subtractions applied.
// The input is NEVER mutated — callers routinely hold informer-cache objects,
// where in-place edits would corrupt every other consumer.
func Apply(obj *unstructured.Unstructured, p Profile) *unstructured.Unstructured {
	if obj == nil {
		return nil
	}
	copied := obj.DeepCopy()
	ApplyInPlace(copied.Object, p)
	return copied
}

// ApplyInPlace executes the profile directly on m — for callers that already
// own a copy (pkg/ai/context deep-copies before minifying) and shouldn't pay
// for a second one. Never hand this an informer-cache object.
func ApplyInPlace(m map[string]any, p Profile) {
	if m == nil {
		return
	}
	for _, path := range p.Drop {
		unstructured.RemoveNestedField(m, path...)
	}
	for _, t := range p.TailTrims {
		applyTailTrimMap(m, t)
	}
}

func applyTailTrimMap(m map[string]any, t TailTrim) {
	items, found, _ := unstructured.NestedSlice(m, t.Path...)
	if !found || len(items) == 0 {
		return
	}
	trimmed := map[string]any{}
	if last, ok := items[len(items)-1].(map[string]any); ok {
		if v, exists := last[t.KeepField]; exists {
			trimmed[t.KeepField] = v
		}
	}
	if err := unstructured.SetNestedSlice(m, []any{trimmed}, t.Path...); err != nil {
		unstructured.RemoveNestedField(m, t.Path...)
	}
}
