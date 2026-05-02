package insights

import (
	"encoding/json"
	"fmt"
	"sort"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// kubectlLastAppliedAnnotation is the annotation kubectl (and Argo's
// client-side apply path) writes on every apply, holding a JSON dump of
// the desired state at the time of apply. We diff this against the live
// spec to produce a per-resource drift view without needing the Argo API
// server or a Git fetch.
//
// Limitations:
//   - server-side-apply doesn't write this annotation; SSA tracks intent
//     via metadata.managedFields instead. Future work: managedFields-based
//     drift for SSA-applied resources.
//   - Helm-installed resources (Flux HelmRelease, helm CLI) don't carry
//     this annotation either.
//   - Resources mutated by other controllers between apply and drift
//     check will show those mutations as added/changed entries — that's
//     the *correct* behavior for a "what's drifted" view, even if the
//     mutation is harmless (defaults, status fields).
const kubectlLastAppliedAnnotation = "kubectl.kubernetes.io/last-applied-configuration"

// driftEntryCap bounds the number of entries returned per resource. Sets
// the worst-case payload size and keeps the UI scannable. When trimmed,
// Drift.Truncated is set so the UI can suggest "see Argo for full diff".
const driftEntryCap = 50

// computeDriftFromLastApplied diffs the live spec against the desired spec
// captured in the last-applied-configuration annotation. Returns nil when
// drift can't be computed (no annotation, parse failure, no spec on either
// side) — callers should treat nil as "no diff available", not "no drift".
func computeDriftFromLastApplied(live *unstructured.Unstructured) *Drift {
	if live == nil {
		return nil
	}
	annotations := live.GetAnnotations()
	raw := annotations[kubectlLastAppliedAnnotation]
	if raw == "" {
		return nil
	}
	var desiredObj map[string]any
	if err := json.Unmarshal([]byte(raw), &desiredObj); err != nil {
		return nil
	}
	desiredSpec, _ := desiredObj["spec"].(map[string]any)
	liveSpec, _, _ := unstructured.NestedMap(live.Object, "spec")
	if desiredSpec == nil && liveSpec == nil {
		return nil
	}
	entries := diffValues("spec", desiredSpec, liveSpec, nil)
	if len(entries) == 0 {
		// last-applied parsed successfully but no field-level diff —
		// returning an empty Drift would be misleading (UI would show
		// "no drift" alongside the "OutOfSync" badge). Nil signals "we
		// looked but didn't find structural drift" and the UI can fall
		// back to the textual explainer.
		return nil
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	truncated := false
	if len(entries) > driftEntryCap {
		entries = entries[:driftEntryCap]
		truncated = true
	}
	return &Drift{
		Entries:   entries,
		Source:    "lastAppliedAnnotation",
		Truncated: truncated,
	}
}

// diffValues recursively walks desired vs live and emits entries where they
// differ. Maps are descended; arrays and scalars are compared by serialized
// equality (cheaper than deep-comparing array elements field-by-field, and
// arrays of structs are typically rewritten wholesale anyway). nil/absent
// values are normalized so {a: nil} and missing-a are treated as equal.
//
// out is passed by reference to avoid allocations per recursion level.
func diffValues(path string, desired, live any, out []DriftEntry) []DriftEntry {
	if isNilish(desired) && isNilish(live) {
		return out
	}
	if isNilish(desired) {
		return append(out, DriftEntry{Path: path, Op: "added", Live: jsonString(live)})
	}
	if isNilish(live) {
		return append(out, DriftEntry{Path: path, Op: "removed", Desired: jsonString(desired)})
	}
	dMap, dIsMap := desired.(map[string]any)
	lMap, lIsMap := live.(map[string]any)
	if dIsMap && lIsMap {
		// Recurse on union of keys so we catch added-on-live and removed-on-live.
		keys := make(map[string]struct{}, len(dMap)+len(lMap))
		for k := range dMap {
			keys[k] = struct{}{}
		}
		for k := range lMap {
			keys[k] = struct{}{}
		}
		for k := range keys {
			out = diffValues(joinPath(path, k), dMap[k], lMap[k], out)
		}
		return out
	}
	// At least one side is non-map (scalar, array, or one's a map and one's
	// a scalar — schema mismatch). Compare by serialized form.
	desiredStr := jsonString(desired)
	liveStr := jsonString(live)
	if desiredStr == liveStr {
		return out
	}
	return append(out, DriftEntry{Path: path, Op: "changed", Desired: desiredStr, Live: liveStr})
}

// joinPath produces dot-notation paths. If the segment looks like an array
// index, it's wrapped in brackets ("foo.[0].bar"); otherwise concatenated
// with a dot. Index detection is naive (all-digit) but sufficient — we
// don't currently descend into arrays anyway.
func joinPath(prefix, segment string) string {
	if prefix == "" {
		return segment
	}
	if isAllDigits(segment) {
		return prefix + ".[" + segment + "]"
	}
	return prefix + "." + segment
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// isNilish treats nil, empty maps, empty arrays, and empty strings as
// equivalent. Without this, a field defaulted to {} or [] in one side
// would always show as drift even though semantically there's nothing
// there.
func isNilish(v any) bool {
	if v == nil {
		return true
	}
	switch val := v.(type) {
	case map[string]any:
		return len(val) == 0
	case []any:
		return len(val) == 0
	case string:
		return val == ""
	}
	return false
}

// jsonString returns a stable single-line JSON encoding of v, suitable for
// scalar comparison and UI display. Strings are intentionally quoted (so
// the UI can tell `"true"` from `true`) and maps are sorted by key
// (json.Marshal on map[string]any does this since Go 1.12).
func jsonString(v any) string {
	if v == nil {
		return "null"
	}
	if s, ok := v.(string); ok {
		// Quote strings via Marshal so embedded characters escape correctly.
		b, err := json.Marshal(s)
		if err != nil {
			return s
		}
		return string(b)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

// looksLikeFieldRename detects the schema-migration pattern where the same
// value disappears from one path and appears at another. Returns the pairs
// of (old, new) paths that look like renames; the UI can present these as
// "field moved" instead of as separate add/remove entries. Currently only
// matches exact-value pairs (string/number scalars); structural renames
// would need fuzzy matching.
func looksLikeFieldRename(entries []DriftEntry) [][2]string {
	removedByValue := map[string]string{}
	for _, e := range entries {
		if e.Op == "removed" {
			removedByValue[e.Desired] = e.Path
		}
	}
	var out [][2]string
	for _, e := range entries {
		if e.Op != "added" {
			continue
		}
		if oldPath, ok := removedByValue[e.Live]; ok && oldPath != e.Path {
			out = append(out, [2]string{oldPath, e.Path})
		}
	}
	return out
}

// looksLikeFieldRename is exposed for test coverage; the production
// renderer shows the raw add/remove pairs today and the user can spot
// the moved value themselves. UI-side heuristics can layer on later
// without changing the wire format.
var _ = looksLikeFieldRename
