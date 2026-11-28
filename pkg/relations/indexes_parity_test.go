package relations

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"

	"github.com/skyhook-io/radar/pkg/configrefs"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// registeredStores is a Session over informer-style stores that carry the
// indexes ReferrerIndexers registers for their kind, as the caches' stores
// do, read by those indexes and in the cache source's order. Every subject
// asked about is held; any other object is present only if a store holds it.
type registeredStores struct {
	cluster
	byKind map[resourceid.GroupKind]cache.Indexer
}

func newRegisteredStores(t testing.TB, objects []runtime.Object) registeredStores {
	t.Helper()
	s := registeredStores{cluster: cluster{objects: objects}, byKind: map[resourceid.GroupKind]cache.Indexer{}}
	for _, obj := range objects {
		gk := refOf(obj).GroupKind()
		if s.byKind[gk] == nil {
			indexers := cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc}
			maps.Copy(indexers, ReferrerIndexers[schema.GroupResource{Group: gk.Group, Resource: sourceResources[gk.Kind]}])
			s.byKind[gk] = cache.NewIndexer(cache.MetaNamespaceKeyFunc, indexers)
		}
		if err := s.byKind[gk].Add(obj); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func (registeredStores) Borrow(resourceid.Ref) (runtime.Object, ownership.ObjectRead) {
	return nil, ownership.ObjectRead{State: ownership.Present}
}

func (s registeredStores) Indexed(gk resourceid.GroupKind, index string, keys []string, scope ownership.SearchScope) IndexedRead {
	var r IndexedRead
	st := s.byKind[gk]
	if st == nil {
		return r
	}
	items := st.List()
	if index != "" {
		items = nil
		for _, key := range keys {
			found, err := st.ByIndex(index, key)
			if err != nil {
				return IndexedRead{Gaps: []ownership.Gap{{Kind: gk, Reason: readreason.ReadError}}}
			}
			items = append(items, found...)
		}
	}
	for _, item := range items {
		obj := item.(runtime.Object)
		ref := refOf(obj)
		inScope := scope.IncludeClusterScoped
		if ref.Namespace != "" {
			inScope = scope.AllNamespaces || slices.Contains(scope.Namespaces, ref.Namespace)
		}
		if inScope && !slices.ContainsFunc(r.Objects, func(o IndexedObject) bool { return o.Ref == ref }) {
			r.Objects = append(r.Objects, IndexedObject{Ref: ref, Object: obj})
		}
	}
	slices.SortFunc(r.Objects, func(a, b IndexedObject) int {
		return cmp.Or(cmp.Compare(a.Ref.Group, b.Ref.Group), cmp.Compare(a.Ref.Kind, b.Ref.Kind), cmp.Compare(a.Ref.Namespace, b.Ref.Namespace), cmp.Compare(a.Ref.Name, b.Ref.Name))
	})
	return r
}

// referrerIndexSources are sources of every kind an index is registered for,
// in several namespaces, naming targets locally, across namespaces and
// cluster-wide, with alternative and superset keys.
func referrerIndexSources() []runtime.Object {
	u := func(apiVersion, kind, namespace, name, body string) runtime.Object {
		var fields map[string]any
		if err := json.Unmarshal([]byte(body), &fields); err != nil {
			panic(err)
		}
		fields["apiVersion"], fields["kind"] = apiVersion, kind
		fields["metadata"] = map[string]any{"namespace": namespace, "name": name}
		return &unstructured.Unstructured{Object: fields}
	}
	mirror := func(obj runtime.Object, kind, namespace, name, reflects string) runtime.Object {
		m := metav1.ObjectMeta{Namespace: namespace, Name: name, Annotations: map[string]string{configrefs.ReflectsAnnotation: reflects}}
		switch o := obj.(type) {
		case *corev1.Secret:
			o.ObjectMeta = m
		case *corev1.ConfigMap:
			o.ObjectMeta = m
		}
		return typed(obj, "", "v1", kind)
	}
	const gw = "gateway.networking.k8s.io/v1"
	return []runtime.Object{
		u("keda.sh/v1alpha1", "ScaledObject", "shop", "worker", `{"spec":{"triggers":[{"authenticationRef":{"name":"auth"}},{"authenticationRef":{"kind":"ClusterTriggerAuthentication","name":"shared-creds"}}]}}`),
		u("keda.sh/v1alpha1", "ScaledObject", "team", "worker", `{"spec":{"triggers":[{"authenticationRef":{"name":"auth"}}]}}`),
		u("keda.sh/v1alpha1", "ScaledJob", "team", "batch", `{"spec":{"triggers":[{"authenticationRef":{"name":"auth"}},{"authenticationRef":{"kind":"ClusterTriggerAuthentication","name":"shared-creds"}}]}}`),

		u(gw, "HTTPRoute", "shop", "web", `{"spec":{"parentRefs":[{"name":"gw","namespace":"shared"},{"name":"local"}],"rules":[{"backendRefs":[{"name":"web"},{"name":"api","namespace":"shared","port":80}]}]}}`),
		u(gw, "GRPCRoute", "team", "grpc", `{"spec":{"parentRefs":[{"name":"gw","namespace":"shared"}],"rules":[{"backendRefs":[{"name":"api","namespace":"shared"}]}]}}`),
		u("gateway.networking.k8s.io/v1alpha2", "TCPRoute", "shop", "tcp", `{"spec":{"parentRefs":[{"name":"gw","namespace":"shared","sectionName":"tcp"}],"rules":[{"backendRefs":[{"name":"db","port":5432}]}]}}`),
		u("gateway.networking.k8s.io/v1alpha2", "TLSRoute", "team", "tls", `{"spec":{"parentRefs":[{"name":"gw","namespace":"shared"}],"rules":[{"backendRefs":[{"name":"web","namespace":"shop"}]}]}}`),
		u("gateway.networking.k8s.io/v1alpha2", "UDPRoute", "shared", "dns", `{"spec":{"parentRefs":[{"name":"gw"}],"rules":[{"backendRefs":[{"name":"dns"}]}]}}`),
		u(gw, "Gateway", "shared", "gw", `{"spec":{"gatewayClassName":"public"}}`),
		u(gw, "Gateway", "team", "edge", `{"spec":{"gatewayClassName":"public"}}`),

		u("cert-manager.io/v1", "Certificate", "shop", "front", `{"spec":{"issuerRef":{"name":"ca"}}}`),
		u("cert-manager.io/v1", "CertificateRequest", "shop", "front-1", `{"spec":{"issuerRef":{"name":"ca","kind":"ClusterIssuer"}}}`),
		u("acme.cert-manager.io/v1", "Order", "team", "front-1-1", `{"spec":{"issuerRef":{"name":"ca","kind":"ClusterIssuer","group":"cert-manager.io"}}}`),
		u("acme.cert-manager.io/v1", "Challenge", "team", "front-1-1-0", `{"spec":{"issuerRef":{"name":"letsencrypt","kind":"Issuer"}}}`),

		u("karpenter.sh/v1", "NodePool", "", "default", `{"spec":{"template":{"spec":{"nodeClassRef":{"group":"karpenter.k8s.aws","kind":"EC2NodeClass","name":"base"}}}}}`),
		u("karpenter.sh/v1", "NodePool", "", "auto", `{"spec":{"template":{"spec":{"nodeClassRef":{"group":"eks.amazonaws.com","kind":"NodeClass","name":"default"}}}}}`),

		u("cluster.x-k8s.io/v1beta1", "Cluster", "shop", "c1", `{"spec":{"topology":{"class":"base","classNamespace":"shared"}}}`),
		u("cluster.x-k8s.io/v1beta2", "Cluster", "team", "c2", `{"spec":{"topology":{"classRef":{"name":"base","namespace":"shared"}}}}`),
		u("cluster.x-k8s.io/v1beta1", "Cluster", "team", "c3", `{"spec":{"topology":{"class":"local"}}}`),

		u("eventing.knative.dev/v1", "Trigger", "shop", "t1", `{"spec":{"broker":"default","subscriber":{"ref":{"apiVersion":"v1","kind":"Service","name":"web"}}}}`),
		u("eventing.knative.dev/v1", "Trigger", "team", "t2", `{"spec":{"broker":"ignored","brokerRef":{"name":"default","namespace":"shop"},"subscriber":{"ref":{"group":"serving.knative.dev","kind":"Service","name":"fn","namespace":"shared"}}}}`),
		u("sources.knative.dev/v1", "PingSource", "shop", "ping", `{"spec":{"sink":{"ref":{"apiVersion":"eventing.knative.dev/v1","kind":"Broker","name":"default"}}}}`),
		u("sources.knative.dev/v1", "ApiServerSource", "team", "api", `{"spec":{"sink":{"ref":{"apiVersion":"v1","kind":"Service","name":"web","namespace":"shop"}}}}`),
		u("sources.knative.dev/v1", "ContainerSource", "shared", "cs", `{"spec":{"sink":{"ref":{"apiVersion":"messaging.knative.dev/v1","kind":"InMemoryChannel","name":"ch"}}}}`),
		u("sources.knative.dev/v1", "SinkBinding", "shop", "sb", `{"spec":{"sink":{"ref":{"apiVersion":"serving.knative.dev/v1","kind":"Service","name":"fn","namespace":"shared"}}}}`),
		u("messaging.knative.dev/v1", "Channel", "shop", "ch", `{"status":{"channel":{"apiVersion":"messaging.knative.dev/v1","kind":"InMemoryChannel","name":"ch-backing","namespace":"shared"}}}`),
		u("messaging.knative.dev/v1", "InMemoryChannel", "shared", "ch", `{"status":{"channel":{"apiVersion":"messaging.knative.dev/v1","kind":"InMemoryChannel","name":"ch-backing"}}}`),

		u("networking.istio.io/v1", "VirtualService", "shop", "http", `{"spec":{"gateways":["shared/gw","local-gw","mesh"],"http":[{"route":[{"destination":{"host":"web"}},{"destination":{"host":"api.shared.svc.cluster.local"}},{"destination":{"host":"db.data.svc.custom.internal"}},{"destination":{"host":"db.data"}}]}]}}`),
		u("networking.istio.io/v1", "VirtualService", "team", "tcp", `{"spec":{"gateways":["shared/gw"],"tcp":[{"route":[{"destination":{"host":"db.data.svc.cluster.local"}}]}]}}`),
		u("networking.istio.io/v1", "VirtualService", "shared", "tls", `{"spec":{"tls":[{"route":[{"destination":{"host":"api"}},{"destination":{"host":"db.data.svc.custom.internal"}}]}]}}`),
		u("networking.istio.io/v1", "DestinationRule", "shop", "db", `{"spec":{"host":"db.data.svc.custom.internal"}}`),
		u("networking.istio.io/v1", "DestinationRule", "team", "web", `{"spec":{"host":"web.shop.svc.cluster.local"}}`),

		mirror(&corev1.Secret{}, "Secret", "team", "registry", "shop/registry"),
		mirror(&corev1.Secret{}, "Secret", "shared", "registry", "shop/registry"),
		mirror(&corev1.ConfigMap{}, "ConfigMap", "shared", "settings", "shop/settings"),
		typed(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "plain"}}, "", "v1", "Secret"),

		u("karpenter.sh/v1", "NodeClaim", "", "claim-1", `{"status":{"nodeName":"node-a"}}`),
		u("cluster.x-k8s.io/v1beta1", "Machine", "shop", "m1", `{"status":{"nodeRef":{"name":"node-b"}}}`),

		u("snapshot.storage.k8s.io/v1", "VolumeSnapshot", "shop", "daily", `{"spec":{"volumeSnapshotClassName":"csi-snapclass","source":{"persistentVolumeClaimName":"data"}},"status":{"boundVolumeSnapshotContentName":"snapcontent-1"}}`),
		u("snapshot.storage.k8s.io/v1", "VolumeSnapshotContent", "", "snapcontent-1", `{"spec":{"volumeSnapshotClassName":"csi-snapclass","volumeSnapshotRef":{"namespace":"shop","name":"daily","uid":"uid-1"}}}`),
		func() runtime.Object {
			stage := u("kargo.akuity.io/v1alpha1", "Stage", "shop", "uat", `{}`).(*unstructured.Unstructured)
			stage.SetAnnotations(map[string]string{"kargo.akuity.io/argocd-context": `[{"name":"shop-uat","namespace":"argocd"}]`})
			return stage
		}(),
	}
}

