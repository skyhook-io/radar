package search

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

// CacheProvider adapts radar's in-process cache to the search Provider interface.
//
// Use NewCacheProvider to capture the live cache handles and collector probe
// results for one search request.
type CacheProvider struct {
	cache       *k8s.ResourceCache
	dynamic     *k8s.DynamicResourceCache
	discovery   *k8s.ResourceDiscovery
	permissions *k8s.ResourcePermissions
}

// NewCacheProvider returns a Provider over the live radar caches.
// Returns nil when the typed cache is unavailable (radar isn't connected yet).
func NewCacheProvider() *CacheProvider {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil
	}
	provider := &CacheProvider{
		cache:     cache,
		dynamic:   k8s.GetDynamicResourceCache(),
		discovery: k8s.GetResourceDiscovery(),
	}
	if result := k8s.GetCachedPermissionResult(); result != nil {
		provider.permissions = result.Perms
	}
	return provider
}

func (p *CacheProvider) ListTyped(kind string, namespaces []string) ([]runtime.Object, error) {
	return k8s.FetchResourceList(p.cache, kind, namespaces)
}

func (p *CacheProvider) ListDynamic(ctx context.Context, gvr schema.GroupVersionResource, namespace string) ([]*unstructured.Unstructured, error) {
	if p.dynamic == nil {
		return nil, fmt.Errorf("%w: dynamic cache", k8s.ErrDynamicNotReady)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return p.dynamic.ListContext(ctx, gvr, namespace)
}

func (p *CacheProvider) DynamicResources() ([]schema.GroupVersionResource, error) {
	if p.dynamic == nil {
		return nil, fmt.Errorf("%w: dynamic cache", k8s.ErrDynamicNotReady)
	}
	if p.discovery == nil {
		return nil, fmt.Errorf("%w: discovery", k8s.ErrDynamicNotReady)
	}
	resources, err := p.discovery.GetAPIResources()
	if err != nil {
		return nil, err
	}
	seen := map[schema.GroupVersionResource]bool{}
	watched := map[schema.GroupResource]bool{}
	for _, gvr := range p.dynamic.GetWatchedResources() {
		seen[gvr] = true
		watched[gvr.GroupResource()] = true
	}
	for _, ar := range resources {
		if strings.Contains(ar.Name, "/") || !slices.Contains(ar.Verbs, "list") || watched[schema.GroupResource{Group: ar.Group, Resource: ar.Name}] {
			continue
		}
		seen[schema.GroupVersionResource{Group: ar.Group, Version: ar.Version, Resource: ar.Name}] = true
	}
	out := make([]schema.GroupVersionResource, 0, len(seen))
	for gvr := range seen {
		out = append(out, gvr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out, nil
}

func (p *CacheProvider) TypedCoverage(kind string, namespaces []string) string {
	switch p.cache.KindReadinessForKindName(kind) {
	case k8s.KindPending:
		return "syncing"
	case k8s.KindFailed:
		return "sync_failed"
	case k8s.KindUnavailable:
		if p.permissions != nil && !p.permissions.CanList(kind) {
			return "sa_forbidden"
		}
		return "syncing"
	}
	if isClusterScopedKind(kind) {
		return ""
	}
	resource := k8s.CanonicalBuiltinKind(kind)
	if len(namespaces) == 0 {
		if !p.cache.IsKindClusterWide(resource) {
			return "namespace_scope"
		}
	} else {
		for _, ns := range namespaces {
			if !p.cache.KindCoversNamespace(resource, ns) {
				return "namespace_scope"
			}
		}
	}
	return ""
}

func (p *CacheProvider) DynamicObservation(gvr schema.GroupVersionResource) k8score.DynamicResourceObservation {
	if p.dynamic == nil {
		return k8score.DynamicResourceObservation{State: k8score.DynamicObservationSyncing}
	}
	return p.dynamic.Observation(gvr)
}

func (p *CacheProvider) WarmDynamic(ctx context.Context, gvr schema.GroupVersionResource, namespace string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.dynamic == nil {
		return fmt.Errorf("%w: dynamic cache", k8s.ErrDynamicNotReady)
	}
	if p.dynamic.GetDiscoveryStatus() != k8score.CRDDiscoveryComplete {
		return fmt.Errorf("%w: CRD discovery", k8s.ErrDynamicNotReady)
	}
	if err := p.dynamic.EnsureWatchingContext(ctx, gvr, namespace); err != nil {
		return err
	}
	if !p.dynamic.WaitForSyncContext(ctx, gvr, namespace) {
		return fmt.Errorf("%w: initial sync", k8s.ErrDynamicNotReady)
	}
	return nil
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
