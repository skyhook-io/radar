package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/skyhook-io/radar/internal/server"
)

// listPackagesInput mirrors the /api/packages query params.
type listPackagesInput struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"limit to packages in this namespace (release-namespace match). Default: all namespaces."`
	Source    string `json:"source,omitempty" jsonschema:"limit to rows where this source contributed. Stable response codes are H (Helm API), L (workload labels), C (CRDs), A (Argo Application), F (Flux HelmRelease/Kustomization); this MCP tool also accepts verbose aliases helm, labels, crds, argocd, and fluxcd. The response includes sourceLegend so agents do not have to remember the single-letter codes. The response field sourcesErrored lists sources that failed (e.g. RBAC denied for Helm release secrets) — fewer rows than expected may mean a source dropped out, not that nothing is installed."`
	Chart     string `json:"chart,omitempty" jsonschema:"case-insensitive substring filter on chart name."`
}

var packageSourceLegend = map[string]string{
	"H": "Helm API release metadata",
	"L": "Workload labels and annotations",
	"C": "CustomResourceDefinition registrations",
	"A": "Argo CD Application declaration",
	"F": "Flux HelmRelease or Kustomization declaration",
}

func normalizeMCPPackageSourceFilter(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "":
		return ""
	case "h", "helm", "helm_api", "helm-api":
		return "H"
	case "l", "label", "labels", "workload_labels", "workload-labels":
		return "L"
	case "c", "crd", "crds":
		return "C"
	case "a", "argo", "argocd", "argo_cd", "argo-cd":
		return "A"
	case "f", "flux", "fluxcd", "flux_cd", "flux-cd":
		return "F"
	default:
		return source
	}
}

func handleListPackages(ctx context.Context, req *mcp.CallToolRequest, input listPackagesInput) (*mcp.CallToolResult, any, error) {
	user, groups := userFromContext(ctx)
	var requested []string
	if input.Namespace != "" {
		requested = []string{input.Namespace}
	}
	// Intersect the requested namespace(s) with the user's RBAC-allowed set.
	// nil = all-namespace access; empty = no access (ListPackages returns empty).
	namespaces := filterNamespacesForUser(ctx, requested)
	resp, err := server.ListPackages(ctx, server.ListPackagesParams{
		Namespaces: namespaces,
		Source:     normalizeMCPPackageSourceFilter(input.Source),
		Chart:      input.Chart,
		User:       user,
		Groups:     groups,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("packages: %w", err)
	}
	return toJSONResult(struct {
		server.PackagesResponse
		SourceLegend map[string]string `json:"sourceLegend"`
	}{
		PackagesResponse: resp,
		SourceLegend:     packageSourceLegend,
	})
}
