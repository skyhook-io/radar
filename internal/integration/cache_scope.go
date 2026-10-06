package integration

import (
	"slices"
)

// informerScope is the part of Radar's informer cache that says which
// namespaces it holds for a resource.
type InformerScope interface {
	IsKindClusterWide(string) bool
	KindNamespaces(string) []string
	IsKindReady(string) bool
}

type CacheNamespaceResult struct {
	Namespaces  []string
	Limited     bool
	Partial     bool
	Unavailable bool
}

// namespacesWithinCache narrows requested (nil = all namespaces) to what the
// informer for resource holds. Limited: the informer is not cluster-wide;
// Partial: some requested namespaces are not held; Unavailable: none are.
func NamespacesWithinCache(cache InformerScope, resource string, requested []string) CacheNamespaceResult {
	result := CacheNamespaceResult{Namespaces: requested}
	if cache == nil || cache.IsKindClusterWide(resource) {
		return result
	}
	result.Limited = true
	if NoNamespaceAccess(requested) {
		return result
	}
	cached := cache.KindNamespaces(resource)
	if len(cached) == 0 {
		result.Namespaces = []string{}
		result.Unavailable = true
		return result
	}
	result.Namespaces = IntersectNamespaces(cached, requested)
	if requested == nil {
		result.Partial = true
		return result
	}
	for _, namespace := range requested {
		if !slices.Contains(cached, namespace) {
			if len(result.Namespaces) == 0 {
				result.Unavailable = true
			} else {
				result.Partial = true
			}
			return result
		}
	}
	return result
}

func CacheCoversNamespace(cache InformerScope, resource, namespace string) bool {
	return cache != nil && cache.IsKindReady(resource) && (cache.IsKindClusterWide(resource) || slices.Contains(cache.KindNamespaces(resource), namespace))
}

// intersectNamespaces returns the namespaces to actually scan. nil `allowed`
// means the user is unrestricted; preserve `requested` (which may also be nil
// for cluster-wide). When the user is restricted, keep only the requested
// namespaces they're allowed to see; if `requested` is empty, fall back to
// the full allowed set.
func IntersectNamespaces(allowed, requested []string) []string {
	if allowed == nil {
		return requested
	}
	if len(requested) == 0 {
		return allowed
	}
	allowSet := make(map[string]struct{}, len(allowed))
	for _, ns := range allowed {
		allowSet[ns] = struct{}{}
	}
	out := make([]string, 0, len(requested))
	for _, ns := range requested {
		if _, ok := allowSet[ns]; ok {
			out = append(out, ns)
		}
	}
	return out
}

// noNamespaceAccess returns true when a namespace filter explicitly grants no access
// (non-nil empty slice from auth filtering). Handlers with custom namespace logic
// should check this and return empty results.
func NoNamespaceAccess(namespaces []string) bool {
	return namespaces != nil && len(namespaces) == 0
}
