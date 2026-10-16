package k8score

import (
	"fmt"
	"runtime"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
)

// BenchmarkOwnerIndexRetainedBytes reports the heap the owner index keeps per
// owned object: 50,000 Pods held with and without it, each owner owning one
// object (a ReplicaSet's Deployment) or ten (a ReplicaSet's Pods). Run with
// -bench OwnerIndexRetained -run '^$' -benchtime 3x.
func BenchmarkOwnerIndexRetainedBytes(b *testing.B) {
	for _, perOwner := range []int{1, 10} {
		b.Run(fmt.Sprintf("%d-per-owner", perOwner), func(b *testing.B) {
			const pods = 50_000
			objects := make([]*corev1.Pod, pods)
			for i := range objects {
				owner := i / perOwner
				objects[i] = &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
					Namespace: fmt.Sprint("ns-", i%100), Name: fmt.Sprint("pod-", i), UID: types.UID(fmt.Sprintf("8b1c0d5e-%04d-4a8f-9a5e-%012d", i%10000, i)),
					OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: fmt.Sprint("rs-", owner),
						UID: types.UID(fmt.Sprintf("3f6d2c1a-%04d-4b7e-8c9d-%012d", owner%10000, owner))}},
				}}
			}
			retained := func(indexers cache.Indexers) uint64 {
				runtime.GC()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				store := cache.NewIndexer(cache.MetaNamespaceKeyFunc, indexers)
				for _, p := range objects {
					_ = store.Add(p)
				}
				runtime.GC()
				runtime.ReadMemStats(&after)
				runtime.KeepAlive(store)
				return after.HeapAlloc - before.HeapAlloc
			}
			namespaceOnly := cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc}
			withOwners := cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc, OwnerUIDIndex: ownerUIDs}
			var extra float64
			for range b.N {
				extra += float64(retained(withOwners)) - float64(retained(namespaceOnly))
			}
			b.ReportMetric(extra/float64(b.N)/pods, "B/owned-object")
		})
	}
}

// BenchmarkOwnerIndexUpdate reports what the index adds to one store update.
func BenchmarkOwnerIndexUpdate(b *testing.B) {
	for name, indexers := range map[string]cache.Indexers{
		"without": {cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
		"with":    {cache.NamespaceIndex: cache.MetaNamespaceIndexFunc, OwnerUIDIndex: ownerUIDs},
	} {
		b.Run(name, func(b *testing.B) {
			store := cache.NewIndexer(cache.MetaNamespaceKeyFunc, indexers)
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "p", UID: "uid-p",
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "rs", UID: "3f6d2c1a-0001-4b7e-8c9d-000000000001"}}}}
			_ = store.Add(pod)
			b.ReportAllocs()
			for range b.N {
				_ = store.Update(pod)
			}
		})
	}
}
