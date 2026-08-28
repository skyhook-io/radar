package server

import (
	"net/http"
	"slices"

	"github.com/skyhook-io/radar/internal/k8s"
)

func (s *Server) capacityNamespacesForUser(r *http.Request) []string {
	requested := parseNamespaces(r.URL.Query())
	if k8s.ForceNamespaceScope {
		target := k8s.GetNamespaceScopeTarget()
		switch {
		case target == "":
			return []string{}
		case requested == nil:
			requested = []string{target}
		case slices.Contains(requested, target):
			requested = []string{target}
		default:
			return []string{}
		}
	}
	return s.getUserNamespaces(r, requested)
}

func (s *Server) capacityNamespacesForSource(r *http.Request, namespaces []string, group, resource string) []string {
	if noNamespaceAccess(namespaces) {
		return namespaces
	}
	if namespaces == nil {
		if s.canRead(r, group, resource, "", "list") {
			return nil
		}
		return s.filterNamespacesByCanRead(r, group, resource, "list", allNamespaceNames())
	}
	return s.filterNamespacesByCanRead(r, group, resource, "list", namespaces)
}

type capacityInformerScope interface {
	IsKindClusterWide(string) bool
	KindNamespaces(string) []string
	IsKindReady(string) bool
}

type capacityCacheNamespaceResult struct {
	namespaces  []string
	limited     bool
	partial     bool
	unavailable bool
}

func capacityNamespacesWithinCache(cache capacityInformerScope, resource string, requested []string) capacityCacheNamespaceResult {
	result := capacityCacheNamespaceResult{namespaces: requested}
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

func capacityCacheCoversNamespace(cache capacityInformerScope, resource, namespace string) bool {
	return cache != nil && cache.IsKindReady(resource) && (cache.IsKindClusterWide(resource) || slices.Contains(cache.KindNamespaces(resource), namespace))
}