// indexedFamilies are the families with a shipped index, the Istio host
// family at every kind of mesh domain: its index doesn't depend on one.
func indexedFamilies() []*Family {
	var out []*Family
	for _, f := range append(Families(""), IstioServiceHosts("cluster.local"), IstioServiceHosts("custom.internal")) {
		if f.ReferrersIndex != nil {
			out = append(out, f)
		}
	}
	return out
}

// declaredSubjects are what f's sources among objects declare, alternatives
// included, and for a family whose index files a superset, every key it
// files: the subjects where an index could miss a referrer the scan finds.
func declaredSubjects(f *Family, objects []runtime.Object) []resourceid.Ref {
	var out []resourceid.Ref
	add := func(d Declaration) {
		for _, r := range append([]resourceid.Ref{d.Target}, d.Or...) {
			if f.target(r.GroupKind()) && !slices.Contains(out, r) {
				out = append(out, r)
			}
		}
	}
	for _, obj := range objects {
		if f.source(refOf(obj).GroupKind()) < 0 {
			continue
		}
		_ = f.Declared(obj, add)
		if f.ReferrersIndex.IndexDeclarations != nil {
			_ = f.ReferrersIndex.IndexDeclarations(obj, add)
		}
	}
	return out
}

// Each shipped index finds exactly the referrers a scan of every source
// finds, with the same paths, totals and coverage, through stores built from
// the registration the caches use. Every source kind an index is registered
// for has a referrer here, so none goes unchecked.
func TestReferrerIndexesAnswerAsTheScan(t *testing.T) {
	objects := referrerIndexSources()
	s := newRegisteredStores(t, objects)
	found := map[string]bool{}
	for _, f := range indexedFamilies() {
		scan := *f
		scan.ReferrersIndex = nil
		subjects := declaredSubjects(f, objects)
		if len(subjects) == 0 {
			t.Errorf("%s: no example declares anything", f.Name)
		}
		for _, subject := range subjects {
			for _, limit := range []int{0, 1} {
				indexed := NewView(s, ownership.Unrestricted()).WithLimit(limit).Referrers(f, subject, nil)
				scanned := NewView(s, ownership.Unrestricted()).WithLimit(limit).Referrers(&scan, subject, nil)
				if !reflect.DeepEqual(indexed, scanned) {
					t.Errorf("%s referrers of %v (limit %d):\nindexed %+v\nscanned %+v", f.Name, subject, limit, indexed, scanned)
				}
				if limit == 0 {
					for _, r := range indexed.Referrers {
						found[fmt.Sprint(f.Name, r.Ref.GroupKind())] = true
					}
				}
			}
		}
	}
	for _, f := range indexedFamilies() {
		for _, source := range f.Sources {
			if !found[fmt.Sprint(f.Name, source.Kind)] {
				t.Errorf("no %v in the examples is found by %s", source.Kind, f.ReferrersIndex.Name)
			}
		}
	}
}

