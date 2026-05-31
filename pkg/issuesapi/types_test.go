package issuesapi

import "testing"

func TestGroupOf(t *testing.T) {
	cases := []struct {
		name string
		in   Category
		want CategoryGroup
	}{
		{"image pull", CategoryImagePullFailed, GroupStartup},
		{"unschedulable", CategoryUnschedulable, GroupScheduling},
		{"gitops sync", CategoryGitOpsSyncFailed, GroupControlPlane},
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
