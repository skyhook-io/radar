package search

import (
	"context"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/k8s"
)

// CacheProvider adapts radar's in-process cache to the search Provider interface.
//
// Use NewCacheProvider to construct one over the package-level singletons
// the rest of radar already wires up — it has no fields beyond those handles.
type CacheProvider struct {
	cache     *k8s.ResourceCache
	dynamic   *k8s.DynamicResourceCache
	discovery *k8s.ResourceDiscovery
}

// NewCacheProvider returns a Provider over the live radar caches.
// Returns nil when the typed cache is unavailable (radar isn't connected yet).
func NewCacheProvider() *CacheProvider {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil
	}
	return &CacheProvider{
		cache:     cache,
		dynamic:   k8s.GetDynamicResourceCache(),
		discovery: k8s.GetResourceDiscovery(),
	}
}

func (p *CacheProvider) ListTyped(kind string, namespaces []string) ([]runtime.Object, error) {
	return k8s.FetchResourceList(p.cache, kind, namespaces)
}

func (p *CacheProvider) ListDynamic(ctx context.Context, gvr schema.GroupVersionResource, namespace string) ([]*unstructured.Unstructured, error) {
	if p.dynamic == nil {
		return nil, nil
	}
	return p.dynamic.List(gvr, namespace)
}

func (p *CacheProvider) WatchedDynamic() []schema.GroupVersionResource {
	if p.dynamic == nil {
		return nil
	}
	return p.dynamic.GetWatchedResources()
}

func (p *CacheProvider) KindForGVR(gvr schema.GroupVersionResource) string {
	if p.discovery == nil {
		return ""
	}
	return p.discovery.GetKindForGVR(gvr)
}

func (p *CacheProvider) NamespacedForGVR(gvr schema.GroupVersionResource) (bool, bool) {
	if p.discovery == nil {
		return false, false
	}
	kind := p.discovery.GetKindForGVR(gvr)
	if kind == "" {
		return false, false
	}
	ar, ok := p.discovery.GetResourceWithGroup(kind, gvr.Group)
	if !ok {
		return false, false
	}
	return ar.Namespaced, true
}
