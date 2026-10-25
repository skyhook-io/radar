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
	"github.com/skyhook-io/radar/internal/readplan"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
)

// CacheProvider adapts radar's in-process cache to the search Provider interface.
//
// Use NewCacheProvider to capture the live cache handles and collector probe
// results for one search request.
type CacheProvider struct {
	cache            *k8s.ResourceCache
	dynamic          *k8s.DynamicResourceCache
	discovery        *k8s.ResourceDiscovery
	permissions      *k8s.PermissionCheckResult
	observations     *k8score.DynamicObservationView
	catalogResources []k8score.APIResource
}

// NewCacheProvider returns a Provider over the live radar caches.
// Returns nil when the typed cache is unavailable or connection handles change
// during capture.
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
	if result := cache.PermissionResult(); result != nil {
		provider.permissions = result
	}
	if cache != k8s.GetResourceCache() || provider.dynamic != k8s.GetDynamicResourceCache() || provider.discovery != k8s.GetResourceDiscovery() {
		return nil
	}
	return provider
}

func (p *CacheProvider) ListTyped(resource string, namespaces []string) ([]runtime.Object, error) {
	// A namespace whose informer has synced is read from its own store, so a
	// kind still loading elsewhere — whose lister is withheld until it has
	// loaded everywhere — can still be searched there.
	if namespaces != nil {
		if objs, ok := p.cache.ReadableObjects(resource, namespaces); ok {
			return objs, nil
		}
	}
	return k8s.FetchResourceList(p.cache, resource, namespaces)
}

func (p *CacheProvider) ListDynamic(ctx context.Context, gvr schema.GroupVersionResource, namespace string) ([]*unstructured.Unstructured, error) {
	if p.dynamic == nil {
		return nil, fmt.Errorf("%w: dynamic cache", k8s.ErrDynamicNotReady)
	}
	// Search reads every listed object of every watched kind on each query
	// and returns at most Limit of them, so it reads the cached objects in
	// place instead of copying each one. Search must never change an object
	// it lists; TestSearch_LeavesListedObjectsUnchanged guards that.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, ready := p.dynamic.ReadSource(gvr, namespace)
	if !ready {
		return nil, fmt.Errorf("%w: initial sync", k8s.ErrDynamicNotReady)
	}
	items, authoritative, err := k8score.ReadWithSourceCheck(p.dynamic.DynamicResourceCache, gvr, namespace, func() ([]*unstructured.Unstructured, error) {
		return p.dynamic.ListReadOnlyContext(ctx, gvr, namespace)
	})
	if err != nil {
		return nil, err
	}
	if !authoritative {
		return nil, fmt.Errorf("%w: serving informers changed", k8s.ErrDynamicNotReady)
	}
	return items, nil
}

