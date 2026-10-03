package server

import "slices"

// informerScope is the part of Radar's informer cache that says which
// namespaces it holds for a resource.
type informerScope interface {
	IsKindClusterWide(string) bool
	KindNamespaces(string) []string
	IsKindReady(string) bool
}

type cacheNamespaceResult struct {
	namespaces  []string
	limited     bool
	partial     bool
	unavailable bool
}

// namespacesWithinCache narrows requested (nil = all namespaces) to what the
// informer for resource holds. limited: the informer is not cluster-wide;
// partial: some requested namespaces are not held; unavailable: none are.
func namespacesWithinCache(cache informerScope, resource string, requested []string) cacheNamespaceResult {
	result := cacheNamespaceResult{namespaces: requested}
	if cache == nil || cache.IsKindClusterWide(resource) {
		return result
	}
	result.limited = true
	if noNamespaceAccess(requested) {
		return result
	}
	cached := cache.KindNamespaces(resource)
	if len(cached) == 0 {
		result.namespaces = []string{}
		result.unavailable = true
		return result
	}
	result.namespaces = intersectNamespaces(cached, requested)
	if requested == nil {
		result.partial = true
		return result
	}
	for _, namespace := range requested {
		if !slices.Contains(cached, namespace) {
			if len(result.namespaces) == 0 {
				result.unavailable = true
			} else {
				result.partial = true
			}
			return result
		}
	}
	return result
}

func cacheCoversNamespace(cache informerScope, resource, namespace string) bool {
	return cache != nil && cache.IsKindReady(resource) && (cache.IsKindClusterWide(resource) || slices.Contains(cache.KindNamespaces(resource), namespace))
}
