package topology

import (
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestKnativeRoutesExposeTheRevisionsTheirTrafficResolvesTo(t *testing.T) {
	routeGVR := schema.GroupVersionResource{Group: "serving.knative.dev", Version: "v1", Resource: "routes"}
	revisionGVR := schema.GroupVersionResource{Group: "serving.knative.dev", Version: "v1", Resource: "revisions"}
	route := genericIdentityObject(routeGVR, "Route", "app", "web")
	// status.traffic holds the resolved revisions; spec.traffic may only name
	// a configuration.
	route.Object["spec"] = map[string]any{"traffic": []any{map[string]any{"configurationName": "web", "percent": int64(100)}}}
	route.Object["status"] = map[string]any{"traffic": []any{
		map[string]any{"revisionName": "web-00002", "percent": int64(90)},
		map[string]any{"revisionName": "web-00001", "percent": int64(10)},
		// A tagged target can name a revision traffic already reaches; it is
		// still one edge.
		map[string]any{"revisionName": "web-00001", "tag": "canary", "percent": int64(0)},
		map[string]any{"revisionName": "web-gone", "percent": int64(0)},
	}}
	d := &genericIdentityDynamic{
		watched: []schema.GroupVersionResource{routeGVR, revisionGVR},
		kinds:   map[schema.GroupVersionResource]string{routeGVR: "Route", revisionGVR: "Revision"},
		resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{
			routeGVR:    {route},
			revisionGVR: {genericIdentityObject(revisionGVR, "Revision", "app", "web-00001"), genericIdentityObject(revisionGVR, "Revision", "app", "web-00002")},
		},
		listCalls: map[schema.GroupVersionResource]int{},
	}
	topo, err := NewBuilder(&mockProvider{}).WithDynamic(d).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	seen := map[string]bool{}
	for _, e := range topo.Edges {
		if e.Source == "knativeroute/app/web" {
			if seen[e.ID] {
				t.Errorf("%s drawn twice", e.ID)
			}
			seen[e.ID] = true
			if e.Type != EdgeExposes {
				t.Errorf("%s is %s, want exposes", e.ID, e.Type)
			}
			got = append(got, e.Target)
		}
	}
	slices.Sort(got)
	if want := []string{"knativerevision/app/web-00001", "knativerevision/app/web-00002"}; !slices.Equal(got, want) {
		t.Errorf("route exposes %v, want %v", got, want)
	}
}