func (p *CacheProvider) DynamicResources() ([]schema.GroupVersionResource, error) {
	if p.dynamic == nil {
		return nil, fmt.Errorf("%w: dynamic cache", k8s.ErrDynamicNotReady)
	}
	if p.discovery == nil {
		return nil, fmt.Errorf("%w: discovery", k8s.ErrDynamicNotReady)
	}
	catalog := p.discovery.Catalog()
	preferred := map[schema.GroupResource]k8score.APIResource{}
	for _, ar := range catalog.Resources {
		key := schema.GroupResource{Group: ar.Group, Resource: ar.Name}
		if previous, ok := preferred[key]; !ok || k8score.IsMoreStableVersion(ar.Version, previous.Version) {
			preferred[key] = ar
		}
	}
	p.catalogResources = make([]k8score.APIResource, 0, len(preferred))
	for _, ar := range preferred {
		p.catalogResources = append(p.catalogResources, ar)
	}
	resources := p.catalogResources
	var err error
	// Every cluster serves core resources, so an empty catalog is discovery
	// that hasn't succeeded yet (its errors are logged, not returned).
	if len(resources) == 0 {
		return nil, fmt.Errorf("%w: API discovery", k8s.ErrDynamicNotReady)
	}
	// Until CRD discovery completes, kinds Radar is about to watch still look
	// unwatched; until API discovery answers for every group, a kind may be
	// missing from the catalog.
	switch {
	case p.dynamic.GetDiscoveryStatus() != k8score.CRDDiscoveryComplete:
		err = fmt.Errorf("%w: CRD discovery", k8s.ErrDynamicNotReady)
	case p.discovery.HasPartialDiscovery() || p.discovery.LastRefreshFailed():
		err = errIncompleteDiscovery
	case !catalog.Complete:
		err = fmt.Errorf("%w: API discovery", k8s.ErrDynamicNotReady)
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
	return out, err
}

// UnlistableKinds returns the discovered APIs without a list verb, such as
// create-only reviews, from the catalog captured by the preceding
// DynamicResources call. Before that call, empty is not evidence of their absence.
func (p *CacheProvider) UnlistableKinds() []k8score.APIResource {
	if p.discovery == nil {
		return nil
	}
	resources := p.catalogResources
	var out []k8score.APIResource
	for _, ar := range resources {
		if !strings.Contains(ar.Name, "/") && !slices.Contains(ar.Verbs, "list") {
			out = append(out, ar)
		}
	}
	return out
}

func (p *CacheProvider) PlanTyped(kind string, namespaces []string) readplan.Plan {
	return readplan.Typed(p.cache, p.permissions, k8s.CanonicalBuiltinKind(kind), namespaces, isClusterScopedKind(kind))
}

func (p *CacheProvider) PlanDynamic(gvr schema.GroupVersionResource, namespaces []string, clusterScoped bool) readplan.Plan {
	plan := readplan.Dynamic(p, gvr, namespaces, clusterScoped)
	for i := range plan.Partitions {
		part := &plan.Partitions[i]
		if part.Verdict.Code == readreason.KindNotWatchable && k8s.ShouldBypassDynamicInformer(gvr) {
			part.Verdict.Code = readreason.KindNotSearchable
		}
	}
	return plan
}

// Current reports whether the provider still reads the connection Radar is
// on: a search whose caches were replaced mid-read mixes two clusters.
func (p *CacheProvider) Current() bool {
	return p.cache == k8s.GetResourceCache() && p.dynamic == k8s.GetDynamicResourceCache() && p.discovery == k8s.GetResourceDiscovery()
}

func (p *CacheProvider) DynamicCacheStopped() bool {
	return p.dynamic != nil && p.dynamic.IsStopped()
}

func (p *CacheProvider) PrepareDynamicObservations(gvrs []schema.GroupVersionResource) {
	p.observations = nil
	if p.dynamic != nil {
		p.observations = p.dynamic.NewObservationView(gvrs)
	}
}

func (p *CacheProvider) NamespaceObservation(gvr schema.GroupVersionResource, namespace string) k8score.DynamicResourceObservation {
	if p.dynamic == nil {
		return k8score.DynamicResourceObservation{State: k8score.DynamicObservationSyncing}
	}
	var observation k8score.DynamicResourceObservation
	if p.observations != nil {
		observation = p.observations.NamespaceObservation(gvr, namespace)
	} else {
		observation = p.dynamic.NamespaceObservation(gvr, namespace)
	}
	// Radar reads these high-churn built-ins directly and never watches them,
	// so search treats them like list-only APIs instead of warming an informer.
	if k8s.ShouldBypassDynamicInformer(gvr) && observation.State != k8score.DynamicObservationSynced && observation.State != k8score.DynamicObservationSyncing {
		return k8score.DynamicResourceObservation{State: k8score.DynamicObservationUnsupported, ReasonCode: "direct_read_only"}
	}
	return observation
}

func (p *CacheProvider) WarmDynamic(ctx context.Context, gvr schema.GroupVersionResource, namespace string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	if k8s.ShouldBypassDynamicInformer(gvr) {
		return fmt.Errorf("%w: %s are read directly, not watched", k8s.ErrUnknownKind, gvr.Resource)
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

func (p *CacheProvider) KnownMissingNamespaces(names []string) []string {
	return p.cache.KnownMissingNamespaces(names)
}

func (p *CacheProvider) KindForGVR(gvr schema.GroupVersionResource) string {
	if p.discovery == nil {
		return ""
	}
	return p.discovery.GetKindForGVR(gvr)
}

func (p *CacheProvider) NamespacedForGVR(gvr schema.GroupVersionResource) (namespaced, known bool) {
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
