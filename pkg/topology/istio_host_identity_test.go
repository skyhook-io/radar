package topology

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"reflect"
	"testing"
)

func TestIstioHostsJoinOnlyKubernetesServiceNames(t *testing.T) {
	vsGVR := schema.GroupVersionResource{Group: "networking.istio.io", Version: "v1", Resource: "virtualservices"}
	drGVR := schema.GroupVersionResource{Group: "networking.istio.io", Version: "v1", Resource: "destinationrules"}
	for _, tc := range []struct{ host, target string }{
		{"api", "service/rules/api"}, {"api.example.svc.cluster.local", "service/example/api"},
		{"api.example.com", ""}, {"api.example", ""}, {"api.example.svc", ""},
		{"api.example.svc.other.local", ""}, {"*.example.svc.cluster.local", ""}, {"*", ""}, {"", ""},
	} {
		for _, view := range []ViewMode{ViewModeResources, ViewModeTraffic} {
			t.Run(tc.host+"/"+string(view), func(t *testing.T) {
				vs := genericIdentityObject(vsGVR, "VirtualService", "rules", "route")
				vs.Object["spec"] = map[string]any{"hosts": []any{tc.host}, "http": []any{map[string]any{"route": []any{map[string]any{"destination": map[string]any{"host": tc.host}}}}}}
				dr := genericIdentityObject(drGVR, "DestinationRule", "rules", "policy")
				dr.Object["spec"] = map[string]any{"host": tc.host}
				d := &genericIdentityDynamic{watched: []schema.GroupVersionResource{vsGVR, drGVR}, kinds: map[schema.GroupVersionResource]string{vsGVR: "VirtualService", drGVR: "DestinationRule"}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{vsGVR: {vs}, drGVR: {dr}}, listCalls: map[schema.GroupVersionResource]int{}}
				provider := &mockProvider{services: []*corev1.Service{{ObjectMeta: metav1.ObjectMeta{Namespace: "rules", Name: "api"}}, {ObjectMeta: metav1.ObjectMeta{Namespace: "example", Name: "api"}}}}
				opts := DefaultBuildOptions()
				opts.ViewMode = view
				topo, err := NewBuilder(provider).WithDynamic(d).Build(opts)
				if err != nil {
					t.Fatal(err)
				}
				for _, id := range []string{"virtualservice/rules/route", "destinationrule/rules/policy"} {
					if view == ViewModeTraffic && id == "destinationrule/rules/policy" {
						continue
					}
					var targets []string
					for _, e := range topo.Edges {
						if e.Source == id {
							targets = append(targets, e.Target)
						}
					}
					if tc.target == "" {
						if len(targets) > 0 {
							t.Errorf("%s joined %v for %q", id, targets, tc.host)
						}
					} else if len(targets) != 1 || targets[0] != tc.target {
						t.Errorf("%s targets=%v want %s", id, targets, tc.target)
					}
				}
				if view == ViewModeTraffic && tc.target == "" {
					for _, id := range []string{"service/rules/api", "service/example/api"} {
						if nodeByID(topo.Nodes, id) != nil {
							t.Errorf("invalid host pulled in %s", id)
						}
					}
				}
			})
		}
	}
}

// A Service an HTTP and a TCP route both reach gets one edge; edges follow the
// route order, HTTP before TCP before TLS.
func TestIstioRoutesDrawOneEdgePerServiceInProtocolOrder(t *testing.T) {
	vsGVR := schema.GroupVersionResource{Group: "networking.istio.io", Version: "v1", Resource: "virtualservices"}
	route := func(host string) []any {
		return []any{map[string]any{"route": []any{map[string]any{"destination": map[string]any{"host": host}}}}}
	}
	vs := genericIdentityObject(vsGVR, "VirtualService", "rules", "route")
	vs.Object["spec"] = map[string]any{"http": route("web"), "tcp": append(route("db"), route("web")...), "tls": route("vault")}
	d := &genericIdentityDynamic{watched: []schema.GroupVersionResource{vsGVR}, kinds: map[schema.GroupVersionResource]string{vsGVR: "VirtualService"}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{vsGVR: {vs}}, listCalls: map[schema.GroupVersionResource]int{}}
	var services []*corev1.Service
	for _, name := range []string{"web", "db", "vault"} {
		services = append(services, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "rules", Name: name}})
	}
	topo, err := NewBuilder(&mockProvider{services: services}).WithDynamic(d).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range topo.Edges {
		if e.Source == "virtualservice/rules/route" {
			got = append(got, e.ID)
		}
	}
	want := []string{"virtualservice/rules/route-to-service/rules/web", "virtualservice/rules/route-to-service/rules/db", "virtualservice/rules/route-to-service/rules/vault"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("edges = %v, want %v", got, want)
	}
}

func TestTrafficViewDrawsTheIstioGatewaysAVirtualServiceBindsTo(t *testing.T) {
	vsGVR := schema.GroupVersionResource{Group: "networking.istio.io", Version: "v1", Resource: "virtualservices"}
	gwGVR := schema.GroupVersionResource{Group: "networking.istio.io", Version: "v1", Resource: "gateways"}
	vs := genericIdentityObject(vsGVR, "VirtualService", "rules", "route")
	vs.Object["spec"] = map[string]any{"gateways": []any{"mesh", "edge", "infra/shared"}, "http": []any{map[string]any{"route": []any{map[string]any{"destination": map[string]any{"host": "web"}}}}}}
	gateways := []*unstructured.Unstructured{genericIdentityObject(gwGVR, "Gateway", "rules", "edge"), genericIdentityObject(gwGVR, "Gateway", "infra", "shared"), genericIdentityObject(gwGVR, "Gateway", "rules", "mesh")}
	d := &genericIdentityDynamic{watched: []schema.GroupVersionResource{vsGVR, gwGVR}, kinds: map[schema.GroupVersionResource]string{vsGVR: "VirtualService", gwGVR: "Gateway"}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{vsGVR: {vs}, gwGVR: gateways}, listCalls: map[schema.GroupVersionResource]int{}}
	opts := DefaultBuildOptions()
	opts.ViewMode = ViewModeTraffic
	topo, err := NewBuilder(&mockProvider{services: []*corev1.Service{{ObjectMeta: metav1.ObjectMeta{Namespace: "rules", Name: "web"}}}}).WithDynamic(d).Build(opts)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range topo.Edges {
		if e.Target == "virtualservice/rules/route" {
			got = append(got, e.Source)
		}
	}
	want := []string{"istiogateway/rules/edge", "istiogateway/infra/shared"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("gateways bound = %v, want %v (mesh binds sidecars)", got, want)
	}
}
