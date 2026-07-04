// Package prune subtracts heavy or noisy subtrees from unstructured Kubernetes
// objects according to declarative per-kind profiles. It is the shared
// mechanism under consumer-specific policies: the resources API's
// include=summary keep-lists (internal/server/resource_summary.go) and
// pkg/ai/context's verbosity pruning. Policies (WHICH paths drop) stay with
// their consumers; only the tree surgery lives here.
package prune

import (
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// TailTrim reduces a slice at Path to only its LAST element, itself reduced
// to only KeepField (e.g. an Argo Application's status.history → the last
// entry's deployedAt). A missing or empty slice is left untouched; a tail
// element missing KeepField yields an empty object; a non-map tail leaves
// the slice untouched (fail open — rewriting an unexpected shape would be a
// transformation, which profiles must never do).
type TailTrim struct {
	Path      []string
	KeepField string
}

// ElementDrop deletes Keys from every map element of the slice at Path.
// Non-map elements are skipped; a missing or non-slice Path is left
// untouched (fail open).
type ElementDrop struct {
	Path []string
	Keys []string
}

// Profile declares the subtractions for one object shape. Ops execute in
// order: Drop → ElementDrops → TailTrims. Empty paths are skipped, and
// NestedSlice errors (a non-map intermediate on the path) are treated as
// not-found — every malformed shape fails open, leaving the tree untouched.
type Profile struct {
	Drop         [][]string
	ElementDrops []ElementDrop
	TailTrims    []TailTrim
}

// FromDropTables builds a Profile whose Drop paths are the cross product of
// each table's dot-separated subtree and its keys. It panics on an empty
// subtree, empty path segment, or empty leaf key: the tables are hardcoded
// compile-adjacent data evaluated at init, and a typo'd empty segment would
// otherwise silently no-op forever.
func FromDropTables(tables map[string]map[string]bool) Profile {
	var p Profile
	for subtree, keys := range tables {
		if subtree == "" {
			panic("prune.FromDropTables: empty subtree")
		}
		base := strings.Split(subtree, ".")
		for _, seg := range base {
			if seg == "" {
				panic(fmt.Sprintf("prune.FromDropTables: empty path segment in subtree %q", subtree))
			}
		}
		for key := range keys {
			if key == "" {
				panic(fmt.Sprintf("prune.FromDropTables: empty leaf key under subtree %q", subtree))
			}
			path := append(append([]string(nil), base...), key)
			p.Drop = append(p.Drop, path)
		}
	}
	// Map iteration order would otherwise make the profile nondeterministic;
	// harmless for correctness but noisy when diffing or debugging profiles.
	sort.Slice(p.Drop, func(i, j int) bool {
		return strings.Join(p.Drop[i], ".") < strings.Join(p.Drop[j], ".")
	})
	return p
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
		// RemoveNestedField panics on an empty path (fields[:len-1]).
		if len(path) == 0 {
			continue
		}
		unstructured.RemoveNestedField(m, path...)
	}
	for _, d := range p.ElementDrops {
		if len(d.Path) == 0 {
			continue
		}
		applyElementDrop(m, d)
	}
	for _, t := range p.TailTrims {
		if len(t.Path) == 0 {
			continue
		}
		applyTailTrimMap(m, t)
	}
}

func applyElementDrop(m map[string]any, d ElementDrop) {
	v, found, err := unstructured.NestedFieldNoCopy(m, d.Path...)
	if !found || err != nil {
		return
	}
	items, ok := v.([]any)
	if !ok {
		return
	}
	for _, item := range items {
		elem, ok := item.(map[string]any)
		if !ok {
			continue
		}
		for _, key := range d.Keys {
			delete(elem, key)
		}
	}
}

func applyTailTrimMap(m map[string]any, t TailTrim) {
	// NestedFieldNoCopy, not NestedSlice: the latter deep-copies via
	// DeepCopyJSONValue, which PANICS on any non-JSON scalar in the slice
	// (a hand-built int, a []string, time.Time) — the one op that could
	// crash instead of failing open like Drop/ElementDrop. No-copy read +
	// in-place rewrite keeps this op consistent with the rest of the package.
	v, found, err := unstructured.NestedFieldNoCopy(m, t.Path...)
	if !found || err != nil {
		return
	}
	items, ok := v.([]any)
	if !ok || len(items) == 0 {
		return
	}
	last, ok := items[len(items)-1].(map[string]any)
	if !ok {
		return
	}
	trimmed := map[string]any{}
	if val, exists := last[t.KeepField]; exists {
		trimmed[t.KeepField] = val
	}
	// Assign a FRESH one-element slice (not items[:1], which would keep the
	// original backing array — and every pruned entry — live until the
	// response is released). No SetNestedSlice, so no deep-copy/panic path.
	if base, ok := parentMap(m, t.Path); ok {
		base[t.Path[len(t.Path)-1]] = []any{trimmed}
	}
}

// parentMap walks to the map holding the final path segment, returning it so
// a caller can rewrite that key. Returns ok=false if any intermediate is
// absent or not a map.
func parentMap(m map[string]any, path []string) (map[string]any, bool) {
	cur := m
	for _, seg := range path[:len(path)-1] {
		next, ok := cur[seg].(map[string]any)
		if !ok {
			return nil, false
		}
		cur = next
	}
	return cur, true
}
