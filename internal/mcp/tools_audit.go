package mcp

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/skyhook-io/radar/internal/audit"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/settings"
	bp "github.com/skyhook-io/radar/pkg/audit"
	"github.com/skyhook-io/radar/pkg/checks"
)

type auditInput struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"filter to a specific namespace"`
	Category  string `json:"category,omitempty" jsonschema:"filter by category: Security, Reliability, or Efficiency"`
	Severity  string `json:"severity,omitempty" jsonschema:"filter by posture remediation priority: critical, high, medium, or low. Current built-in checks emit high or medium."`
	Limit     int    `json:"limit,omitempty" jsonschema:"max audit violation findings to return (default 30, max 100). This limits findings only; compliant resources are not returned."`
}

type auditToolResult struct {
	Summary    auditSummary   `json:"summary"`
	Findings   []auditFinding `json:"findings"`
	TotalCount int            `json:"totalCount"`
	Truncated  bool           `json:"truncated,omitempty"`
	NarrowHint string         `json:"narrowHint,omitempty"`
}

type auditSummary struct {
	Critical   int            `json:"critical"`
	High       int            `json:"high"`
	Medium     int            `json:"medium"`
	Low        int            `json:"low"`
	Resources  int            `json:"resources"`
	Categories map[string]int `json:"categories"`
}

type auditFinding struct {
	Resource    string          `json:"resource"` // "Deployment/default/web"
	Check       string          `json:"check"`    // "runAsRoot"
	Severity    checks.Severity `json:"severity"`
	Category    string          `json:"category"` // "Security"
	Message     string          `json:"message"`
	Remediation string          `json:"remediation,omitempty"`
}

func handleGetAudit(ctx context.Context, req *mcp.CallToolRequest, input auditInput) (*mcp.CallToolResult, any, error) {
	severity, err := parseAuditSeverity(input.Severity)
	if err != nil {
		return nil, nil, err
	}

	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil, nil, fmt.Errorf("not connected to cluster")
	}

	var requested []string
	if input.Namespace != "" {
		requested = []string{input.Namespace}
	}
	allowed := filterNamespacesForUser(ctx, requested)
	if allowed != nil && len(allowed) == 0 {
		return toJSONResult(auditToolResult{})
	}

	// For namespace-restricted users, narrow the audit scope to their allowed
	// set (cluster-scoped findings are filtered out below). For cluster-admins
	// (allowed == nil) we pass through to RunFromCache's default behavior.
	namespaces := requested
	if allowed != nil && len(requested) == 0 {
		namespaces = allowed
	}

	results := audit.RunFromCache(cache, namespaces, nil)
	if results == nil {
		return toJSONResult(auditToolResult{})
	}

	// Apply user settings (namespace ignore + disabled checks)
	cfg := loadAuditConfig()
	results = bp.ApplySettings(results, cfg.IgnoredNamespaces, cfg.DisabledChecks)

	// Build the check registry lookup for remediation
	registry := bp.CheckRegistry

	// Filter and transform findings
	limit := input.Limit
	if limit <= 0 {
		limit = 30
	}
	if limit > 100 {
		limit = 100
	}

	// For namespace-restricted users, drop findings outside their allowed
	// set (covers cluster-scoped findings and findings on objects the
	// listNamespaced helper let through with empty namespace).
	var nsAllow map[string]bool
	if allowed != nil {
		nsAllow = make(map[string]bool, len(allowed))
		for _, ns := range allowed {
			nsAllow[ns] = true
		}
	}

	catCounts, filtered := collectAuditToolFindings(results.Findings, registry, nsAllow, input.Category, severity)
	summary := summarizeAuditToolFindings(filtered, catCounts)

	totalCount := len(filtered)
	truncated := false
	narrowHint := ""
	if len(filtered) > limit {
		filtered = filtered[:limit]
		truncated = true
		narrowHint = fmt.Sprintf(
			"returned %d of %d findings — narrow with namespace=, category=, severity=, or raise limit",
			limit, totalCount,
		)
	}

	return toJSONResult(auditToolResult{
		Summary:    summary,
		Findings:   filtered,
		TotalCount: totalCount,
		Truncated:  truncated,
		NarrowHint: narrowHint,
	})
}

func collectAuditToolFindings(
	findings []bp.Finding,
	registry map[string]bp.CheckMeta,
	nsAllow map[string]bool,
	category string,
	severity checks.Severity,
) (map[string]int, []auditFinding) {
	// Category counts stay post-RBAC + post-severity-filter + pre-category-filter
	// so an agent can see which categories remain available before narrowing.
	categories := map[string]int{}
	var filtered []auditFinding
	for _, f := range findings {
		if nsAllow != nil && !nsAllow[f.Namespace] {
			continue
		}
		effectiveSeverity := checks.MapSeverity(f.Severity)
		if severity != "" && effectiveSeverity != severity {
			continue
		}
		categories[f.Category]++
		if category != "" && f.Category != category {
			continue
		}
		remediation := ""
		if meta, ok := registry[f.CheckID]; ok {
			remediation = meta.Remediation
		}
		filtered = append(filtered, auditFinding{
			Resource:    fmt.Sprintf("%s/%s/%s", f.Kind, f.Namespace, f.Name),
			Check:       f.CheckID,
			Severity:    effectiveSeverity,
			Category:    f.Category,
			Message:     f.Message,
			Remediation: remediation,
		})
	}
	return categories, filtered
}

func summarizeAuditToolFindings(findings []auditFinding, categories map[string]int) auditSummary {
	summary := auditSummary{Categories: categories}
	resources := map[string]struct{}{}
	for _, f := range findings {
		switch f.Severity {
		case checks.SeverityCritical:
			summary.Critical++
		case checks.SeverityHigh:
			summary.High++
		case checks.SeverityMedium:
			summary.Medium++
		case checks.SeverityLow:
			summary.Low++
		}
		resources[f.Resource] = struct{}{}
	}
	summary.Resources = len(resources)
	return summary
}

func parseAuditSeverity(value string) (checks.Severity, error) {
	if value == "" {
		return "", nil
	}
	if !checks.ValidSeverity(value) {
		return "", fmt.Errorf("unknown audit severity %q (want: critical, high, medium, low)", value)
	}
	return checks.Severity(value), nil
}

func loadAuditConfig() settings.AuditConfig {
	s := settings.Load()
	if s.Audit != nil {
		return *s.Audit
	}
	return settings.DefaultAuditConfig()
}
