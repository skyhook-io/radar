package mcp

import (
	"context"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/internal/auditcontext"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/policyreports"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	topo "github.com/skyhook-io/radar/pkg/topology"
)

// mcpPolicyReportLookupAdapter wraps k8s.GetPolicyReportIndex into the
// resourcecontext.PolicyReportLookup interface. Mirrors the REST adapter in
// internal/server/ai_handlers.go — keeping the projection narrow here lets
// pkg/policyreports.Finding evolve without perturbing the wire contract.
type mcpPolicyReportLookupAdapter struct {
	idx *policyreports.Index
}

func (a mcpPolicyReportLookupAdapter) FindingsFor(group, kind, namespace, name string) []resourcecontext.KyvernoFinding {
	if a.idx == nil {
		return nil
	}
	findings := a.idx.FindingsFor(group, kind, namespace, name)
	if len(findings) == 0 {
		return nil
	}
	out := make([]resourcecontext.KyvernoFinding, len(findings))
	for i, f := range findings {
		out[i] = resourcecontext.KyvernoFinding{
			Policy:  f.Policy,
			Rule:    f.Rule,
			Result:  f.Result,
			Message: f.Message,
		}
	}
	return out
}

type mcpServiceBackendLookup struct {
	cache *k8s.ResourceCache
}

func (l mcpServiceBackendLookup) PodsForServiceSelector(namespace string, selector labels.Selector) ([]*corev1.Pod, error) {
	if l.cache == nil || l.cache.Pods() == nil {
		return nil, nil
	}
	return l.cache.Pods().Pods(namespace).List(selector)
}

// computeMCPIssueSummary rolls up per-resource issue-composer rows
// (problem + condition) into an IssueSummary. Mirrors the
// REST handler's computeIssueSummaryForResource — same composer call, same
// group-aware iteration filter, same deterministic sort. The composer's
// native namespace filter restricts the scan to the resource's namespace;
// the per-row group check prevents cross-group collisions where a CRD and
// a built-in share kind+ns+name.
//
// Pascal-singular kind required: the composer's Filters.Kinds matcher
// case-folds both sides but doesn't plural-to-singular convert. Callers
// pass canonicalKind from obj's TypeMeta.
func computeMCPIssueSummary(ctx context.Context, cache *k8s.ResourceCache, group, kind, namespace, name string) *resourcecontext.IssueSummary {
	if cache == nil {
		return nil
	}
	provider := issues.NewCacheProvider()
	if provider == nil {
		return nil
	}
	namespaces := issueNamespacesForResource(namespace)
	// RelatedIssues is owner-aware and uncapped: get_resource on a workload
	// surfaces the GROUPED issues its pods are evidence for (was empty — the
	// old flat-by-exact-resource match looked for Kind=Deployment rows, but the
	// evidence is Kind=Pod), and on a pod past the inline-Members cap too.
	matched := issues.RelatedIssues(provider, issues.RelatedIssueOptions{
		Namespaces:           namespaces,
		CanReadClusterScoped: issueClusterScopedAccess(ctx),
		CanReadRelated:       issueRelatedResourceAccess(ctx),
	}, group, kind, namespace, name)
	if len(matched) == 0 {
		return nil
	}
	bySource := make(map[string]int, len(matched))
	for _, row := range matched {
		bySource[string(row.Source)]++
	}
	// (severity desc, Reason asc) — deterministic across runs.
	sort.Slice(matched, func(i, j int) bool {
		ri, rj := issues.SeverityRank(matched[i].Severity), issues.SeverityRank(matched[j].Severity)
		if ri != rj {
			return ri > rj
		}
		return matched[i].Reason < matched[j].Reason
	})
	return &resourcecontext.IssueSummary{
		Count:           len(matched),
		HighestSeverity: string(matched[0].Severity),
		TopReason:       matched[0].Reason,
		BySource:        bySource,
	}
}

func issueNamespacesForResource(namespace string) []string {
	if namespace == "" {
		return nil
	}
	return []string{namespace}
}

// issueClusterScopedAccess mirrors the issues_list gate so every composer entry
// point applies the same cluster-scoped authorization — both to the rows
// themselves and to the cluster-scoped state (NodePool specs) folded into them.
func issueClusterScopedAccess(ctx context.Context) func(kind, group string) bool {
	return func(kind, group string) bool {
		return canReadClusterScopedKind(ctx, kind, group, "list")
	}
}

func issueRelatedResourceAccess(ctx context.Context) func(issues.Ref) bool {
	return func(ref issues.Ref) bool {
		user, _ := resolveUserPerms(ctx)
		if user == nil {
			return true
		}
		if ref.Namespace != "" {
			if !checkNamespaceAccess(ctx, ref.Namespace) {
				return false
			}
			if strings.EqualFold(ref.Kind, "Secret") {
				return canReadInNamespace(ctx, ref.Group, "secrets", ref.Namespace, "get")
			}
			return true
		}
		clusterScoped, _, _ := k8s.ClassifyKindScope(ref.Kind, ref.Group)
		return clusterScoped && canReadClusterScopedKind(ctx, ref.Kind, ref.Group, "get")
	}
}

func computeMCPAuditSummary(cache *k8s.ResourceCache, group, kind, namespace, name string) *resourcecontext.AuditSummary {
	summary, _ := auditcontext.SummarizeResource(cache, group, kind, namespace, name)
	return summary
}

// mcpTopologyForContext returns a per-call topology snapshot scoped to the
// resource's namespace (cluster-scoped resources get an all-namespaces
// build). Reuses the package-level summaryCtxTopoMemo cache to amortize
// build cost across get_resource and list_resources / search calls. nil
// return is fine — Build then skips topology-derived fields and the
// remaining sidecar still populates.
func mcpTopologyForContext(namespace string) (*topo.Topology, topo.ResourceProvider, topo.DynamicProvider, bool) {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil, nil, nil, false
	}
	opts := topo.DefaultBuildOptions()
	// Match the REST handler's build options (see ai_handlers.go) so MCP
	// get_resource produces the same relationship context as REST. Without
	// these the topology drops the RS layer for Pod→Deployment chains and
	// the relationship cache uses a thinner shape — silently weakening
	// resourceContext for MCP callers.
	opts.IncludeReplicaSets = true
	opts.ForRelationshipCache = true
	if namespace != "" {
		opts.Namespaces = []string{namespace}
	}
	provider := k8s.NewTopologyResourceProvider(cache)
	dyn := k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery())

	topology, err := summaryCtxTopoMemo.Get(opts, func() (*topo.Topology, error) {
		return topo.NewBuilder(provider).WithDynamic(dyn).Build(opts)
	})
	if err != nil || topology == nil {
		return nil, nil, nil, false
	}
	return topology, provider, dyn, true
}
