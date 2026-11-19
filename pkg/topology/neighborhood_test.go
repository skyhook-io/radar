package topology

import (
	"maps"
	"testing"

	"github.com/skyhook-io/radar/pkg/resourceid"
)

func TestEdgeTypesForAutoIncludesWorkflowDefinitionEdges(t *testing.T) {
	for _, kind := range []NodeKind{KindWorkflow, KindCronWorkflow, KindWorkflowTemplate, KindClusterWorkflowTemplate} {
		if !edgeTypesForAuto(kind)[EdgeConfigures] {
			t.Fatalf("auto profile for %s must include definition edges", kind)
		}
	}
}

func TestEdgeTypesForAutoTreatsJobSetAsWorkload(t *testing.T) {
	if got, want := edgeTypesForAuto(KindJobSet), edgeTypesForAuto(KindCronJob); !maps.Equal(got, want) {
		t.Fatalf("auto profile for JobSet = %v, want the CronJob workload profile %v", got, want)
	}
}

// TestAPIVersionGroup pins the canonical split-on-first-slash extraction.
func TestAPIVersionGroup(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"v1", ""},
		{"apps/v1", "apps"},
		{"argoproj.io/v1alpha1", "argoproj.io"},
		{"networking.k8s.io/v1", "networking.k8s.io"},
		{"serving.knative.dev/v1", "serving.knative.dev"},
		{"", ""},
		{"/v1", ""},               // leading slash → empty group
		{"apps/v1/extra", "apps"}, // multi-slash → split on FIRST
	}
	for _, tc := range cases {
		if got := resourceid.GroupFromAPIVersion(tc.in); got != tc.want {
			t.Errorf("resourceid.GroupFromAPIVersion(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
