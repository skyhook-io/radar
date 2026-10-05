package server

import (
	"context"
	"errors"
	"log"
	"net/http"
	"slices"
	"sort"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

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
	observeDynamic bool
	key            string
	group          string
	kind           string
	resource       string
	clusterScoped  bool
}

// readWorkspaceKind authorizes and lists one kind, keeping only objects of
// groups. A namespaced kind falls back to the namespaces the caller may list
// when a cluster-wide list is denied; a cluster-scoped kind needs the
// cluster-scope list.
func (s *Server) readWorkspaceKind(r *http.Request, cache *k8s.ResourceCache, k workspaceKind, namespaces, groups []string) (kindAccess, []*unstructured.Unstructured) {
	var acc kindAccess
	var readNamespaces []string
	if k.clusterScoped {
		if !s.canRead(r, k.group, k.resource, "", "list") {
			return kindAccess{state: kindCoverageDenied}, nil
		}
		acc = kindAccess{state: kindCoverageFull, all: true}
	} else {
		allowed, denied, partial, ok := s.listScope(r, namespaces, k.group, k.resource)
		if !ok {
			return kindAccess{state: kindCoverageDenied}, nil
		}
		acc, readNamespaces = accessFromScope(allowed, partial), allowed
		acc.denied = denied
	}

	list, err := listKindInGroups(r.Context(), cache, k, readNamespaces, groups)
	var readErr *k8score.ResourceReadError
	if errors.As(err, &readErr) {
		switch readErr.Code {
		case "kind_sync_pending":
			return kindAccess{state: kindCoverageSyncing}, nil
		case "kind_not_served":
			return kindAccess{state: kindCoverageNotInstalled}, nil
		}
	}
	switch {
	case err == nil:
		return acc, list
	case errors.Is(err, k8s.ErrUnknownDynamicKind):
		return kindAccess{state: kindCoverageNotInstalled}, nil
	case errors.Is(err, errDynamicNotSynced):
		return kindAccess{state: kindCoverageSyncing}, nil
	default:
		log.Printf("[workspace] Failed to list %s.%s: %v", k.kind, k.group, err)
		return kindAccess{state: kindCoverageError}, nil
	}
}

// listKindInGroups lists k in namespaces (nil = all), keeping only objects of
// groups.
func listKindInGroups(ctx context.Context, cache *k8s.ResourceCache, k workspaceKind, namespaces, groups []string) ([]*unstructured.Unstructured, error) {
	ctx, cancel := context.WithTimeout(ctx, k8s.ResourceReadTimeout(k.kind, k.group))
	defer cancel()
	if k.observeDynamic {
		dc, disc := k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery()
		if dc == nil || disc == nil {
			return nil, k8s.ErrDynamicNotReady
		}
		gvr, ok := disc.GetGVRWithGroup(k.kind, k.group)
		if !ok {
			return nil, k8s.ErrUnknownDynamicKind
		}
		list, err := dc.ListComplete(ctx, gvr, namespaces)
		return keepGroups(list, groups), err
	}
	if namespaces == nil {
		list, err := listDynamicSynced(ctx, cache, k.kind, k.group, "")
		if err != nil {
			return nil, err
		}
		return keepGroups(list, groups), nil
	}
	var out []*unstructured.Unstructured
	for _, ns := range namespaces {
		list, err := listDynamicSynced(ctx, cache, k.kind, k.group, ns)
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
