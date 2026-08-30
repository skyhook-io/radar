package issuesapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestGroupOf(t *testing.T) {
	cases := []struct {
		name string
		in   Category
		want CategoryGroup
	}{
		{"image pull", CategoryImagePullFailed, GroupStartup},
		{"unschedulable", CategoryUnschedulable, GroupScheduling},
		{"gitops sync", CategoryGitOpsSyncFailed, GroupControlPlane},
		{"native helm release failed", CategoryHelmReleaseFailed, GroupControlPlane},
		{"unknown", CategoryUnknown, GroupUnknown},
		{"unmapped", Category("future_category"), GroupUnknown},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := GroupOf(tc.in); got != tc.want {
				t.Fatalf("GroupOf(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}

	for c, g := range categoryGroup {
		if g == GroupUnknown {
			t.Fatalf("category %q maps to GroupUnknown", c)
		}
	}
}

func TestIssueOnsetProvenanceWireShape(t *testing.T) {
	createdAt := time.Date(2026, 8, 9, 1, 2, 3, 0, time.UTC)
	blob, err := json.Marshal(Issue{
		OnsetUnknown:      false,
		OnsetCoverage:     &OnsetCoverage{Known: 2, Unknown: 1},
		ResourceCreatedAt: createdAt,
		TimingSummary:     "Some timing is unknown.",
	})
	if err != nil {
		t.Fatal(err)
	}
	wire := string(blob)
	if !strings.Contains(wire, `"onset_coverage":{"known":2,"unknown":1}`) ||
		!strings.Contains(wire, `"resource_created_at":"2026-08-09T01:02:03Z"`) ||
		!strings.Contains(wire, `"timing_summary":"Some timing is unknown."`) {
		t.Fatalf("onset provenance wire shape = %s", wire)
	}
}
