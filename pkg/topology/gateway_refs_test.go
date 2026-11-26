package topology

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestGatewayRouteEdgesHonorExplicitReferenceIdentity(t *testing.T) {
	gatewayGVR := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"}
	routeGVR := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}
	route := genericIdentityObject(routeGVR, "HTTPRoute", "team", "web")
	route.Object["spec"] = map[string]any{
		"parentRefs": []any{
			map[string]any{"group": "", "kind": "Service", "name": "shared"},
			map[string]any{"name": "edge", "sectionName": "http"},
			map[string]any{"name": "edge", "sectionName": "https"},
		},
		"rules": []any{
			map[string]any{"backendRefs": []any{
				map[string]any{"group": "custom.example.io", "kind": "Service", "name": "api", "port": int64(80)},
				map[string]any{"name": "web", "port": int64(80)},
			}},
			map[string]any{"backendRefs": []any{map[string]any{"name": "web", "port": int64(80)}}},
		},
	}
	dynamic := &genericIdentityDynamic{
		watched: []schema.GroupVersionResource{gatewayGVR, routeGVR},
		kinds:   map[schema.GroupVersionResource]string{gatewayGVR: "Gateway", routeGVR: "HTTPRoute"},
		resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{
			gatewayGVR: {
				genericIdentityObject(gatewayGVR, "Gateway", "team", "shared"),
				genericIdentityObject(gatewayGVR, "Gateway", "team", "edge"),
			},
			routeGVR: {route},
		},
		listCalls: map[schema.GroupVersionResource]int{},
	}
	provider := &mockProvider{services: []*corev1.Service{
		{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "team"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "team"}},
	}}

	for _, viewMode := range []ViewMode{ViewModeResources, ViewModeTraffic} {
		t.Run(string(viewMode), func(t *testing.T) {
			opts := DefaultBuildOptions()
			opts.ViewMode = viewMode
			topo, err := NewBuilder(provider).WithDynamic(dynamic).Build(opts)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			edges := map[string]bool{}
			for _, edge := range topo.Edges {
				key := edge.Source + " -> " + edge.Target
				if edges[key] {
					t.Errorf("duplicate edge %s", key)
				}
				edges[key] = true
			}
			for _, want := range []string{"gateway/team/edge -> httproute/team/web", "httproute/team/web -> service/team/web"} {
				if !edges[want] {
					t.Errorf("missing edge %s in %v", want, edges)
				}
			}
			for _, unwanted := range []string{"gateway/team/shared -> httproute/team/web", "httproute/team/web -> service/team/api"} {
				if edges[unwanted] {
					t.Errorf("joined explicit non-default reference: %s", unwanted)
				}
			}
			if viewMode == ViewModeTraffic && nodeByID(topo.Nodes, "service/team/api") != nil {
				t.Error("traffic view pulled in a core Service for a custom-group backendRef")
			}
		})
	}
}

func TestGatewayUsesCanonicalClassWhenNodePoolDrawsItToo(t *testing.T) {
	f := charCluster()
	group := "gateway.networking.k8s.io"
	pool := charUnstructured("karpenter.sh/v1", "NodePool", "", "alias", nil,
		map[string]any{"template": map[string]any{"spec": map[string]any{"nodeClassRef": map[string]any{
			"kind": "GatewayClass", "group": group, "name": "envoy",
		}}}}, nil)
	f.dynamic.add("karpenter.sh", "v1", "NodePool", "nodepools", false, pool)
	topo := charBuild(t, f, DefaultBuildOptions())
	const canonical = "gatewayclass//envoy"
	const alias = "nodeclass//envoy/gateway.networking.k8s.io/gatewayclass"
	const gateway = "gateway/shop/public"
	if nodeByID(topo.Nodes, alias) == nil || nodeByID(topo.Nodes, canonical) == nil {
		t.Fatal("expected both display nodes for GatewayClass envoy")
	}
	var count int
	for _, e := range topo.Edges {
		if e.Target == gateway && (e.Source == canonical || e.Source == alias) {
			count++
			if e.Source != canonical || e.Type != EdgeManages || e.ID != canonical+"-to-"+gateway {
				t.Errorf("class binding = %+v", e)
			}
		}
	}
	if count != 1 {
		t.Errorf("got %d class bindings, want one", count)
	}
}
