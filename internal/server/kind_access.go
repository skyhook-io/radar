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

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

// Coverage states for one kind a workspace lists.
const (
	kindCoverageFull    = "full"
	kindCoveragePartial = "partial"
	kindCoverageDenied  = "denied"
	// kindCoverageUncached: the caller may list the kind, but Radar's informer
	// holds none of the namespaces in scope, so nothing was read.
	kindCoverageUncached     = "uncached"
	kindCoverageNotInstalled = "notInstalled"
	kindCoverageSyncing      = "syncing"
	kindCoverageError        = "error"
)

// KindCoverage states how much of one kind the caller could see.
// DeniedNamespaces (the caller may not list there) and UncachedNamespaces
// (the caller may, but Radar's informer does not hold them) list only
// namespaces already in the caller's scope, so either may be omitted on a
// partial state; AllowedNamespaces is always set on a partial state and is
// the authority for which namespaces were read.
type KindCoverage struct {
	State              string   `json:"state"`
	DeniedNamespaces   []string `json:"deniedNamespaces,omitempty"`
	UncachedNamespaces []string `json:"uncachedNamespaces,omitempty"`
	AllowedNamespaces  []string `json:"allowedNamespaces,omitempty"`
}

// kindAccess is the resolved read scope for one kind. all means every
// namespace in the request's scope (or the cluster-scoped kind itself).
// denied and uncached name the in-scope namespaces left unread, under the
// disclosure rule of listScope.
type kindAccess struct {
	state      string
	all        bool
	namespaces map[string]bool
	denied     []string
	uncached   []string
}

func (a kindAccess) covers(namespace string) bool {
	if a.state != kindCoverageFull && a.state != kindCoveragePartial {
		return false
	}
	return a.all || a.namespaces[namespace]
}

func (a kindAccess) coverage() KindCoverage {
	cov := KindCoverage{State: a.state, DeniedNamespaces: a.denied, UncachedNamespaces: a.uncached}
	if a.state == kindCoveragePartial {
		cov.AllowedNamespaces = make([]string, 0, len(a.namespaces))
		for ns := range a.namespaces {
			cov.AllowedNamespaces = append(cov.AllowedNamespaces, ns)
		}
		sort.Strings(cov.AllowedNamespaces)
	}
	return cov
}

// listScope resolves where the caller may list one namespaced resource: nil
// allowed means the whole request scope.
//
// denied names namespaces only when the candidate set came from the caller —
// their view filter or their RBAC-allowed list. When the scope is "all" the
// candidates are every namespace in Radar's cache, and naming the denied ones
// would disclose namespaces the caller was never shown; partial then carries
// the fact without the names.
func (s *Server) listScope(r *http.Request, namespaces []string, group, resource string) (allowed, denied []string, partial, any bool) {
	if noNamespaceAccess(namespaces) {
		return []string{}, nil, false, false
	}
	if s.canRead(r, group, resource, "", "list") {
		return namespaces, nil, false, true
	}
	candidates := namespaces
	if candidates == nil {
		candidates = allNamespaceNames()
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

func accessFromScope(allowed []string, partial bool) kindAccess {
	acc := kindAccess{state: kindCoverageFull, all: allowed == nil}
	if partial {
		acc.state = kindCoveragePartial
	}
	if allowed != nil {
		acc.namespaces = make(map[string]bool, len(allowed))
		for _, ns := range allowed {
			acc.namespaces[ns] = true
		}
	}
	return acc
}

// typedKindScope resolves where the caller may list a typed kind and which of
// those namespaces Radar's informer actually holds. The informer may itself be
// namespace-scoped when Radar's own identity cannot list the kind
// cluster-wide; what it does not hold is unread, not empty, and not denied.
// read is nil for "every namespace".
func (s *Server) typedKindScope(r *http.Request, cache informerScope, namespaces []string, group, resource string) (acc kindAccess, read []string) {
	allowed, denied, partial, ok := s.listScope(r, namespaces, group, resource)
	if !ok {
		return kindAccess{state: kindCoverageDenied}, []string{}
	}
	within := namespacesWithinCache(cache, resource, allowed)
	var uncached []string
	if allowed != nil {
		for _, ns := range allowed {
			if slices.Contains(within.namespaces, ns) {
				continue
			}
			partial = true
			if namespaces != nil {
				uncached = append(uncached, ns)
			}
		}
		sort.Strings(uncached)
	}
	if within.unavailable {
		return kindAccess{state: kindCoverageUncached, denied: denied, uncached: uncached}, []string{}
	}
	acc = accessFromScope(within.namespaces, partial || within.partial)
	acc.denied, acc.uncached = denied, uncached
	return acc, within.namespaces
}

// workspaceKind is one kind a workspace lists from Radar's dynamic cache.
type workspaceKind struct {
	key           string
	group         string
	kind          string
	resource      string
	clusterScoped bool
}

// readWorkspaceKind authorizes and lists one kind, keeping only objects of
// groups. A namespaced kind falls back to the namespaces the caller may list
// when a cluster-wide list is denied; a cluster-scoped kind needs the
// cluster-scope list. Radar's own watch scope counts as well, as for typed
// kinds: where its identity watches the kind namespace by namespace, what it
// does not hold is unread (uncached), never empty and never syncing forever.
func (s *Server) readWorkspaceKind(r *http.Request, cache *k8s.ResourceCache, k workspaceKind, namespaces, groups []string, budget *syncBudget) (kindAccess, []*unstructured.Unstructured) {
	ctx := r.Context()
	if k.clusterScoped {
		if !s.canRead(r, k.group, k.resource, "", "list") {
			return kindAccess{state: kindCoverageDenied}, nil
		}
		list, err := listKindInGroups(ctx, cache, k, nil, groups, budget)
		if err != nil {
			return kindAccessFromListError(k, err), nil
		}
		return kindAccess{state: kindCoverageFull, all: true}, list
	}
	allowed, denied, partial, ok := s.listScope(r, namespaces, k.group, k.resource)
	if !ok {
		return kindAccess{state: kindCoverageDenied}, nil
	}
	acc := accessFromScope(allowed, partial)
	acc.denied = denied
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
	ctx      context.Context
	deadline time.Time
}

func newSyncBudget(ctx context.Context) *syncBudget {
	return &syncBudget{ctx: ctx, deadline: time.Now().Add(dynamicSyncWait)}
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
		return nil, errDynamicNotSynced
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
	return nil, errDynamicNotSynced
}

// readWorkspaceKindEverywhere reads a kind in every namespace. When Radar's
// identity watches it only namespace by namespace, a cluster-wide read never
// syncs (or is refused outright); each watched namespace that has synced is
// read instead and the rest stays unread, unnamed (the caller did not name the
// scope).
func readWorkspaceKindEverywhere(ctx context.Context, cache *k8s.ResourceCache, k workspaceKind, groups []string, acc kindAccess, budget *syncBudget) (kindAccess, []*unstructured.Unstructured) {
	list, err := listKindInGroups(ctx, cache, k, nil, groups, budget)
	if err == nil {
		return acc, list
	}
	radarDenied := apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err)
	if !errors.Is(err, errDynamicNotSynced) && !radarDenied {
		return kindAccessFromListError(k, err), nil
	}
	dc, gvr, watched, ok := dynamicNamespaceWatches(k)
	if !ok {
		if radarDenied {
			return kindAccess{state: kindCoverageUncached}, nil
		}
		return kindAccess{state: kindCoverageSyncing}, nil
	}
	read := map[string]bool{}
	var out []*unstructured.Unstructured
	for _, ns := range watched {
		if !dc.IsNamespaceSynced(gvr, ns) {
			continue
		}
		items, err := cache.ListDynamicWithGroup(ctx, k.kind, ns, k.group)
		if err != nil {
			continue
		}
		read[ns] = true
		out = append(out, keepGroups(items, groups)...)
	}
	if len(read) == 0 {
		return kindAccess{state: kindCoverageSyncing}, nil
	}
	return kindAccess{state: kindCoveragePartial, namespaces: read, denied: acc.denied}, out
}

// readWorkspaceKindIn reads each allowed namespace on its own, which starts
// that namespace's watch when Radar watches the kind namespace by namespace. A
// namespace Radar's identity cannot watch, or that has not synced in time, is
// unread: uncached, named only when the caller named the scope.
func readWorkspaceKindIn(ctx context.Context, cache *k8s.ResourceCache, k workspaceKind, groups []string, acc kindAccess, allowed []string, callerScoped bool, budget *syncBudget) (kindAccess, []*unstructured.Unstructured) {
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
		case errors.Is(err, errDynamicNotSynced):
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
			return kindAccess{state: kindCoverageSyncing}, nil
		}
		return kindAccess{state: kindCoverageUncached, denied: acc.denied, uncached: named}, nil
	}
	acc.all, acc.namespaces = false, read
	if len(unread) > 0 {
		acc.state, acc.uncached = kindCoveragePartial, named
	}
	return acc, out
}

