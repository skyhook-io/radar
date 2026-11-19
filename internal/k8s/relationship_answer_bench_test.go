package k8s

import (
	"fmt"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/cachesource"
)

// configMapAnswerFixture caches a ConfigMap and n Deployments in another
// namespace, none of which relate to it, and answers through those caches.
func configMapAnswerFixture(tb testing.TB, n int) (*ResourceCache, *corev1.ConfigMap) {
	tb.Helper()
	cm := &corev1.ConfigMap{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"}, ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "settings", UID: "settings"}}
	objects := []runtime.Object{cm}
	for i := range n {
		objects = append(objects, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "elsewhere", Name: fmt.Sprintf("app-%05d", i)}})
	}
	core, err := k8score.NewResourceCache(k8score.CacheConfig{Client: fake.NewClientset(objects...), ResourceTypes: map[string]bool{
		k8score.Pods: true, k8score.Deployments: true, k8score.ReplicaSets: true, k8score.ConfigMaps: true,
	}, DeferredTypes: map[string]bool{}})
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(core.Stop)
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if list, _ := core.Deployments().List(labels.Everything()); len(list) == n {
			break
		}
	}
	discovery, err := k8score.NewResourceDiscovery(fake.NewClientset().Discovery())
	if err != nil {
		tb.Fatal(err)
	}
	for _, r := range []k8score.APIResource{
		{Name: "pods", Kind: "Pod", Version: "v1", Namespaced: true},
		{Name: "configmaps", Kind: "ConfigMap", Version: "v1", Namespaced: true},
		{Name: "deployments", Kind: "Deployment", Group: "apps", Version: "v1", Namespaced: true},
		{Name: "replicasets", Kind: "ReplicaSet", Group: "apps", Version: "v1", Namespaced: true},
	} {
		r.Verbs = metav1.Verbs{"delete", "get", "list", "watch"}
		discovery.AddAPIResource(r)
	}
	previous := ownershipSource
	ownershipSource = cachesource.New(func() cachesource.Caches { return cachesource.Caches{Typed: core, Discovery: discovery} })
	tb.Cleanup(func() { ownershipSource = previous })
	return &ResourceCache{ResourceCache: core}, cm
}

func BenchmarkConfigMapAnswerWithUnrelatedDeployments(b *testing.B) {
	access := func(*ownership.Catalog) ownership.Access { return ownership.Unrestricted() }
	for _, n := range []int{0, 10000} {
		b.Run(fmt.Sprintf("deployments=%d", n), func(b *testing.B) {
			cache, cm := configMapAnswerFixture(b, n)
			b.ReportAllocs()
			for b.Loop() {
				if rel, _, _ := AnswerRelationships(cache, cm, access, access); rel == nil {
					b.Fatal("no answer")
				}
			}
		})
	}
}
