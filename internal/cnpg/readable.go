package cnpg

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/internal/k8s"
)

// readableIn lists resource in namespace from the informer store that holds
// it, keeping the objects of type T that match selector. ok is false unless
// that store is complete now. The typed listers of PVCs, PodDisruptionBudgets
// and ConfigMaps (among others) are nil until every namespace of the kind has
// loaded, so a read of one loaded namespace goes through its own store.
func readableIn[T metav1.Object](cache *k8s.ResourceCache, resource, namespace string, selector labels.Selector) ([]T, bool) {
	objs, ok := cache.ReadableObjects(resource, []string{namespace})
	if !ok {
		return nil, false
	}
	out := make([]T, 0, len(objs))
	for _, obj := range objs {
		if typed, isT := any(obj).(T); isT && selector.Matches(labels.Set(typed.GetLabels())) {
			out = append(out, typed)
		}
	}
	return out, true
}
