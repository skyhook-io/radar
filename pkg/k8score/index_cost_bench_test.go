package k8score

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
)

// Pod populations the relationship-layer budgets are stated against, with
// the field census's distribution (fieldShape in pkg/topology): L has its
// 29,455 Pods over 432 namespaces, XL scales both by 10/3. The census has more
// ReplicaSets than Pods, so each Pod has an owner of its own, the owner-UID
// index's costliest shape per Pod.
var indexCostPopulations = []struct {
	name       string
	pods       int
	namespaces int
}{
	{"L", 29_455, 432},
	{"XL", 98_183, 1_440},
}

func indexCostPods(n, namespaces int) []any {
	objects := make([]any, n)
	for i := range objects {
		objects[i] = &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Namespace: fmt.Sprintf("ns-%04d", i%namespaces), Name: fmt.Sprintf("pod-%06d", i),
			UID:             types.UID(fmt.Sprintf("8b1c0d5e-%04d-4a8f-9a5e-%012d", i%10000, i)),
			ResourceVersion: "1",
			Labels:          map[string]string{"app": fmt.Sprintf("app-%05d", i/2)},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: fmt.Sprintf("rs-%05d", i),
				UID: types.UID(fmt.Sprintf("3f6d2c1a-%04d-4b7e-8c9d-%012d", i%10000, i))}},
		}}
	}
	return objects
}

func indexCostIndexers() map[string]cache.Indexers {
	return map[string]cache.Indexers{
		"namespace-only": {cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
		"with-owner-uid": {cache.NamespaceIndex: cache.MetaNamespaceIndexFunc, OwnerUIDIndex: ownerUIDs},
	}
}

// BenchmarkIndexRelist reports one relist of a Pod store: Replace keys the
// list outside the store's lock, then rebuilds the storage and every index
// while holding its write lock. The total is an upper bound on how long
// readers of the kind wait; an index adds only to the locked part, so the
// difference with and without it is that index's cost.
func BenchmarkIndexRelist(b *testing.B) {
	for _, pop := range indexCostPopulations {
		objects := indexCostPods(pop.pods, pop.namespaces)
		for name, indexers := range indexCostIndexers() {
			b.Run(pop.name+"/"+name, func(b *testing.B) {
				store := cache.NewIndexer(cache.MetaNamespaceKeyFunc, indexers)
				if err := store.Replace(objects, "1"); err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				for b.Loop() {
					if err := store.Replace(objects, "2"); err != nil {
						b.Fatal(err)
					}
				}
				b.ReportMetric(float64(pop.pods), "pods")
			})
		}
	}
}

// BenchmarkIndexedStoreChurn reports a store's update cost while eight readers
// query it: one goroutine writes Pod updates (a new resourceVersion, as a
// status write brings) while the readers list one namespace's Pods, the same
// query with and without the extra index. The difference between the two runs
// is what the index costs under the store lock with concurrent reads.
func BenchmarkIndexedStoreChurn(b *testing.B) {
	const readers = 8
	pop := indexCostPopulations[0]
	objects := indexCostPods(pop.pods, pop.namespaces)
	updated := make([]*corev1.Pod, len(objects))
	for i, o := range objects {
		p := o.(*corev1.Pod).DeepCopy()
		p.ResourceVersion = "2"
		updated[i] = p
	}
	for name, indexers := range indexCostIndexers() {
		b.Run(name, func(b *testing.B) {
			store := cache.NewIndexer(cache.MetaNamespaceKeyFunc, indexers)
			if err := store.Replace(objects, "1"); err != nil {
				b.Fatal(err)
			}
			var stop, measuring atomic.Bool
			var reads atomic.Int64
			var readNanos atomic.Int64
			var wg sync.WaitGroup
			for range readers {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for !stop.Load() {
						start := time.Now()
						_, _ = store.ByIndex(cache.NamespaceIndex, "ns-0000")
						if measuring.Load() {
							readNanos.Add(int64(time.Since(start)))
							reads.Add(1)
						}
					}
				}()
			}
			b.ReportAllocs()
			measuring.Store(true)
			i := 0
			for b.Loop() {
				_ = store.Update(updated[i%len(updated)])
				i++
			}
			measuring.Store(false)
			stop.Store(true)
			wg.Wait()
			if n := reads.Load(); n > 0 {
				b.ReportMetric(float64(readNanos.Load())/float64(n)/1e3, "read-µs")
			}
		})
	}
}
