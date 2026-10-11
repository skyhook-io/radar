package topology

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestServersTransportConfiguresRoutesWithoutReplacingTheirBackends(t *testing.T) {
	routeGVR := schema.GroupVersionResource{Group: "traefik.io", Version: "v1alpha1", Resource: "ingressroutes"}
	transportGVR := schema.GroupVersionResource{Group: "traefik.io", Version: "v1alpha1", Resource: "serverstransports"}
	route := func(name, backend string) *unstructured.Unstructured {
		obj := genericIdentityObject(routeGVR, "IngressRoute", "team", name)
		obj.Object["spec"] = map[string]any{"routes": []any{map[string]any{
			"match":    "Host(`" + name + ".example.com`)",
			"services": []any{map[string]any{"name": backend, "port": int64(443), "serversTransport": "mtls"}},
		}}}
		return obj
	}
	dynamic := &genericIdentityDynamic{
		watched: []schema.GroupVersionResource{routeGVR, transportGVR},
		kinds:   map[schema.GroupVersionResource]string{routeGVR: "IngressRoute", transportGVR: "ServersTransport"},
		resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{
			routeGVR:     {route("web", "web"), route("api", "api"), route("orphan", "missing")},
			transportGVR: {genericIdentityObject(transportGVR, "ServersTransport", "team", "mtls")},
		},
		listCalls: map[schema.GroupVersionResource]int{},
	}
	provider := &mockProvider{services: []*corev1.Service{
		{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "team"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "team"}},
	}}
	topo, err := NewBuilder(provider).WithDynamic(dynamic).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	edges := map[string]EdgeType{}
	for _, edge := range topo.Edges {
		edges[edge.Source+" -> "+edge.Target] = edge.Type
	}
	want := map[string]EdgeType{
		"ingressroute/team/web -> service/team/web":           EdgeExposes,
		"ingressroute/team/api -> service/team/api":           EdgeExposes,
		"serverstransport/team/mtls -> ingressroute/team/web": EdgeConfigures,
		"serverstransport/team/mtls -> ingressroute/team/api": EdgeConfigures,
		// The transport configures the route even with its backend missing.
		"serverstransport/team/mtls -> ingressroute/team/orphan": EdgeConfigures,
	}
	for edge, typ := range want {
		if edges[edge] != typ {
			t.Errorf("edge %s = %q, want %q", edge, edges[edge], typ)
		}
	}
	for edge := range edges {
		if edge == "serverstransport/team/mtls -> service/team/web" || edge == "serverstransport/team/mtls -> service/team/api" {
			t.Errorf("transport drawn as a hop to a backend: %s", edge)
		}
	}

	web := GetRelationshipsWithObject("Service", "team", "web", nil, topo, nil, nil, IndexByResource(topo))
	if web == nil || len(web.Routes) != 1 || web.Routes[0].Name != "web" {
		t.Errorf("Service web routes = %+v, want its IngressRoute", web)
	}
}

func TestServersTransportResolvesWhereTraefikDoes(t *testing.T) {
	routeGVR := schema.GroupVersionResource{Group: "traefik.io", Version: "v1alpha1", Resource: "ingressroutes"}
	transportGVR := schema.GroupVersionResource{Group: "traefik.io", Version: "v1alpha1", Resource: "serverstransports"}
	traefikServiceGVR := schema.GroupVersionResource{Group: "traefik.io", Version: "v1alpha1", Resource: "traefikservices"}
	route := func(name string, backend map[string]any) *unstructured.Unstructured {
		obj := genericIdentityObject(routeGVR, "IngressRoute", "edge", name)
		obj.Object["spec"] = map[string]any{"routes": []any{map[string]any{
			"match":    "Host(`" + name + ".example.com`)",
			"services": []any{backend},
		}}}
		return obj
	}
	dynamic := &genericIdentityDynamic{
		watched: []schema.GroupVersionResource{routeGVR, transportGVR, traefikServiceGVR},
		kinds:   map[schema.GroupVersionResource]string{routeGVR: "IngressRoute", transportGVR: "ServersTransport", traefikServiceGVR: "TraefikService"},
		resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{
			routeGVR: {
				// An HTTP backend's transport lives in the backend's namespace.
				route("web", map[string]any{"name": "web", "namespace": "apps", "port": int64(443), "serversTransport": "mtls"}),
				// Traefik ignores serversTransport on a TraefikService backend.
				route("weighted", map[string]any{"name": "split", "kind": "TraefikService", "serversTransport": "mtls"}),
			},
			transportGVR: {
				genericIdentityObject(transportGVR, "ServersTransport", "edge", "mtls"),
				genericIdentityObject(transportGVR, "ServersTransport", "apps", "mtls"),
			},
			traefikServiceGVR: {genericIdentityObject(traefikServiceGVR, "TraefikService", "edge", "split")},
		},
		listCalls: map[schema.GroupVersionResource]int{},
	}
	provider := &mockProvider{services: []*corev1.Service{{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "apps"}}}}
	topo, err := NewBuilder(provider).WithDynamic(dynamic).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	edges := map[string]bool{}
	for _, edge := range topo.Edges {
		edges[edge.Source+" -> "+edge.Target] = true
	}
	if !edges["serverstransport/apps/mtls -> ingressroute/edge/web"] {
		t.Errorf("web's transport not resolved in its backend's namespace: %v", edges)
	}
	for _, wrong := range []string{
		"serverstransport/edge/mtls -> ingressroute/edge/web",
		"serverstransport/edge/mtls -> ingressroute/edge/weighted",
		"serverstransport/apps/mtls -> ingressroute/edge/weighted",
	} {
		if edges[wrong] {
			t.Errorf("unexpected transport edge %s", wrong)
		}
	}
}
