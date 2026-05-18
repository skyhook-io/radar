// Per-request helpers that compute the compact ResourceSummaryContext attached
// to list_resources rows and search hits served via MCP.
//
// The shared core (issue index, kind canonicalization, managedBy
// resolution, per-row scope dispatch) lives in
// internal/summarycontext. This file is the MCP-specific wrapper — it
// sources topology from a short-TTL per-process memoizer (MCP has no
// shared broadcaster cache) and otherwise just plumbs arguments through.

package mcp

import (
	"time"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/summarycontext"
	"github.com/skyhook-io/radar/pkg/topology"
)

// newResourceSummaryContextBuilder assembles the per-request closure for MCP
// list_resources. Returns nil when the cache or topology isn't
// available, in which case the caller should skip context attachment
// rather than emit empty objects.
//
// namespaces scopes the issue index to just the rows being returned;
// pass nil for cluster-wide.
//
// Use newSearchSummaryContextBuilder for MCP search, which routes
// per-hit between a namespaced and a cluster-wide index — search
// returns mixed kinds in one response, so a single index can't get
// both right.
func newResourceSummaryContextBuilder(namespaces []string) summarycontext.Builder {
	provider := issues.NewCacheProvider()
	if provider == nil {
		return nil
	}
	idx := summarycontext.BuildIssueIndex(provider, namespaces)
	return summarycontext.BuilderFromIndexes(buildSummaryContextTopology(namespaces), idx, idx)
}

// newSearchSummaryContextBuilder is the MCP search variant. Mirrors
// internal/server.newSearchSummaryContextBuilder — see that comment for
// the dual-index rationale (mixed-kind hits, cluster-scoped issues at
// namespace=""). MCP search-level RBAC (CanReadClusterScoped via
// canReadClusterScopedKind) already gates which cluster-scoped kinds
// are reachable, so composing the cluster-wide index doesn't leak
// rows the user can't see.
func newSearchSummaryContextBuilder(scanNamespaces []string) summarycontext.Builder {
	provider := issues.NewCacheProvider()
	if provider == nil {
		return nil
	}
	namespacedIdx := summarycontext.BuildIssueIndex(provider, scanNamespaces)
	clusterIdx := namespacedIdx
	if scanNamespaces != nil {
		clusterIdx = summarycontext.BuildIssueIndex(provider, nil)
	}
	return summarycontext.BuilderFromIndexes(buildSummaryContextTopology(scanNamespaces), namespacedIdx, clusterIdx)
}

// summaryCtxTopoMemo caches topology builds across summary-context list and
// search invocations. MCP has no shared broadcaster cache, so without
// memoization every list_resources / search call from an agent pays a
// full topology build (multi-second on multi-thousand-resource clusters).
// 5s TTL matches the REST broadcaster's cadence — short enough that
// managedBy stays current after a context switch, long enough that a
// burst of agent calls amortizes the build cost.
//
// Other MCP tools (handleGetResource, get_neighborhood) still build
// inline; threading them through here is a separate follow-up.
var summaryCtxTopoMemo = topology.NewMemoizer(5 * time.Second)

// buildSummaryContextTopology returns a topology snapshot suitable for
// resolving managedBy pointers, reusing a cached snapshot when one is
// fresh. Returns nil on failure — the caller falls back to a
// managedBy-less ResourceSummaryContext rather than failing the response.
func buildSummaryContextTopology(namespaces []string) *topology.Topology {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil
	}
	opts := topology.DefaultBuildOptions()
	if len(namespaces) > 0 {
		opts.Namespaces = namespaces
	}
	topo, err := summaryCtxTopoMemo.Get(opts, func() (*topology.Topology, error) {
		builder := topology.NewBuilder(k8s.NewTopologyResourceProvider(cache)).
			WithDynamic(k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery()))
		return builder.Build(opts)
	})
	if err != nil {
		return nil
	}
	return topo
}
