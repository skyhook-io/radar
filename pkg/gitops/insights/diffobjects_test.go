package insights

import (
	"fmt"
	"testing"
)

func TestDiffObjects(t *testing.T) {
	tests := []struct {
		name        string
		desired     map[string]any
		live        map[string]any
		wantPaths   []string // exact set of entry paths, in sorted order
		wantNoPaths []string // paths that must NOT appear
	}{
		{
			name: "spec change detected",
			desired: map[string]any{
				"spec": map[string]any{"replicas": int64(3)},
			},
			live: map[string]any{
				"spec": map[string]any{"replicas": int64(1)},
			},
			wantPaths: []string{"spec.replicas"},
		},
		{
			name: "status ignored",
			desired: map[string]any{
				"spec":   map[string]any{"replicas": int64(3)},
				"status": map[string]any{"readyReplicas": int64(3)},
			},
			live: map[string]any{
				"spec":   map[string]any{"replicas": int64(3)},
				"status": map[string]any{"readyReplicas": int64(0)},
			},
			wantPaths:   nil,
			wantNoPaths: []string{"status.readyReplicas"},
		},
		{
			name: "metadata labels change detected, resourceVersion ignored",
			desired: map[string]any{
				"metadata": map[string]any{
					"name":            "web",
					"resourceVersion": "100",
					"uid":             "aaa",
					"generation":      int64(1),
					"labels":          map[string]any{"app": "web"},
				},
			},
			live: map[string]any{
				"metadata": map[string]any{
					"name":            "web",
					"resourceVersion": "205",
					"uid":             "aaa",
					"generation":      int64(4),
					"labels":          map[string]any{"app": "api"},
				},
			},
			wantPaths: []string{"metadata.labels.app"},
			wantNoPaths: []string{
				"metadata.resourceVersion",
				"metadata.uid",
				"metadata.generation",
				"metadata.name",
			},
		},
		{
			name: "metadata annotations change detected",
			desired: map[string]any{
				"metadata": map[string]any{
					"annotations": map[string]any{"team": "payments"},
				},
			},
			live: map[string]any{
				"metadata": map[string]any{
					"annotations": map[string]any{"team": "platform"},
				},
			},
			wantPaths: []string{"metadata.annotations.team"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entries := DiffObjects(tc.desired, tc.live)
			got := make(map[string]bool, len(entries))
			paths := make([]string, 0, len(entries))
			for _, e := range entries {
				got[e.Path] = true
				paths = append(paths, e.Path)
			}
			if len(entries) != len(tc.wantPaths) {
				t.Fatalf("entry count = %d %v, want %d %v", len(entries), paths, len(tc.wantPaths), tc.wantPaths)
			}
			for i, want := range tc.wantPaths {
				if paths[i] != want {
					t.Errorf("entry[%d].Path = %q, want %q (full: %v)", i, paths[i], want, paths)
				}
			}
			for _, forbidden := range tc.wantNoPaths {
				if got[forbidden] {
					t.Errorf("path %q should have been ignored but was reported", forbidden)
				}
			}
		})
	}
}

// TestDiffObjectsSortedAndCapped pins the shared sort+cap behavior: with more
// differing fields than driftEntryCap, the output is truncated to the cap and
// remains sorted by path.
func TestDiffObjectsSortedAndCapped(t *testing.T) {
	desired := map[string]any{"spec": map[string]any{}}
	live := map[string]any{"spec": map[string]any{}}
	dSpec := desired["spec"].(map[string]any)
	lSpec := live["spec"].(map[string]any)
	// 120 differing fields under spec, well above driftEntryCap (50).
	for i := 0; i < 120; i++ {
		key := fmt.Sprintf("field%03d", i)
		dSpec[key] = "desired"
		lSpec[key] = "live"
	}

	entries := DiffObjects(desired, live)
	if len(entries) != driftEntryCap {
		t.Fatalf("entry count = %d, want cap %d", len(entries), driftEntryCap)
	}
	for i := 1; i < len(entries); i++ {
		if entries[i-1].Path > entries[i].Path {
			t.Fatalf("entries not sorted: %q > %q at index %d", entries[i-1].Path, entries[i].Path, i)
		}
	}
	// Deterministic truncation: after sorting field000..field119, the cap keeps
	// the lexicographically-first driftEntryCap paths, so field000 is present
	// and field119 is dropped.
	first := entries[0].Path
	if first != "spec.field000" {
		t.Errorf("first entry = %q, want spec.field000 (sort applied before cap)", first)
	}
}
