package tree

import (
	"context"
	"fmt"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/ownership/cachesource"
	"github.com/skyhook-io/radar/pkg/topology"
)

func BenchmarkGeneratedTreeCachedOwnership(b *testing.B) {
	var objects []runtime.Object
	var inventory []any
	topo := &topology.Topology{}
	for i := range 100 {
		name := fmt.Sprintf("workload-%03d", i)
		dep := metav1.ObjectMeta{Name: name, Namespace: "monitoring", UID: types.UID("deploy-" + name)}
		rs := metav1.ObjectMeta{Name: name, Namespace: "monitoring", UID: types.UID("rs-" + name), OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: name, UID: dep.UID}}}
		objects = append(objects, &appsv1.Deployment{ObjectMeta: dep}, &appsv1.ReplicaSet{ObjectMeta: rs})
		inventory = append(inventory, map[string]any{"group": "apps", "kind": "Deployment", "namespace": "monitoring", "name": name})
		topo.Nodes = append(topo.Nodes, topology.Node{ID: name, Kind: topology.KindReplicaSet, Name: name, Data: map[string]any{"namespace": "monitoring", "apiVersion": "apps/v1"}})
		for pod := range 9 {
			objects = append(objects, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-%d", name, pod), Namespace: "monitoring", UID: types.UID(fmt.Sprintf("pod-%s-%d", name, pod)), OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: name, UID: rs.UID}}}})
		}
	}
	client := fake.NewClientset(objects...)
	verbs := metav1.Verbs{"get", "list", "watch", "delete"}
	resources := []*metav1.APIResourceList{
		{GroupVersion: "v1", APIResources: []metav1.APIResource{{Name: "pods", Kind: "Pod", Namespaced: true, Verbs: verbs}}},
		{GroupVersion: "apps/v1", APIResources: []metav1.APIResource{{Name: "deployments", Kind: "Deployment", Namespaced: true, Verbs: verbs}, {Name: "replicasets", Kind: "ReplicaSet", Namespaced: true, Verbs: verbs}}},
	}
	for i := range 197 {
		resources = append(resources, &metav1.APIResourceList{GroupVersion: fmt.Sprintf("group%d.example/v1", i), APIResources: []metav1.APIResource{{Name: "widgets", Kind: fmt.Sprintf("Widget%d", i), Namespaced: true, Verbs: verbs}}})
	}
	client.Discovery().(*fakediscovery.FakeDiscovery).Resources = resources
	discovery, err := k8score.NewResourceDiscovery(client.Discovery())
	if err != nil {
		b.Fatal(err)
	}
	cache, err := k8score.NewResourceCache(k8score.CacheConfig{Client: client, ResourceTypes: map[string]bool{k8score.Pods: true, k8score.ReplicaSets: true, k8score.Deployments: true}})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(cache.Stop)
	source := cachesource.New(func() cachesource.Caches { return cachesource.Caches{Typed: cache, Discovery: discovery} })
	app := operatorApplication()
	unstructured.SetNestedSlice(app.Object, inventory, "status", "resources")
	getter := &fakeDynamic{objects: map[string]*unstructured.Unstructured{refKey(ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "monitoring"}): app}}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		session, err := source.Begin()
		if err != nil {
			b.Fatal(err)
		}
		result, _, err := NewBuilder(getter, topo).WithOwnership(session).Build(context.Background(), "Application", "argocd", "monitoring", "argoproj.io")
		if err != nil {
			b.Fatal(err)
		}
		if result.Summary.Generated != 1000 {
			b.Fatalf("generated=%d", result.Summary.Generated)
		}
		getter.calls = nil
	}
}