// Indexed reverse reads of thousands of routes find the referrers a scan
// finds, through the registered indexes.
func TestReferrerIndexesCountAsTheScanAtScale(t *testing.T) {
	_, routes, virtualServices := routeFixture(false)
	var objects []runtime.Object
	for _, item := range append(routes, virtualServices...) {
		objects = append(objects, item.(runtime.Object))
	}
	s := newRegisteredStores(t, objects)
	for _, q := range []struct {
		f      *Family
		target resourceid.Ref
		count  int
	}{
		{GatewayParents, resourceid.NewRef("gateway.networking.k8s.io", "Gateway", "shared", "gw"), 5000},
		{GatewayBackends, resourceid.NewRef("", "Service", "shared", "service-7"), 50},
		{IstioGateways, resourceid.NewRef("networking.istio.io", "Gateway", "shared", "mesh"), 2000},
		{IstioServiceHosts("cluster.local"), resourceid.NewRef("", "Service", "shared", "service-7"), 20},
	} {
		scan := *q.f
		scan.ReferrersIndex = nil
		indexed := NewView(s, ownership.Unrestricted()).WithLimit(0).Referrers(q.f, q.target, nil)
		scanned := NewView(s, ownership.Unrestricted()).WithLimit(0).Referrers(&scan, q.target, nil)
		if len(indexed.Referrers) != q.count || !reflect.DeepEqual(indexed, scanned) {
			t.Errorf("%s: indexed %d, scanned %d; want %d each, the same", q.f.Name, len(indexed.Referrers), len(scanned.Referrers), q.count)
		}
	}
}
