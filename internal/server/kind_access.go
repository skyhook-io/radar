package server

import (
	"context"
	"errors"
	"log"
	"net/http"
	"slices"
	"sort"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	integration "github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

// listScope resolves where the caller may list one namespaced Resource: nil
// allowed means the whole request scope.
//
// denied names namespaces only when the candidate set came from the caller —
// their view filter or their RBAC-allowed list. When the scope is "all" the
// candidates are every namespace in Radar's cache, and naming the denied ones
// would disclose namespaces the caller was never shown; partial then carries
// the fact without the names.
func (s *Server) listScope(r *http.Request, namespaces []string, group, resource string) (allowed, denied []string, partial, any bool) {
	return s.listScopeWithCandidates(r, namespaces, group, resource, allNamespaceNames)
}

func (s *Server) listScopeWithCandidates(r *http.Request, namespaces []string, group, resource string, knownNamespaces func() []string) (allowed, denied []string, partial, any bool) {
	if integration.NoNamespaceAccess(namespaces) {
		return []string{}, nil, false, false
	}
	if s.canRead(r, group, resource, "", "list") {
		return namespaces, nil, false, true
	}
	candidates := namespaces
	if candidates == nil {
		candidates = knownNamespaces()
	}
	if len(candidates) == 0 {
		return []string{}, nil, false, false
	}
	allowed = s.filterNamespacesByCanRead(r, group, resource, "list", candidates)
	partial = len(allowed) < len(candidates)
	if namespaces != nil {
		for _, ns := range candidates {
			if !slices.Contains(allowed, ns) {
				denied = append(denied, ns)
			}
		}
		sort.Strings(denied)
	}
	return allowed, denied, partial, len(allowed) > 0
}

// typedKindScope resolves where the caller may list a typed kind and which of
// those namespaces Radar's informer actually holds. The informer may itself be
// namespace-scoped when Radar's own identity cannot list the kind
// cluster-wide; what it does not hold is unread, not empty, and not denied.
// read is nil for "every namespace".
func (s *Server) typedKindScope(r *http.Request, cache integration.InformerScope, namespaces []string, group, resource string) (acc integration.KindAccess, read []string) {
	return s.typedKindScopeWithCandidates(r, cache, namespaces, group, resource, allNamespaceNames)
}

func (s *Server) typedKindScopeWithCandidates(r *http.Request, cache integration.InformerScope, namespaces []string, group, resource string, candidates func() []string) (acc integration.KindAccess, read []string) {
	allowed, denied, partial, ok := s.listScopeWithCandidates(r, namespaces, group, resource, candidates)
	if !ok {
		return integration.KindAccess{State: integration.KindCoverageDenied}, []string{}
	}
	within := integration.NamespacesWithinCache(cache, resource, allowed)
	var uncached []string
	if allowed != nil {
		for _, ns := range allowed {
			if slices.Contains(within.Namespaces, ns) {
				continue
			}
			partial = true
			if namespaces != nil {
				uncached = append(uncached, ns)
			}
		}
		sort.Strings(uncached)
	}
	if within.Unavailable {
		return integration.KindAccess{State: integration.KindCoverageUncached, Denied: denied, Uncached: uncached}, []string{}
	}
	acc = integration.AccessFromScope(within.Namespaces, partial || within.Partial)
	acc.Denied, acc.Uncached = denied, uncached
	return acc, within.Namespaces
}

// readWorkspaceKind authorizes and lists one kind, keeping only objects of
// groups. A namespaced kind falls back to the namespaces the caller may list
// when a cluster-wide list is denied; a cluster-scoped kind needs the
// cluster-scope list. Radar's own watch scope counts as well, as for typed
// kinds: where its identity watches the kind namespace by namespace, what it
// does not hold is unread (uncached), never empty and never syncing forever.
func (s *Server) readWorkspaceKind(r *http.Request, cache *k8s.ResourceCache, k integration.WorkspaceKind, namespaces, groups []string, budget *syncBudget) (integration.KindAccess, []*unstructured.Unstructured) {
	ctx := r.Context()
	if k.ClusterScoped {
		if !s.canRead(r, k.Group, k.Resource, "", "list") {
			return integration.KindAccess{State: integration.KindCoverageDenied}, nil
		}
		list, err := listKindInGroups(ctx, cache, k, nil, groups, budget)
		if err != nil {
			return kindAccessFromListError(k, err), nil
		}
		return integration.KindAccess{State: integration.KindCoverageFull, All: true}, list
	}
	allowed, denied, partial, ok := s.listScopeWithCandidates(r, namespaces, k.Group, k.Resource, func() []string { return namespaceNamesInCache(cache) })
	if !ok {
		return integration.KindAccess{State: integration.KindCoverageDenied}, nil
	}
	acc := integration.AccessFromScope(allowed, partial)
	acc.Denied = denied
	if allowed == nil {
		return readWorkspaceKindEverywhere(ctx, cache, k, groups, acc, budget)
	}
	return readWorkspaceKindIn(ctx, cache, k, groups, acc, allowed, namespaces != nil, budget)
}

// syncBudget bounds how long one workspace read waits on the dynamic cache,
// across every kind and namespace it reads: starting a watch, which probes the
// apiserver, as well as waiting for it to sync. Once it is spent, or the
// request is cancelled, a namespace that has not synced is unread.
type syncBudget struct {
	ctx          context.Context
	deadline     time.Time
	bound        bool
	discovery    *k8s.ResourceDiscovery
	dynamicCache *k8s.DynamicResourceCache
}

func newSyncBudget(ctx context.Context) *syncBudget {
	return &syncBudget{ctx: ctx, deadline: time.Now().Add(dynamicSyncWait)}
}

func (b *syncBudget) dynamicDependencies() (*k8s.ResourceDiscovery, *k8s.DynamicResourceCache) {
	if b != nil && b.bound {
		return b.discovery, b.dynamicCache
	}
	return k8s.GetResourceDiscovery(), k8s.GetDynamicResourceCache()
}

// listBlocking is ListBlocking within the budget; a nil budget waits
// dynamicSyncWait for the sync alone. A watch still starting when the budget
// runs out carries on in the background, so a later read finds it.
func (b *syncBudget) listBlocking(dc *k8s.DynamicResourceCache, gvr schema.GroupVersionResource, namespace string) ([]*unstructured.Unstructured, error) {
	if b == nil {
		return dc.ListBlocking(gvr, namespace, dynamicSyncWait)
	}
	if dc.IsNamespaceSynced(gvr, namespace) {
		return dc.ListBlocking(gvr, namespace, 0)
	}
	if b.ctx.Err() != nil {
		return nil, integration.ErrDynamicNotSynced
	}
	wait := max(0, time.Until(b.deadline))
	type result struct {
		items []*unstructured.Unstructured
		err   error
	}
	done := make(chan result, 1)
	go func() {
		items, err := dc.ListBlocking(gvr, namespace, wait)
		done <- result{items, err}
	}()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case r := <-done:
		return r.items, r.err
	case <-timer.C:
	case <-b.ctx.Done():
	}
	return nil, integration.ErrDynamicNotSynced
}

