package mcp

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/skyhook-io/radar/internal/server"
)

// listPackagesInput mirrors the /api/packages query params.
type listPackagesInput struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"limit to packages in this namespace (release-namespace match). Default: all namespaces."`
	Source    string `json:"source,omitempty" jsonschema:"limit to rows where this source contributed. One of: H (Helm API), L (workload labels), C (CRDs), A (Argo Application), F (Flux HelmRelease/Kustomization). The response field 'sourcesErrored' lists sources that failed (e.g. RBAC denied for Helm release secrets) — fewer rows than expected may mean a source dropped out, not that nothing is installed."`
	Chart     string `json:"chart,omitempty" jsonschema:"case-insensitive substring filter on chart name."`
}

func handleListPackages(ctx context.Context, req *mcp.CallToolRequest, input listPackagesInput) (*mcp.CallToolResult, any, error) {
	user, groups := userFromContext(ctx)
	var namespaces []string
	if input.Namespace != "" {
		namespaces = []string{input.Namespace}
	}
	resp, err := server.ListPackages(ctx, server.ListPackagesParams{
		Namespaces: namespaces,
		Source:     input.Source,
		Chart:      input.Chart,
		User:       user,
		Groups:     groups,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("packages: %w", err)
	}
	return toJSONResult(resp)
}
