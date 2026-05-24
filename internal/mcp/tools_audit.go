package mcp

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/skyhook-io/radar/internal/audit"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/settings"
	bp "github.com/skyhook-io/radar/pkg/audit"
)

type auditInput struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"filter to a specific namespace"`
	Category  string `json:"category,omitempty" jsonschema:"filter by category: Security, Reliability, or Efficiency"`
	Severity  string `json:"severity,omitempty" jsonschema:"filter by severity: danger or warning"`
	Limit     int    `json:"limit,omitempty" jsonschema:"max audit violation findings to return (default 30, max 100). This limits findings only; compliant resources are not returned."`
}

type auditToolResult struct {
	Summary    auditSummary   `json:"summary"`
	Findings   []auditFinding `json:"findings"`
	TotalCount int            `json:"totalCount"`
	Truncated  bool           `json:"truncated,omitempty"`
}

type auditSummary struct {
	Critical   int            `json:"critical"`
	Warning    int            `json:"warning"`
	Resources  int            `json:"resources"`
	Categories map[string]int `json:"categories"`
}

type auditFinding struct {
	Resource    string `json:"resource"` // "Deployment/default/web"
	Check       string `json:"check"`    // "runAsRoot"
	Severity    string `json:"severity"` // "danger" or "warning"
	Category    string `json:"category"` // "Security"
	Message     string `json:"message"`
	Remediation string `json:"remediation,omitempty"`
}

func handleGetAudit(ctx context.Context, req *mcp.CallToolRequest, input auditInput) (*mcp.CallToolResult, any, error) {
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

	// Category counts are built post-namespace-filter so restricted users
	// don't see totals that include namespaces they can't access.
	catCounts := map[string]int{}
	var filtered []auditFinding
	for _, f := range results.Findings {
		if nsAllow != nil && !nsAllow[f.Namespace] {
			continue
		}
		catCounts[f.Category]++

		if input.Category != "" && f.Category != input.Category {
			continue
		}
		if input.Severity != "" && f.Severity != input.Severity {
			continue
		}
		remediation := ""
		if meta, ok := registry[f.CheckID]; ok {
			remediation = meta.Remediation
		}
		filtered = append(filtered, auditFinding{
			Resource:    fmt.Sprintf("%s/%s/%s", f.Kind, f.Namespace, f.Name),
			Check:       f.CheckID,
			Severity:    f.Severity,
			Category:    f.Category,
			Message:     f.Message,
			Remediation: remediation,
		})
	}

	// Counts come from `filtered` so Summary reflects the agent's namespace
	// / category / severity filters the same way Findings does. Categories
	// stays post-RBAC + pre-category-filter so the agent can still see
	// which categories have findings before narrowing.
	var critical, warning int
	resourceSet := map[string]struct{}{}
	for _, f := range filtered {
		switch f.Severity {
		case "danger", "critical":
			critical++
		case "warning":
			warning++
		}
		resourceSet[f.Resource] = struct{}{}
	}

	totalCount := len(filtered)
	truncated := false
	if len(filtered) > limit {
		filtered = filtered[:limit]
		truncated = true
	}

	return toJSONResult(auditToolResult{
		Summary: auditSummary{
			Critical:   critical,
			Warning:    warning,
			Resources:  len(resourceSet),
			Categories: catCounts,
		},
		Findings:   filtered,
		TotalCount: totalCount,
		Truncated:  truncated,
	})
}

func loadAuditConfig() settings.AuditConfig {
	s := settings.Load()
	if s.Audit != nil {
		return *s.Audit
	}
	return settings.DefaultAuditConfig()
}
