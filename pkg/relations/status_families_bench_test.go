package relations

import (
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// BenchmarkNodePods answers a Node's Pods over a Pod store the size of the L
// and XL fixtures (about 30 Pods a Node), by reading every Pod, as
// PodPlacement does, and by an index of Pods by Node, which Radar doesn't
// keep: BenchmarkIndexRelist in pkg/k8score prices it.
func BenchmarkNodePods(b *testing.B) {
	for _, pop := range []struct {
		name             string
		pods, namespaces int
	}{{"L", 29_455, 432}, {"XL", 98_183, 1_440}} {
		indexed := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{
			cache.NamespaceIndex: cache.MetaNamespaceIndexFunc,
			"podNode":            func(obj any) ([]string, error) { return []string{obj.(*corev1.Pod).Spec.NodeName}, nil },
		})
		for i := range pop.pods {
			_ = indexed.Add(typed(&corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Namespace: fmt.Sprintf("ns-%04d", i%pop.namespaces), Name: fmt.Sprintf("pod-%06d", i)},
				Spec:       corev1.PodSpec{NodeName: fmt.Sprintf("node-%04d", i/30)},
				Status:     corev1.PodStatus{Phase: corev1.PodRunning},
			}, "", "v1", "Pod"))
		}
		s := stores{byKind: map[resourceid.GroupKind]cache.Indexer{kindPod: indexed}}
		node := resourceid.Ref{Kind: "Node", Name: "node-0007"}
		index := *PodPlacement
		index.ReferrersIndex = &ReferrersIndex{Name: "podNode", Key: func(node resourceid.Ref) string { return node.Name }}
		for name, f := range map[string]*Family{"scan": PodPlacement, "index": &index} {
			b.Run(pop.name+"/"+name, func(b *testing.B) {
				v := NewView(s, ownership.Unrestricted())
				for b.Loop() {
					if got := v.Referrers(f, node, nil); len(got.Referrers) != 30 {
						b.Fatalf("got %d Pods", len(got.Referrers))
					}
				}
			})
		}
	}
}
