package relations

import (
	"encoding/json"
	"fmt"
	goruntime "runtime"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

type indexExample struct {
	family *Family
	obj    runtime.Object
}

func indexExamples() []indexExample {
	u := func(api, kind, spec string) runtime.Object {
		var fields map[string]any
		if err := json.Unmarshal([]byte(spec), &fields); err != nil {
			panic(err)
		}
		return object(api, kind, "example", fields)
	}
	class := "fast"
	examples := []indexExample{
		{GatewayParents, u("gateway.networking.k8s.io/v1", "HTTPRoute", `{"parentRefs":[{"name":"gw","namespace":"shared"}]}`)},
		{GatewayBackends, u("gateway.networking.k8s.io/v1", "HTTPRoute", `{"rules":[{"backendRefs":[{"name":"web","namespace":"shared","port":8080}]}]}`)},
		{GatewayClasses, u("gateway.networking.k8s.io/v1", "Gateway", `{"gatewayClassName":"public"}`)},
		{IstioGateways, u("networking.istio.io/v1", "VirtualService", `{"gateways":["shared/gw"]}`)},
		{IstioServiceHosts("cluster.local"), u("networking.istio.io/v1", "VirtualService", `{"http":[{"route":[{"destination":{"host":"db.shared.svc.custom.internal"}}]}]}`)},
		{IstioServiceHosts("cluster.local"), u("networking.istio.io/v1", "DestinationRule", `{"host":"db.shared.svc.custom.internal"}`)},
		{TraefikBackends, u("traefik.io/v1alpha1", "IngressRoute", `{"routes":[{"services":[{"name":"web","namespace":"shared","kind":"TraefikService"}]}]}`)},
		{TraefikRouteConfig, u("traefik.io/v1alpha1", "IngressRoute", `{"routes":[{"middlewares":[{"name":"auth","namespace":"shared"}]}]}`)},
		{ContourBackends, u("projectcontour.io/v1", "HTTPProxy", `{"includes":[{"name":"child","namespace":"shared"}]}`)},
		{ContourSecrets, u("projectcontour.io/v1", "HTTPProxy", `{"virtualhost":{"tls":{"secretName":"shared/tls"}}}`)},
		{KnativeEventDestinations, u("eventing.knative.dev/v1", "Trigger", `{"subscriber":{"ref":{"apiVersion":"v1","kind":"Service","name":"web","namespace":"shared"}}}`)},
		{KnativeTriggerBrokers, u("eventing.knative.dev/v1", "Trigger", `{"brokerRef":{"name":"default","namespace":"shared"}}`)},
		{FluxSources, u("kustomize.toolkit.fluxcd.io/v1", "Kustomization", `{"sourceRef":{"kind":"GitRepository","name":"app","namespace":"shared"}}`)},
		{CertManagerIssuers, u("cert-manager.io/v1", "Certificate", `{"issuerRef":{"name":"ca","kind":"ClusterIssuer"}}`)},
		{ClusterClasses, u("cluster.x-k8s.io/v1beta1", "Cluster", `{"topology":{"class":"base","classNamespace":"shared"}}`)},
		{ArgoWorkflowTemplates, u("argoproj.io/v1alpha1", "Workflow", `{"workflowTemplateRef":{"name":"base","clusterScope":true},"templates":[{"name":"run","dag":{"tasks":[{"name":"a","templateRef":{"name":"task","template":"run","clusterScope":true}}]}}]}`)},
		{KEDAAuthentication, u("keda.sh/v1alpha1", "ScaledObject", `{"triggers":[{"type":"prometheus","authenticationRef":{"name":"auth","kind":"ClusterTriggerAuthentication"}}]}`)},
		{NodeClasses, u("karpenter.sh/v1", "NodePool", `{"template":{"spec":{"nodeClassRef":{"group":"karpenter.k8s.aws","kind":"EC2NodeClass","name":"base"}}}}`)},
		{MachineNodes, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "karpenter.sh/v1", "kind": "NodeClaim", "metadata": map[string]any{"name": "example"}, "status": map[string]any{"nodeName": "node"}}}},
		{Storage, &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "example"}, Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "pv", StorageClassName: &class}}},
		{Storage, &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Name: "example"}, Spec: corev1.PersistentVolumeSpec{StorageClassName: class, ClaimRef: &corev1.ObjectReference{Name: "claim", Namespace: "shop"}}}},
		{Reflections, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "example", Annotations: map[string]string{"reflector.v1.k8s.emberstack.com/reflects": "shared/source"}}}},
	}
	for i := range examples {
		examples[i].family = candidateFamily(examples[i].family)
	}
	return examples
}

func BenchmarkRelationIndexKeys(b *testing.B) {
	for _, ex := range indexExamples() {
		kind := ex.obj.GetObjectKind().GroupVersionKind().Kind
		if kind == "" {
			kind = fmt.Sprintf("%T", ex.obj)
		}
		b.Run(ex.family.Name+"/"+kind, func(b *testing.B) {
			fn := ex.family.IndexFunc()
			b.ReportAllocs()
			for b.Loop() {
				keys, err := fn(ex.obj)
				if err != nil || len(keys) == 0 {
					b.Fatalf("no keys: %v", err)
				}
			}
		})
	}
}

