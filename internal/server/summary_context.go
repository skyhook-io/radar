// Per-request helpers that compute the compact ResourceSummaryContext attached
// to /api/ai/resources/{kind} list rows and /api/search hits.
//
// The shared core (issue index, kind canonicalization, managedBy
// resolution, per-row scope dispatch) lives in
// internal/summarycontext. This file is the REST-specific wrapper —
// it sources topology from the server-wide broadcaster cache and
// otherwise just plumbs arguments through.

package server

import (
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/summarycontext"
)

// newResourceSummaryContextBuilder assembles the per-request closure for the
// list/search handlers. Returns nil when the cache or topology isn't
// available, in which case callers should skip context attachment
// rather than emit empty objects.
//
// Callers pass the namespace list they're scanning so the issue index
// is scoped to just those rows (the full Compose call on a 100-namespace
// cluster is fine; this is mostly belt-and-suspenders for very large
// envs). Pass nil to compose cluster-wide.
//
// Use newSearchSummaryContextBuilder for search, which routes per-hit
// between a namespaced and a cluster-wide index — search returns mixed
// kinds in one response, so a single index can't get both right.
func (s *Server) newResourceSummaryContextBuilder(namespaces []string) summarycontext.Builder {
	provider := issues.NewCacheProvider()
	if provider == nil {
		return nil
	}
	idx := summarycontext.BuildIssueIndex(provider, namespaces)
	return summarycontext.BuilderFromIndexes(s.broadcaster.GetCachedTopology(), idx, idx)
}

// newSearchSummaryContextBuilder is the search-specific variant. Search
// hits are MIXED-kind in one response — a single query can return both
// namespaced Pods and cluster-scoped Nodes. A single issue index can't
// be both: scoped to the user's namespaces it would silently zero
// issueCount on Node/PV/cluster-scoped CRD hits (whose issues live at
// namespace=""); composed cluster-wide it would over-count or pull in
// rows the namespace-restricted user shouldn't see.
//
// Fix: build two indexes per request. namespacedIdx is scoped to
// scanNamespaces (intersection of user RBAC and the query's `ns:`
// modifier). clusterIdx is composed cluster-wide (nil filter) so
// namespace="" issues surface. The returned closure dispatches per-hit
// via k8s.ClassifyKindScope(kind, group). Search-level RBAC
// (CanReadClusterScoped) already gated which cluster-scoped kinds the
// user can see, so the cluster-wide index doesn't expose unauthorized
// rows.
//
// The cluster-wide index is skipped when scanNamespaces is already nil
// (cluster-wide user) — both indexes would be identical, so one pass
// suffices.
func (s *Server) newSearchSummaryContextBuilder(scanNamespaces []string) summarycontext.Builder {
	provider := issues.NewCacheProvider()
	if provider == nil {
		return nil
	}
	namespacedIdx := summarycontext.BuildIssueIndex(provider, scanNamespaces)
	clusterIdx := namespacedIdx
	if scanNamespaces != nil {
		clusterIdx = summarycontext.BuildIssueIndex(provider, nil)
	}
	return summarycontext.BuilderFromIndexes(s.broadcaster.GetCachedTopology(), namespacedIdx, clusterIdx)
}