// readWorkspaceKindEverywhere reads a kind in every namespace. When Radar's
// identity watches it only namespace by namespace, a cluster-wide read never
// syncs (or is refused outright); each watched namespace that has synced is
// read instead and the rest stays unread, unnamed (the caller did not name the
// scope).
func readWorkspaceKindEverywhere(ctx context.Context, cache *k8s.ResourceCache, k integration.WorkspaceKind, groups []string, acc integration.KindAccess, budget *syncBudget) (integration.KindAccess, []*unstructured.Unstructured) {
	list, err := listKindInGroups(ctx, cache, k, nil, groups, budget)
	if err == nil {
		return acc, list
	}
	radarDenied := apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err)
	if !errors.Is(err, integration.ErrDynamicNotSynced) && !radarDenied {
		return kindAccessFromListError(k, err), nil
	}
	dc, gvr, watched, ok := dynamicNamespaceWatches(k, budget)
	if !ok {
		if radarDenied {
			return integration.KindAccess{State: integration.KindCoverageUncached}, nil
		}
		return integration.KindAccess{State: integration.KindCoverageSyncing}, nil
	}
	read := map[string]bool{}
	var out []*unstructured.Unstructured
	for _, ns := range watched {
		if !dc.IsNamespaceSynced(gvr, ns) {
			continue
		}
		items, err := listDynamicSyncedWithin(ctx, cache, k.Kind, k.Group, ns, budget)
		if err != nil {
			continue
		}
		read[ns] = true
		out = append(out, integration.KeepGroups(items, groups)...)
	}
	if len(read) == 0 {
		return integration.KindAccess{State: integration.KindCoverageSyncing}, nil
	}
	return integration.KindAccess{State: integration.KindCoveragePartial, Namespaces: read, Denied: acc.Denied}, out
}