func routeFixture(indexed bool) (stores, []any, []any) {
	s := stores{byKind: map[resourceid.GroupKind]cache.Indexer{}}
	for _, gk := range []resourceid.GroupKind{gatewayRoutes[0].Kind, kindVirtualService} {
		indexes := cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc}
		if indexed {
			for _, f := range []*Family{GatewayParents, GatewayBackends, IstioGateways, IstioServiceHosts("")} {
				if f.source(gk) >= 0 {
					indexes[f.ReferrersIndex.Name] = f.IndexFunc()
				}
			}
		}
		s.byKind[gk] = cache.NewIndexer(cache.MetaNamespaceKeyFunc, indexes)
	}
	var routes, virtualServices []any
	for i := range 7000 {
		var u *unstructured.Unstructured
		if i < 5000 {
			u = object("gateway.networking.k8s.io/v1", "HTTPRoute", fmt.Sprintf("route-%05d", i), map[string]any{"parentRefs": []any{map[string]any{"name": "gw", "namespace": "shared"}}, "rules": []any{map[string]any{"backendRefs": []any{map[string]any{"name": fmt.Sprintf("service-%d", i%100), "namespace": "shared"}}}}})
			routes = append(routes, u)
		} else {
			u = object("networking.istio.io/v1", "VirtualService", fmt.Sprintf("vs-%05d", i), map[string]any{"gateways": []any{"shared/mesh"}, "http": []any{map[string]any{"route": []any{map[string]any{"destination": map[string]any{"host": fmt.Sprintf("service-%d.shared.svc.cluster.local", i%100)}}}}}})
			virtualServices = append(virtualServices, u)
		}
		u.SetNamespace(fmt.Sprintf("ns-%03d", i%300))
		_ = s.byKind[refOf(u).GroupKind()].Add(u)
	}
	return s, routes, virtualServices
}

func BenchmarkRelationRouteReverse(b *testing.B) {
	for _, indexed := range []bool{false, true} {
		s, _, _ := routeFixture(indexed)
		for _, q := range []struct {
			f      *Family
			target resourceid.Ref
			count  int
		}{
			{GatewayParents, resourceid.NewRef("gateway.networking.k8s.io", "Gateway", "shared", "gw"), 5000},
			{GatewayBackends, resourceid.NewRef("", "Service", "shared", "service-7"), 50},
			{IstioServiceHosts("cluster.local"), resourceid.NewRef("", "Service", "shared", "service-7"), 20},
		} {
			f := *q.f
			if !indexed {
				f.ReferrersIndex = nil
			}
			b.Run(fmt.Sprintf("indexed=%t/%s", indexed, f.Name), func(b *testing.B) {
				v := NewView(s, ownership.Unrestricted()).WithLimit(0)
				b.ReportAllocs()
				for b.Loop() {
					if got := v.Referrers(&f, q.target, nil); len(got.Referrers) != q.count {
						b.Fatalf("got %d want %d", len(got.Referrers), q.count)
					}
				}
			})
		}
	}
}

func BenchmarkRelationRouteReplace(b *testing.B) {
	for _, indexed := range []bool{false, true} {
		s, routes, vs := routeFixture(indexed)
		b.Run(fmt.Sprintf("indexed=%t", indexed), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if err := s.byKind[gatewayRoutes[0].Kind].Replace(routes, ""); err != nil {
					b.Fatal(err)
				}
				if err := s.byKind[kindVirtualService].Replace(vs, ""); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkRelationRouteMemory(b *testing.B) {
	for _, indexed := range []bool{false, true} {
		b.Run(fmt.Sprintf("indexed=%t", indexed), func(b *testing.B) {
			var held int64
			for b.Loop() {
				goruntime.GC()
				var before, after goruntime.MemStats
				goruntime.ReadMemStats(&before)
				s, _, _ := routeFixture(indexed)
				goruntime.GC()
				goruntime.ReadMemStats(&after)
				goruntime.KeepAlive(s)
				held += int64(after.HeapAlloc) - int64(before.HeapAlloc)
			}
			b.ReportMetric(float64(held)/float64(b.N), "held-B")
		})
	}
}

func BenchmarkRelationIndexUpdate(b *testing.B) {
	for _, ex := range indexExamples() {
		kind := ex.obj.GetObjectKind().GroupVersionKind().Kind
		if kind == "" {
			kind = fmt.Sprintf("%T", ex.obj)
		}
		for _, indexed := range []bool{false, true} {
			indexes := cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc}
			if indexed {
				indexes[ex.family.ReferrersIndex.Name] = ex.family.IndexFunc()
			}
			st := cache.NewIndexer(cache.MetaNamespaceKeyFunc, indexes)
			if err := st.Add(ex.obj); err != nil {
				b.Fatal(err)
			}
			b.Run(fmt.Sprintf("%s/%s/indexed=%t", ex.family.Name, kind, indexed), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if err := st.Update(ex.obj); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
