package insights

import "sort"

// DiffObjects walks two whole manifests (desired vs live) and returns the
// field-level differences, reusing the same diffValues walker that backs the
// last-applied drift path.
//
// Two scoping rules keep the output to real, user-actionable drift:
//   - `status` is skipped entirely — it is controller-owned observed state, not
//     declared intent.
//   - under `metadata`, only `labels` and `annotations` are compared. The rest
//     of metadata (resourceVersion, uid, generation, managedFields,
//     creationTimestamp, …) is server-assigned and is never drift.
//
// The inputs are Argo CD's normalized/predicted states, so Argo's own
// normalizations and the Application's spec.ignoreDifferences are already
// applied upstream. This makes DiffObjects a field-level *summary* only — the
// canonical, authoritative view is the desired/live YAML pair the caller
// renders alongside these entries. Output is sorted by path and capped at
// driftEntryCap.
func DiffObjects(desired, live map[string]any) []DriftEntry {
	var entries []DriftEntry
	for _, key := range unionKeys(desired, live) {
		switch key {
		case "status":
			continue
		case "metadata":
			entries = diffMetadataScoped(desired[key], live[key], entries)
		default:
			entries = diffValues(key, desired[key], live[key], entries)
		}
	}
	entries, _ = sortAndCapDriftEntries(entries)
	return entries
}

// diffMetadataScoped diffs only the labels and annotations sub-maps of two
// metadata blocks, ignoring server-assigned identity/versioning fields.
func diffMetadataScoped(desired, live any, out []DriftEntry) []DriftEntry {
	desiredMeta, _ := desired.(map[string]any)
	liveMeta, _ := live.(map[string]any)
	for _, field := range []string{"labels", "annotations"} {
		out = diffValues("metadata."+field, desiredMeta[field], liveMeta[field], out)
	}
	return out
}

// sortAndCapDriftEntries orders entries by path and truncates to driftEntryCap,
// reporting whether truncation occurred. Shared by DiffObjects and the
// last-applied drift path so the two can't drift on ordering or the cap.
func sortAndCapDriftEntries(entries []DriftEntry) ([]DriftEntry, bool) {
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	if len(entries) > driftEntryCap {
		return entries[:driftEntryCap], true
	}
	return entries, false
}

// unionKeys returns the deduplicated set of top-level keys across two maps.
// Order is unspecified; callers that need determinism sort the resulting
// entries (DiffObjects does, via sortAndCapDriftEntries).
func unionKeys(a, b map[string]any) []string {
	set := make(map[string]struct{}, len(a)+len(b))
	for k := range a {
		set[k] = struct{}{}
	}
	for k := range b {
		set[k] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	return out
}