// readWorkspaceKindIn reads each allowed namespace on its own, which starts
// that namespace's watch when Radar watches the kind namespace by namespace. A
// namespace Radar's identity cannot watch, or that has not synced in time, is
// unread: uncached, named only when the caller named the scope.
func readWorkspaceKindIn(ctx context.Context, cache *k8s.ResourceCache, k integration.WorkspaceKind, groups []string, acc integration.KindAccess, allowed []string, callerScoped bool, budget *syncBudget) (integration.KindAccess, []*unstructured.Unstructured) {
	read := map[string]bool{}
	var out []*unstructured.Unstructured
	var unread []string
	syncing := false
	for _, ns := range allowed {
		items, err := listKindInGroups(ctx, cache, k, []string{ns}, groups, budget)
		switch {
		case err == nil:
			read[ns] = true
			out = append(out, items...)
		case errors.Is(err, integration.ErrDynamicNotSynced):
			syncing = true
			unread = append(unread, ns)
		case apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err):
			unread = append(unread, ns)
		default:
			return kindAccessFromListError(k, err), nil
		}
	}
	var named []string
	if callerScoped {
		named = unread
		sort.Strings(named)
	}
	if len(read) == 0 {
		if syncing {
			return integration.KindAccess{State: integration.KindCoverageSyncing}, nil
		}
		return integration.KindAccess{State: integration.KindCoverageUncached, Denied: acc.Denied, Uncached: named}, nil
	}
	acc.All, acc.Namespaces = false, read
	if len(unread) > 0 {
		acc.State, acc.Uncached = integration.KindCoveragePartial, named
	}
	return acc, out
}

func kindAccessFromListError(k integration.WorkspaceKind, err error) integration.KindAccess {
	switch {
	case errors.Is(err, k8s.ErrUnknownDynamicKind):
		return integration.KindAccess{State: integration.KindCoverageNotInstalled}
	case errors.Is(err, integration.ErrDynamicNotSynced):
		return integration.KindAccess{State: integration.KindCoverageSyncing}
	case apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err):
		// Radar's own identity may not watch it; the caller's access was checked first.
		return integration.KindAccess{State: integration.KindCoverageUncached}
	default:
		log.Printf("[workspace] Failed to list %s.%s: %v", k.Kind, k.Group, err)
		return integration.KindAccess{State: integration.KindCoverageError}
	}
}

// dynamicNamespaceWatches returns the namespaces Radar's identity watches k in
// when it holds no cluster-wide informer for it.
func dynamicNamespaceWatches(k integration.WorkspaceKind, budget *syncBudget) (*k8s.DynamicResourceCache, schema.GroupVersionResource, []string, bool) {
	discovery, dc := budget.dynamicDependencies()
	if discovery == nil || dc == nil {
		return nil, schema.GroupVersionResource{}, nil, false
	}
	gvr, found := discovery.GetGVRWithGroup(k.Kind, k.Group)
	if !found {
		return nil, schema.GroupVersionResource{}, nil, false
	}
	obs := dc.Observation(gvr)
	if obs.Scope != k8score.DynamicObservationScopeExplicitNamespaces || len(obs.Namespaces) == 0 {
		return nil, schema.GroupVersionResource{}, nil, false
	}
	return dc, gvr, obs.Namespaces, true
}

// listKindInGroups lists k in namespaces (nil = all), keeping only objects of
// groups, waiting for sync no longer than budget allows.
func listKindInGroups(ctx context.Context, cache *k8s.ResourceCache, k integration.WorkspaceKind, namespaces, groups []string, budget *syncBudget) ([]*unstructured.Unstructured, error) {
	if namespaces == nil {
		list, err := listDynamicSyncedWithin(ctx, cache, k.Kind, k.Group, "", budget)
		if err != nil {
			return nil, err
		}
		return integration.KeepGroups(list, groups), nil
	}
	var out []*unstructured.Unstructured
	for _, ns := range namespaces {
		list, err := listDynamicSyncedWithin(ctx, cache, k.Kind, k.Group, ns, budget)
		if err != nil {
			return nil, err
		}
		out = append(out, integration.KeepGroups(list, groups)...)
	}
	return out, nil
}
