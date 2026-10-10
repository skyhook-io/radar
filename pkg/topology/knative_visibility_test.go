package topology

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestTrafficInternetReachesOnlyPublicKnativeServices(t *testing.T) {
	ksvcGVR := schema.GroupVersionResource{Group: "serving.knative.dev", Version: "v1", Resource: "services"}
	ksvc := func(name, statusURL string, labels map[string]string) *unstructured.Unstructured {
		obj := genericIdentityObject(ksvcGVR, "Service", "team", name)
		obj.SetLabels(labels)
		obj.Object["status"] = map[string]any{"url": statusURL}
		return obj
	}
	dynamic := &genericIdentityDynamic{
		watched: []schema.GroupVersionResource{ksvcGVR},
		kinds:   map[schema.GroupVersionResource]string{ksvcGVR: "Service"},
		resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{ksvcGVR: {
			ksvc("web", "https://web.team.example.com", nil),
			// The label alone decides, even before the Route publishes a private URL.
			ksvc("billing", "https://billing.team.example.com", map[string]string{"networking.knative.dev/visibility": "cluster-local"}),
			// Made private through its Route: only the published URL says so.
			ksvc("jobs", "http://jobs.team.svc.cluster.local", nil),
		}},
		listCalls: map[schema.GroupVersionResource]int{},
	}
	opts := DefaultBuildOptions()
	opts.ViewMode = ViewModeTraffic
	topo, err := NewBuilder(&mockProvider{}).WithDynamic(dynamic).Build(opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	internet := map[string]bool{}
	for _, edge := range topo.Edges {
		if edge.Source == "internet" {
			internet[edge.Target] = true
		}
	}
	if !internet["knativeservice/team/web"] {
		t.Errorf("public Knative Service lost its Internet edge: %v", internet)
	}
	for _, private := range []string{"knativeservice/team/billing", "knativeservice/team/jobs"} {
		if internet[private] {
			t.Errorf("cluster-local %s drawn as reachable from the Internet", private)
		}
	}
}
