package cachesource

import (
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakediscovery "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

var (
	httpRoutes      = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}
	gatewayAPIs     = schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"}
	virtualServices = schema.GroupVersionResource{Group: "networking.istio.io", Version: "v1", Resource: "virtualservices"}
)

// routeCaches are real caches over 5,000 HTTPRoutes and 2,000
// VirtualServices in 300 namespaces, every route attached to one Gateway and
// sending to one of 100 Services.
func routeCaches(b *testing.B) *fixture {
	b.Helper()
	unstructuredObject := func(apiVersion, kind, namespace, name string, spec map[string]any) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": map[string]any{"namespace": namespace, "name": name}, "spec": spec}}
	}
	var objects []runtime.Object
	for i := range 7000 {
		ns := fmt.Sprintf("ns-%03d", i%300)
		if i < 5000 {
			objects = append(objects, unstructuredObject("gateway.networking.k8s.io/v1", "HTTPRoute", ns, fmt.Sprintf("route-%05d", i), map[string]any{
				"parentRefs": []any{map[string]any{"name": "gw", "namespace": "shared"}},
				"rules":      []any{map[string]any{"backendRefs": []any{map[string]any{"name": fmt.Sprintf("service-%d", i%100), "namespace": "shared"}}}},
			}))
		} else {
			objects = append(objects, unstructuredObject("networking.istio.io/v1", "VirtualService", ns, fmt.Sprintf("vs-%05d", i), map[string]any{
				"http": []any{map[string]any{"route": []any{map[string]any{"destination": map[string]any{"host": fmt.Sprintf("service-%d.shared.svc.cluster.local", i%100)}}}}},
			}))
		}
	}
	f := newFixture(b, k8score.CacheConfig{ResourceTypes: map[string]bool{k8score.Pods: true, k8score.Services: true}}, func(f *fixture) {
		for i := range 100 {
			if err := f.typed.Tracker().Add(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "shared", Name: fmt.Sprintf("service-%d", i)}}); err != nil {
				b.Fatal(err)
			}
		}
		verbs := []string{"get", "list", "watch"}
		d := f.typed.Discovery().(*fakediscovery.FakeDiscovery)
		d.Resources = append(d.Resources,
			&metav1.APIResourceList{GroupVersion: "gateway.networking.k8s.io/v1", APIResources: []metav1.APIResource{
				{Name: "httproutes", Kind: "HTTPRoute", Namespaced: true, Verbs: verbs},
				{Name: "gateways", Kind: "Gateway", Namespaced: true, Verbs: verbs},
			}},
			&metav1.APIResourceList{GroupVersion: "networking.istio.io/v1", APIResources: []metav1.APIResource{
				{Name: "virtualservices", Kind: "VirtualService", Namespaced: true, Verbs: verbs},
			}})
		f.dynamic = dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
			widgets: "WidgetList", httpRoutes: "HTTPRouteList", gatewayAPIs: "GatewayList", virtualServices: "VirtualServiceList",
		}, objects...)
		// The fake would guess the Gateway's resource as "gatewaies".
		gw := unstructuredObject("gateway.networking.k8s.io/v1", "Gateway", "shared", "gw", map[string]any{"gatewayClassName": "public"})
		if err := f.dynamic.Tracker().Create(gatewayAPIs, gw, "shared"); err != nil {
			b.Fatal(err)
		}
	})
	for _, gvr := range []schema.GroupVersionResource{httpRoutes, gatewayAPIs, virtualServices} {
		if err := f.caches.Dynamic.EnsureWatching(gvr); err != nil {
			b.Fatal(err)
		}
		if !f.caches.Dynamic.WaitForSync(gvr, time.Minute) {
			b.Fatalf("%v did not sync", gvr)
		}
	}
	return f
}

// BenchmarkRelationRouteReverse answers who refers to a Gateway or Service
// through the cache source's reads of real stores, by the family's index and
// by reading every source.
func BenchmarkRelationRouteReverse(b *testing.B) {
	f := routeCaches(b)
	gateway := resourceid.NewRef("gateway.networking.k8s.io", "Gateway", "shared", "gw")
	service := resourceid.NewRef("", "Service", "shared", "service-7")
	for _, indexed := range []bool{false, true} {
		for _, q := range []struct {
			f      *relations.Family
			target resourceid.Ref
			count  int
		}{
			{relations.GatewayParents, gateway, 5000},
			{relations.GatewayBackends, service, 50},
			{relations.IstioServiceHosts("cluster.local"), service, 20},
		} {
			family := *q.f
			if !indexed {
				family.ReferrersIndex = nil
			}
			b.Run(fmt.Sprintf("indexed=%t/%s", indexed, family.Name), func(b *testing.B) {
				v := relations.NewView(begin(b, f.source()).(relations.Session), ownership.Unrestricted()).WithLimit(0)
				b.ReportAllocs()
				for b.Loop() {
					if got := v.Referrers(&family, q.target, nil); len(got.Referrers) != q.count || !got.Coverage.Complete() {
						b.Fatalf("got %d referrers, coverage %+v; want %d, complete", len(got.Referrers), got.Coverage, q.count)
					}
				}
			})
		}
	}
}
