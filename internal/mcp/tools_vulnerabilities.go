package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/vulnerabilities"
	"github.com/skyhook-io/radar/pkg/readreason"
)

type listVulnerabilitiesInput struct {
	View      string `json:"view,omitempty" jsonschema:"image (default), cve or workload"`
	Namespace string `json:"namespace,omitempty" jsonschema:"limit to one namespace"`
	Severity  string `json:"severity,omitempty" jsonschema:"comma-separated: critical, high, medium, low, unknown"`
	Fixable   bool   `json:"fixable,omitempty" jsonschema:"keep only findings with a fixed version"`
	Query     string `json:"query,omitempty" jsonschema:"image, CVE ID, package or workload substring"`
	Workload  string `json:"workload,omitempty" jsonschema:"Kind/namespace/name, e.g. Deployment/shop/checkout"`
	Limit     int    `json:"limit,omitempty" jsonschema:"max rows (default 20, max 100)"`
}

const (
	defaultVulnerabilityRows = 20
	maxVulnerabilityRows     = 100
	// Per row, an agent needs the worst few findings or images, not all.
	vulnerabilityDetailsPerRow = 10
)

// handleListVulnerabilities answers with the same rollup as GET
// /api/vulnerabilities. Reports are read only where the caller may list the
// report kind, as search and list_resources gate Trivy reports; the
// workloads the answer names are checked per kind, as every MCP reference is.
func handleListVulnerabilities(ctx context.Context, _ *mcp.CallToolRequest, input listVulnerabilitiesInput) (*mcp.CallToolResult, any, error) {
	view, err := vulnerabilities.ParseView(input.View)
	if err != nil {
		return nil, nil, err
	}
	severities, err := vulnerabilities.ParseSeverities(input.Severity)
	if err != nil {
		return nil, nil, err
	}
	filter := vulnerabilities.Filter{Severities: severities, FixableOnly: input.Fixable, Query: input.Query}
	if input.Workload != "" {
		parts := strings.Split(input.Workload, "/")
		if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
			return nil, nil, fmt.Errorf("workload must be Kind/namespace/name, e.g. Deployment/shop/checkout")
		}
		filter.Workload = &vulnerabilities.WorkloadRef{Kind: parts[0], Namespace: parts[1], Name: parts[2]}
	}
	limit := input.Limit
	if limit <= 0 {
		limit = defaultVulnerabilityRows
	}
	limit = min(limit, maxVulnerabilityRows)

	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil, nil, errNotConnected()
	}
	var requested []string
	if input.Namespace != "" {
		requested = []string{input.Namespace}
	}
	namespaces, checkFailed := filterNamespacesForUserWithStatus(ctx, requested)
	kindRBAC := mcpSearchKindRBAC(ctx)
	resp, err := vulnerabilities.Read(ctx, cache, vulnerabilities.Request{
		Namespaces:           namespaces,
		NamespaceCheckFailed: checkFailed,
		ReportAccess:         mcpSearchKindAccess(ctx, kindRBAC),
		CanRead:              summaryRefReader(ctx),
		// The inventory of what runs is a Pod list, as get_runtime_versions reads it.
		CanListPods: func(namespace string) bool { return canReadInNamespace(ctx, "", "pods", namespace, "list") },
		View:        view,
		Filter:      filter,
	})
	switch {
	case errors.Is(err, vulnerabilities.ErrNotInstalled):
		return nil, nil, readErrorf(readreason.KindUnknown, "%s", err.Error())
	case errors.Is(err, vulnerabilities.ErrNotReady):
		return nil, nil, readErrorf(readreason.KindSyncPending, "%s", err.Error())
	case err != nil:
		return nil, nil, err
	}
	return toJSONResult(trimVulnerabilities(resp, limit))
}

// trimVulnerabilities caps rows at limit and each row's findings or images
// at vulnerabilityDetailsPerRow; rowsOmitted says how many rows were left.
func trimVulnerabilities(resp vulnerabilities.Response, limit int) vulnerabilities.Response {
	omitted := 0
	if len(resp.Images) > limit {
		omitted, resp.Images = len(resp.Images)-limit, resp.Images[:limit]
	}
	for i := range resp.Images {
		resp.Images[i].Findings = resp.Images[i].Findings[:min(len(resp.Images[i].Findings), vulnerabilityDetailsPerRow)]
	}
	if len(resp.CVEs) > limit {
		omitted, resp.CVEs = len(resp.CVEs)-limit, resp.CVEs[:limit]
	}
	for i := range resp.CVEs {
		resp.CVEs[i].Affected = resp.CVEs[i].Affected[:min(len(resp.CVEs[i].Affected), vulnerabilityDetailsPerRow)]
	}
	if len(resp.Workloads) > limit {
		omitted, resp.Workloads = len(resp.Workloads)-limit, resp.Workloads[:limit]
	}
	if len(resp.Unscanned) > limit {
		resp.Unscanned = resp.Unscanned[:limit]
	}
	for i := range resp.Unscanned {
		resp.Unscanned[i].Workloads = resp.Unscanned[i].Workloads[:min(len(resp.Unscanned[i].Workloads), vulnerabilityDetailsPerRow)]
	}
	for i := range resp.Images {
		resp.Images[i].Workloads = resp.Images[i].Workloads[:min(len(resp.Images[i].Workloads), vulnerabilityDetailsPerRow)]
	}
	resp.Coverage.RowsOmitted += omitted
	return resp
}
