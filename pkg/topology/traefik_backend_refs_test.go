package topology

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestTraefikBackendKindsInBothViews(t *testing.T) {
	for _, mode := range []ViewMode{ViewModeResources, ViewModeTraffic} {
		for _, backendKind := range []string{"", "Service", "Other"} {
			t.Run(string(mode)+"/"+backendKind, func(t *testing.T) {
				routeGVR := schema.GroupVersionResource{Group: "traefik.io", Version: "v1alpha1", Resource: "ingressroutes"}
				tsGVR := schema.GroupVersionResource{Group: "traefik.io", Version: "v1alpha1", Resource: "traefikservices"}
				backend := map[string]any{"name": "web", "kind": backendKind, "port": int64(80)}
				route := genericIdentityObject(routeGVR, "IngressRoute", "team", "route")
				route.Object["spec"] = map[string]any{"routes": []any{map[string]any{"match": "PathPrefix(`/`)", "services": []any{backend}}}}
				ts := genericIdentityObject(tsGVR, "TraefikService", "team", "split")
				ts.Object["spec"] = map[string]any{
					"weighted":            map[string]any{"services": []any{backend}},
					"highestRandomWeight": map[string]any{"services": []any{backend}},
					"mirroring":           map[string]any{"name": "web", "kind": backendKind, "port": int64(80), "mirrors": []any{backend}},
				}
				dynamic := &genericIdentityDynamic{
					watched:   []schema.GroupVersionResource{routeGVR, tsGVR},
					kinds:     map[schema.GroupVersionResource]string{routeGVR: "IngressRoute", tsGVR: "TraefikService"},
					resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{routeGVR: {route}, tsGVR: {ts}},
					listCalls: map[schema.GroupVersionResource]int{},
				}
				provider := &mockProvider{services: []*corev1.Service{{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "team"}}}}
				opts := DefaultBuildOptions()
				opts.ViewMode = mode
				topo, err := NewBuilder(provider).WithDynamic(dynamic).Build(opts)
				if err != nil {
					t.Fatal(err)
				}
				for _, source := range []string{"ingressroute/team/route", "traefikservice/team/split"} {
					count := 0
					for _, edge := range topo.Edges {
						if edge.Source == source && edge.Target == "service/team/web" && edge.Type == EdgeExposes {
							count++
						}
					}
					want := 1
					if backendKind == "Other" {
						want = 0
					}
					if count != want {
						t.Errorf("%s to Service has %d edges, want %d", source, count, want)
					}
				}
			})
		}
	}
}
