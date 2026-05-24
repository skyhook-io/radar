package mcp

import (
	"sort"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/internal/audit"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	bpaudit "github.com/skyhook-io/radar/pkg/audit"
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
func computeMCPIssueSummary(cache *k8s.ResourceCache, group, kind, namespace, name string) *resourcecontext.IssueSummary {
	if cache == nil {
		return nil
	}
	provider := issues.NewCacheProvider()
	if provider == nil {
		return nil
	}
	filters := issues.Filters{
		Kinds: []string{kind},
		// NoLimit (not MaxLimit): the post-compose filter narrows to a
		// single {group, kind, ns, name}, so a hard cap before that
		// filter can silently drop the target resource's rows when the
		// kind has many same-namespace siblings whose issues outrank
		// the target on (severity, last_seen). Mirrors
		// summarycontext.BuildIssueIndex's identical rationale.
		Limit: issues.NoLimit,
	}
	if namespace != "" {
		filters.Namespaces = []string{namespace}
	}
	rows, _ := issues.ComposeWithStats(provider, filters)

	matched := make([]issues.Issue, 0, len(rows))
	bySource := make(map[string]int)
	for _, row := range rows {
		if row.Name != name {
			continue
		}
		if namespace != "" && row.Namespace != namespace {
			continue
		}
		if row.Group != group {
			continue
		}
		matched = append(matched, row)
		bySource[string(row.Source)]++
	}
	if len(matched) == 0 {
		return nil
	}
	// (severity desc, Reason asc) — deterministic across runs.
	sort.Slice(matched, func(i, j int) bool {
		ri, rj := mcpComposeSeverityRank(matched[i].Severity), mcpComposeSeverityRank(matched[j].Severity)
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

func mcpComposeSeverityRank(s issues.Severity) int {
	switch s {
	case issues.SeverityCritical:
		return 2
	case issues.SeverityWarning:
		return 1
	}
	return 0
}

// computeMCPAuditSummary looks up audit findings for the subject resource
// via the group-aware (group, Kind, ns, name) key. Mirrors the REST
// handler's computeAuditSummaryForResource.
//
// kind MUST be Pascal singular — the audit check runner writes that into
// Finding.Kind, and Finding.Group is populated by audit.buildResults via
// the built-in (Kind→Group) table, so the lookup keys correctly.
func computeMCPAuditSummary(cache *k8s.ResourceCache, group, kind, namespace, name string) *resourcecontext.AuditSummary {
	if cache == nil || kind == "" {
		return nil
	}
	var namespaces []string
	if namespace != "" {
		namespaces = []string{namespace}
	}
	results := audit.RunFromCache(cache, namespaces, nil)
	if results == nil || len(results.Findings) == 0 {
		return nil
	}
	idx := bpaudit.IndexByResource(results.Findings)
	match := idx[bpaudit.ResourceKey(group, kind, namespace, name)]
	if len(match) == 0 {
		return nil
	}

	sort.Slice(match, func(i, j int) bool {
		ri, rj := mcpAuditSeverityRank(match[i].Severity), mcpAuditSeverityRank(match[j].Severity)
		if ri != rj {
			return ri > rj
		}
		return match[i].CheckID < match[j].CheckID
	})

	return &resourcecontext.AuditSummary{
		Count:           len(match),
		HighestSeverity: mcpNormalizeAuditSeverity(match[0].Severity),
		TopFinding:      match[0].CheckID,
	}
}

func mcpAuditSeverityRank(s string) int {
	switch s {
	case bpaudit.SeverityDanger:
		return 2
	case bpaudit.SeverityWarning:
		return 1
	}
	return 0
}

// mcpNormalizeAuditSeverity maps the audit suite's emission vocabulary
// ("danger" / "warning") onto the unified resourceContext severity scale
// ("critical" / "warning") used by issueSummary. Two sibling fields in
// the same response reporting severity in different vocabularies is a
// wire-shape footgun — mirror the REST handler's normalizeAuditSeverity.
func mcpNormalizeAuditSeverity(s string) string {
	switch s {
	case bpaudit.SeverityDanger:
		return string(issues.SeverityCritical)
	case bpaudit.SeverityWarning:
		return string(issues.SeverityWarning)
	}
	return s
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

