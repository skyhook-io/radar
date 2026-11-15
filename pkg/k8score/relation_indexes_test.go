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
	if got, ok := rc.ReadableIndexedObjects(Secrets, nil, ReflectsIndex, ReflectsKey("platform", "registry")); !ok || !slices.Equal(objectNames(got), []string{"registry", "registry"}) {
		t.Errorf("platform/registry's mirrors = %v, %v; want both", objectNames(got), ok)
	}
}

func TestRelationIndexRegistrationCoversEverySource(t *testing.T) {
	for gr, indexes := range relations.ReferrerIndexers {
		for name := range indexes {
			if dynamicRelationIndexers[gr][name] == nil {
				t.Errorf("dynamic %v lacks %s", gr, name)
			}
			if gr.Group == "" && relationIndexers[gr.Resource][name] == nil {
				t.Errorf("typed %v lacks %s", gr, name)
			}
		}
	}
}

func TestDynamicRouteIndexesOnEveryVersionAndScope(t *testing.T) {
	for _, version := range []string{"v1", "v1beta1"} {
		for _, namespace := range []string{"", "alpha"} {
			t.Run(version+"/"+namespace, func(t *testing.T) {
				gvr := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: version, Resource: "httproutes"}
				route := func(ns string) *unstructured.Unstructured {
					return &unstructured.Unstructured{Object: map[string]any{
						"apiVersion": gvr.GroupVersion().String(), "kind": "HTTPRoute", "metadata": map[string]any{"namespace": ns, "name": "route"}, "spec": map[string]any{"parentRefs": []any{map[string]any{"name": "gw", "namespace": "shared"}}},
					}}
				}
				dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "HTTPRouteList"}, route("alpha"), route("beta"))
				d, err := NewDynamicResourceCache(DynamicCacheConfig{DynamicClient: dyn})
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
				hits, ok := d.ReadableIndexed(gvr, namespace, f.ReferrersIndex.Name, f.ReferrersIndex.Key(target, "alpha"))
				if !ok || len(hits) != 1 || hits[0].GetNamespace() != "alpha" {
					t.Fatalf("alpha bucket: %v %v", hits, ok)
				}
				hits, ok = d.ReadableIndexed(gvr, namespace, f.ReferrersIndex.Name, f.ReferrersIndex.Key(target, "*"))
				want := 2
				if namespace != "" {
					want = 1
				}
				if !ok || len(hits) != want {
					t.Fatalf("all buckets: %d %v; want %d", len(hits), ok, want)
				}
				if !d.DynamicIndexMissing(gvr, namespace, "missing") {
					t.Fatal("missing index not distinguished")
				}
			})
		}
	}
}
