package auditcontext

import (
	"sort"

	"github.com/skyhook-io/radar/internal/audit"
	"github.com/skyhook-io/radar/internal/k8s"
	bpaudit "github.com/skyhook-io/radar/pkg/audit"
	"github.com/skyhook-io/radar/pkg/checks"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

// SummarizeResource returns the audit findings scoped to one resource and
// their deterministic rollup. The rollup uses the public Checks severity
// ladder; the returned findings retain the audit engine's raw severity for
// callers that need finding detail. kind must be the Pascal singular form
// stored in Finding.Kind; a mismatch returns the same empty result as a
// resource with no findings.
func SummarizeResource(cache *k8s.ResourceCache, group, kind, namespace, name string) (*resourcecontext.AuditSummary, []bpaudit.Finding) {
	if cache == nil || kind == "" {
		return nil, nil
	}

	// Empty means all namespaces; []string{""} filters to namespace="" instead.
	var namespaces []string
	if namespace != "" {
		namespaces = []string{namespace}
	}
	results := audit.RunFromCache(cache, namespaces, nil)
	if results == nil {
		return nil, nil
	}
	return summarizeFindings(results.Findings, group, kind, namespace, name)
}

func summarizeFindings(findings []bpaudit.Finding, group, kind, namespace, name string) (*resourcecontext.AuditSummary, []bpaudit.Finding) {
	if kind == "" || len(findings) == 0 {
		return nil, nil
	}

	matched := bpaudit.IndexByResource(findings)[bpaudit.ResourceKey(group, kind, namespace, name)]
	if len(matched) == 0 {
		return nil, nil
	}

	sort.Slice(matched, func(i, j int) bool {
		left := checks.SeverityRank(checks.MapSeverity(matched[i].Severity))
		right := checks.SeverityRank(checks.MapSeverity(matched[j].Severity))
		if left != right {
			return left > right
		}
		return matched[i].CheckID < matched[j].CheckID
	})

	return &resourcecontext.AuditSummary{
		Count:           len(matched),
		HighestSeverity: string(checks.MapSeverity(matched[0].Severity)),
		TopFinding:      matched[0].CheckID,
	}, matched
}
