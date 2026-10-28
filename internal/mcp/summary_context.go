// Per-request helpers that compute the compact ResourceSummaryContext attached
// to list_resources rows and search hits served via MCP.
//
// The shared core (issue index, kind canonicalization, managedBy
// resolution, per-row scope dispatch) lives in
// internal/summarycontext. This file is the MCP-specific wrapper — it
// reuses the host’s indexed relationship graph, or the shared detail memo
// when standalone, and otherwise just plumbs arguments through.

package mcp

import (
	"context"
	"time"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/summarycontext"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/topology"
)

var summaryTopologyGate = summarycontext.NewBudgetGate(1)

// newResourceSummaryContextBuilder assembles the per-request closure for MCP
// list_resources. A missing typed cache returns nil; WithBudget turns that into
// cheap context with omissions for unavailable enrichment.
//
// namespaces scopes the issue index to just the rows being returned;
// pass nil for cluster-wide.
//
// Use newSearchSummaryContextBuilder for MCP search, which routes
// per-hit between a namespaced and a cluster-wide index — search
// returns mixed kinds in one response, so a single index can't get
// both right.
func newResourceSummaryContextBuilder(ctx context.Context, namespaces []string) summarycontext.Builder {
	provider := issues.NewCacheProvider()
	if provider == nil {
		return nil
	}
	idx := summarycontext.BuildIssueIndex(provider, issues.Filters{Namespaces: namespaces, CanReadClusterScoped: issueClusterScopedAccess(ctx), CanReadRelated: issueRelatedResourceAccess(ctx), CanReadSubject: issueSubjectAccess(ctx), CanReadEvidence: issueEvidenceAccess(ctx)})
	return summarycontext.BuilderFromIndexes(ctx, func() summarycontext.IndexedTopology {
		return summarycontext.WithinContext(ctx, summaryTopologyGate, buildSummaryContextTopology)
	}, idx, idx, summaryRefReader(ctx), summaryAgentOwnership(ctx))
}

// newSearchSummaryContextBuilder is the MCP search variant. Mirrors
// internal/server.newSearchSummaryContextBuilder — see that comment for
// the dual-index rationale (mixed-kind hits, cluster-scoped issues at
// namespace=""). MCP search-level RBAC (CanReadClusterScoped via
// canReadClusterScopedKind) already gates which cluster-scoped kinds
// are reachable, so composing the cluster-wide index doesn't leak
// rows the user can't see.
func newSearchSummaryContextBuilder(ctx context.Context, scanNamespaces []string) summarycontext.Builder {
	provider := issues.NewCacheProvider()
	if provider == nil {
		return nil
	}
	namespacedIdx := summarycontext.BuildIssueIndex(provider, issues.Filters{Namespaces: scanNamespaces, CanReadClusterScoped: issueClusterScopedAccess(ctx), CanReadRelated: issueRelatedResourceAccess(ctx), CanReadSubject: issueSubjectAccess(ctx), CanReadEvidence: issueEvidenceAccess(ctx)})
	clusterIdx := namespacedIdx
	if scanNamespaces != nil {
		clusterIdx = summarycontext.BuildIssueIndex(provider, issues.Filters{Namespaces: nil, CanReadClusterScoped: issueClusterScopedAccess(ctx), CanReadRelated: issueRelatedResourceAccess(ctx), CanReadSubject: issueSubjectAccess(ctx), CanReadEvidence: issueEvidenceAccess(ctx)})
	}
	return summarycontext.BuilderFromIndexes(ctx, func() summarycontext.IndexedTopology {
		return summarycontext.WithinContext(ctx, summaryTopologyGate, buildSummaryContextTopology)
	}, namespacedIdx, clusterIdx, summaryRefReader(ctx), summaryAgentOwnership(ctx))
}

// summaryCtxTopoMemo is the standalone-host fallback shared by detail and
// summaries. The server supplies its maintained relationship graph instead.
var summaryCtxTopoMemo = topology.NewMemoizer(5 * time.Second)

func buildSummaryContextTopology() summarycontext.IndexedTopology {
	graph, index, _, _, _ := mcpTopologyForContext()
	return summarycontext.IndexedTopology{Topology: graph, Index: index}
}

// summaryRefReader is the caller's access to a reference a summary names, as
// the resource context checks it.
func summaryRefReader(ctx context.Context) func(group, kind, namespace string) bool {
	checker := newMCPRequestScopedChecker(ctx)
	return func(group, kind, namespace string) bool { return checker.CanRead(ctx, group, kind, namespace) }
}

// summaryAgentOwnership reads each row's controller chain as the caller,
// with the access get_resource's owner answer reads with.
func summaryAgentOwnership(ctx context.Context) *k8s.AgentOwnership {
	return k8s.NewAgentOwnership(func(c *ownership.Catalog) ownership.Access { return ownershipAccess(ctx, c) })
}
