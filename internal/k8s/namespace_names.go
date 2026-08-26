package k8s

import "k8s.io/apimachinery/pkg/labels"

// AllNamespaceNames returns every namespace name from the shared cache lister,
// or nil when the namespace informer isn't available. Used as the candidate
// pool for per-user SAR filtering — the SAR is the authorization gate, so the
// (cluster-wide) pool only needs to be a superset of what the user can read.
func AllNamespaceNames() []string {
	cache := GetResourceCache()
	if cache == nil {
		return nil
	}
	lister := cache.Namespaces()
	if lister == nil {
		return nil
	}
	nsList, err := lister.List(labels.Everything())
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(nsList))
	for _, ns := range nsList {
		names = append(names, ns.Name)
	}
	return names
}
