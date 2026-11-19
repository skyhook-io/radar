package k8score

import (
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

func TestReflectsIndexFilesMirrorsBySource(t *testing.T) {
	mirror := func(namespace, name, reflects string) *corev1.Secret {
		return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Annotations: map[string]string{"reflector.v1.k8s.emberstack.com/reflects": reflects}}}
	}
	client := fake.NewClientset(
		mirror("alpha", "registry", "platform/registry"), mirror("beta", "registry", "platform/registry"), mirror("beta", "bad", "not-a-ref"),
	)
	rc, err := NewResourceCache(CacheConfig{Client: client, ResourceTypes: map[string]bool{Secrets: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Stop()
	index := relations.Reflections.ReferrersIndex
	if got, ok := rc.ReadableIndexedObjects(Secrets, nil, index.Name, index.Key(resourceid.NewRef("", "Secret", "platform", "registry"))); !ok || !slices.Equal(objectNames(got), []string{"registry", "registry"}) {
		t.Errorf("platform/registry's mirrors = %v, %v; want both", objectNames(got), ok)
	}
}

// Every typed kind that is an indexed family's source carries the family's
// index in each of its stores.
func TestTypedRelationIndexesOnEverySourceStore(t *testing.T) {
	enabled := map[string]bool{}
	for _, s := range buildInformerSetups() {
		if b, ok := resourceid.BuiltinForKind(s.kind); ok && relations.ReferrerIndexers[schema.GroupResource{Group: b.Group, Resource: b.Resource}] != nil {
			enabled[s.key] = true
		}
	}
	if !enabled[Secrets] || !enabled[ConfigMaps] {
		t.Fatalf("typed sources = %v; want Secrets and ConfigMaps at least", enabled)
	}
	rc, err := NewResourceCache(CacheConfig{Client: fake.NewClientset(), ResourceTypes: enabled})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Stop()
	for _, s := range buildInformerSetups() {
		if !enabled[s.key] {
			continue
		}
		b, _ := resourceid.BuiltinForKind(s.kind)
		if len(rc.kindStores[s.key]) == 0 {
			t.Errorf("%s has no store", s.key)
		}
		for _, st := range rc.kindStores[s.key] {
			for name := range relations.ReferrerIndexers[schema.GroupResource{Group: b.Group, Resource: b.Resource}] {
				if st.indexer.GetIndexers()[name] == nil {
					t.Errorf("%s store lacks %s", s.key, name)
				}
			}
		}
	}
}

// A dynamic informer takes its resource's indexes on every version and
// scope, whether or not discovery names its kind when it starts; a store read
// by an index it lacks fails and says why.
func TestDynamicRouteIndexesOnEveryVersionAndScope(t *testing.T) {
	for _, discovery := range []string{"none", "without the kind", "complete"} {
		for _, version := range []string{"v1", "v1beta1"} {
			for _, namespace := range []string{"", "alpha"} {
				t.Run(discovery+"/"+version+"/"+namespace, func(t *testing.T) {
					gvr := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: version, Resource: "httproutes"}
					route := func(ns string) *unstructured.Unstructured {
						return &unstructured.Unstructured{Object: map[string]any{
							"apiVersion": gvr.GroupVersion().String(), "kind": "HTTPRoute", "metadata": map[string]any{"namespace": ns, "name": "route"}, "spec": map[string]any{"parentRefs": []any{map[string]any{"name": "gw", "namespace": "shared"}}},
						}}
					}
					dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "HTTPRouteList"}, route("alpha"), route("beta"))
					cfg := DynamicCacheConfig{DynamicClient: dyn}
					switch discovery {
					case "without the kind":
						cfg.Discovery = newDiscoveryWith(t, APIResource{Group: gvr.Group, Version: version, Kind: "Gateway", Name: "gateways", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}})
					case "complete":
						cfg.Discovery = newDiscoveryWith(t, APIResource{Group: gvr.Group, Version: version, Kind: "HTTPRoute", Name: "httproutes", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}})
					}
					d, err := NewDynamicResourceCache(cfg)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(d.Stop)
					if err := d.startWatching(gvr, namespace); err != nil {
						t.Fatal(err)
					}
					waitUntil(t, 5*time.Second, "route informer", func() bool { return d.IsNamespaceSynced(gvr, namespace) })
					f := relations.GatewayParents
					target := resourceid.NewRef(gvr.Group, "Gateway", "shared", "gw")
					hits, ok := d.ReadableIndexed(gvr, namespace, f.ReferrersIndex.Name, f.ReferrersIndex.Key(target))
					want := 2
					if namespace != "" {
						want = 1
					}
					if !ok || len(hits) != want || d.LacksIndex(gvr, namespace, f.ReferrersIndex.Name) {
						t.Fatalf("referrers: %d %v; want %d", len(hits), ok, want)
					}
					if _, ok := d.ReadableIndexed(gvr, namespace, "missing", "key"); ok || !d.LacksIndex(gvr, namespace, "missing") {
						t.Fatal("a read by an index the store lacks succeeded or wasn't said to lack it")
					}
					if _, ok := d.ReadableIndexed(gvr, namespace, "missing"); ok {
						t.Fatal("a read by an index the store lacks, with no keys, succeeded")
					}
				})
			}
		}
	}
}
