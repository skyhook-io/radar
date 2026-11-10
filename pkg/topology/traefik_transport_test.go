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

func TestTraefikTLSMaterialEdgesFollowTraefiksFields(t *testing.T) {
	routeGVR := schema.GroupVersionResource{Group: "traefik.io", Version: "v1alpha1", Resource: "ingressroutes"}
	httpGVR := schema.GroupVersionResource{Group: "traefik.io", Version: "v1alpha1", Resource: "serverstransports"}
	tcpGVR := schema.GroupVersionResource{Group: "traefik.io", Version: "v1alpha1", Resource: "serverstransporttcps"}
	storeGVR := schema.GroupVersionResource{Group: "traefik.io", Version: "v1alpha1", Resource: "tlsstores"}

	httpTransport := genericIdentityObject(httpGVR, "ServersTransport", "team", "mtls")
	httpTransport.Object["spec"] = map[string]any{
		"rootCAs":             []any{map[string]any{"configMap": "ca-bundle"}, map[string]any{"secret": "ca-secret"}},
		"certificatesSecrets": []any{"client-cert"},
	}
	// ServersTransportTCP keeps its TLS material under spec.tls.
	tcpTransport := genericIdentityObject(tcpGVR, "ServersTransportTCP", "team", "tcp-tls")
	tcpTransport.Object["spec"] = map[string]any{
		"tls": map[string]any{
			"rootCAs":             []any{map[string]any{"configMap": "tcp-ca-bundle"}},
			"rootCAsSecrets":      []any{"tcp-ca"},
			"certificatesSecrets": []any{"tcp-client"},
		},
		"certificatesSecrets": []any{"not-a-tcp-field"},
	}
	store := genericIdentityObject(storeGVR, "TLSStore", "team", "default")
	store.Object["spec"] = map[string]any{
		"defaultCertificate": map[string]any{"secretName": "default-cert"},
		"certificates":       []any{map[string]any{"secretName": "extra-cert"}},
	}
	route := genericIdentityObject(routeGVR, "IngressRoute", "team", "web")
	route.Object["spec"] = map[string]any{
		"routes": []any{map[string]any{"match": "Host(`web.example.com`)", "services": []any{map[string]any{"name": "web", "port": int64(80)}}}},
		"tls":    map[string]any{"secretName": "web-cert", "store": map[string]any{"name": "default"}},
	}
	dynamic := &genericIdentityDynamic{
		watched: []schema.GroupVersionResource{routeGVR, httpGVR, tcpGVR, storeGVR},
		kinds:   map[schema.GroupVersionResource]string{routeGVR: "IngressRoute", httpGVR: "ServersTransport", tcpGVR: "ServersTransportTCP", storeGVR: "TLSStore"},
		resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{
			routeGVR: {route}, httpGVR: {httpTransport}, tcpGVR: {tcpTransport}, storeGVR: {store},
		},
		listCalls: map[schema.GroupVersionResource]int{},
	}
	provider := &mockProvider{services: []*corev1.Service{{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "team"}}}}
	topo, err := NewBuilder(provider).WithDynamic(dynamic).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	edges := map[string]EdgeType{}
	for _, edge := range topo.Edges {
		edges[edge.Source+" -> "+edge.Target] = edge.Type
	}
	for _, want := range []string{
		"configmap/team/ca-bundle -> serverstransport/team/mtls",
		"secret/team/ca-secret -> serverstransport/team/mtls",
		"secret/team/client-cert -> serverstransport/team/mtls",
		"configmap/team/tcp-ca-bundle -> serverstransporttcp/team/tcp-tls",
		"secret/team/tcp-ca -> serverstransporttcp/team/tcp-tls",
		"secret/team/tcp-client -> serverstransporttcp/team/tcp-tls",
		"secret/team/default-cert -> tlsstore/team/default",
		"secret/team/extra-cert -> tlsstore/team/default",
	} {
		if edges[want] != EdgeConfigures {
			t.Errorf("missing configures edge %s", want)
		}
	}
	for _, unwanted := range []string{
		"secret/team/not-a-tcp-field -> serverstransporttcp/team/tcp-tls",
		"tlsstore/team/default -> ingressroute/team/web",
	} {
		if _, ok := edges[unwanted]; ok {
			t.Errorf("unexpected edge %s", unwanted)
		}
	}
}
