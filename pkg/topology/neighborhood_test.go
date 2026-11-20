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

// An empty or unknown profile reads as auto, never as all: a typo can't
// broaden a walk to every edge type.
func TestEdgeTypesForProfileUnknownIsAuto(t *testing.T) {
	for _, kind := range []NodeKind{KindPod, KindService, KindKustomization, KindNetworkPolicy} {
		auto := edgeTypesForProfile(ProfileAuto, kind)
		for _, profile := range []Profile{"", "banana"} {
			if got := edgeTypesForProfile(profile, kind); !maps.Equal(got, auto) {
				t.Errorf("%s profile %q = %v, want auto's %v", kind, profile, got, auto)
			}
		}
	}
	if all, auto := edgeTypesForProfile(ProfileAll, KindPod), edgeTypesForProfile(ProfileAuto, KindPod); len(all) <= len(auto) {
		t.Fatalf("the fixture no longer tells all from auto for a Pod: all %v, auto %v", all, auto)
	}
}

// A group-less lookup of a built-in kind finds only the built-in: Volcano's
// Job is not a Job.
func TestFindNodeByRefGroupLessBuiltinSkipsCustomKind(t *testing.T) {
	volcano := Node{ID: "job/ml/train/batch.volcano.sh", Kind: KindJob, Name: "train", Data: map[string]any{"namespace": "ml", "apiVersion": "batch.volcano.sh/v1alpha1"}}
	if got, ambiguous := findNodeByRef([]Node{volcano}, ResourceRef{Kind: "Job", Namespace: "ml", Name: "train"}, nil); got != nil || ambiguous {
		t.Fatalf("group-less Job resolved a Volcano Job: %+v ambiguous=%v", got, ambiguous)
	}
	if got, _ := findNodeByRef([]Node{volcano}, ResourceRef{Kind: "Job", Namespace: "ml", Name: "train", Group: "batch.volcano.sh"}, nil); got == nil {
		t.Fatal("the Volcano Job isn't found by its own group")
	}
}

// A Knative Service is found by its group whatever the kind's spelling, as
// MCP (Service) and REST (service, services) send it, and never as the core
// Service of the same name.
func TestFindNodeByRefKnativeServiceAnySpelling(t *testing.T) {
	knative := Node{ID: "knativeservice/prod/api", Kind: KindKnativeService, Name: "api", Data: map[string]any{"namespace": "prod", "apiVersion": "serving.knative.dev/v1"}}
	core := Node{ID: "service/prod/api", Kind: KindService, Name: "api", Data: map[string]any{"namespace": "prod", "apiVersion": "v1"}}
	for _, kind := range []string{"Service", "service", "services"} {
		got, _ := findNodeByRef([]Node{core, knative}, ResourceRef{Kind: kind, Namespace: "prod", Name: "api", Group: "serving.knative.dev"}, nil)
		if got == nil || got.ID != knative.ID {
			t.Errorf("kind %q found %+v, want the Knative Service", kind, got)
		}
	}
}