func kindAccessFromListError(k workspaceKind, err error) kindAccess {
	switch {
	case errors.Is(err, k8s.ErrUnknownDynamicKind):
		return kindAccess{state: kindCoverageNotInstalled}
	case errors.Is(err, errDynamicNotSynced):
		return kindAccess{state: kindCoverageSyncing}
	case apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err):
		// Radar's own identity may not watch it; the caller's access was checked first.
		return kindAccess{state: kindCoverageUncached}
	default:
		log.Printf("[workspace] Failed to list %s.%s: %v", k.kind, k.group, err)
		return kindAccess{state: kindCoverageError}
	}
}

// dynamicNamespaceWatches returns the namespaces Radar's identity watches k in
// when it holds no cluster-wide informer for it.
func dynamicNamespaceWatches(k workspaceKind) (*k8s.DynamicResourceCache, schema.GroupVersionResource, []string, bool) {
	discovery := k8s.GetResourceDiscovery()
	dc := k8s.GetDynamicResourceCache()
	if discovery == nil || dc == nil {
		return nil, schema.GroupVersionResource{}, nil, false
	}
	gvr, found := discovery.GetGVRWithGroup(k.kind, k.group)
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
func listKindInGroups(ctx context.Context, cache *k8s.ResourceCache, k workspaceKind, namespaces, groups []string, budget *syncBudget) ([]*unstructured.Unstructured, error) {
	if namespaces == nil {
		list, err := listDynamicSyncedWithin(ctx, cache, k.kind, k.group, "", budget)
		if err != nil {
			return nil, err
		}
		return keepGroups(list, groups), nil
	}
	var out []*unstructured.Unstructured
	for _, ns := range namespaces {
		list, err := listDynamicSyncedWithin(ctx, cache, k.kind, k.group, ns, budget)
		if err != nil {
			return nil, err
		}
		out = append(out, keepGroups(list, groups)...)
	}
	return out, nil
}

// keepGroups drops anything whose apiVersion is not one of groups, so an
// object of another group can never ride along on a kind-name match (a Velero
// Backup on a CloudNativePG Backup, a CAPI Cluster on a CNPG Cluster).
func keepGroups(items []*unstructured.Unstructured, groups []string) []*unstructured.Unstructured {
	out := items[:0:0]
	for _, u := range items {
		if u == nil || !slices.Contains(groups, u.GroupVersionKind().Group) {
			continue
		}
		out = append(out, u)
	}
	return out
}
