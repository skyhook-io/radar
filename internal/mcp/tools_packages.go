package mcp

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/skyhook-io/radar/internal/server"
)

// listPackagesInput mirrors the /api/packages query params. Same shape
// is exposed both as a REST handler and an MCP tool — the tool's job
// is just to surface the merged "what's installed" view in a way an
// AI agent can consume directly.
type listPackagesInput struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"limit to packages in this namespace (release-namespace match). Default: all namespaces."`
	Source    string `json:"source,omitempty" jsonschema:"limit to rows where this source contributed. One of: H (Helm API), L (workload labels), C (CRDs), A (Argo Application), F (Flux HelmRelease/Kustomization)."`
	Chart     string `json:"chart,omitempty" jsonschema:"case-insensitive substring filter on chart name."`
}

// handleListPackages calls the same `server.ListPackages` free function
// the REST handler uses. Single source of truth for the merge logic +
// the 60s namespace-keyed cache; the MCP path is just a different
// transport with the same caching benefits.
func handleListPackages(ctx context.Context, req *mcp.CallToolRequest, input listPackagesInput) (*mcp.CallToolResult, any, error) {
	user, groups := userFromContext(ctx)
	resp, err := server.ListPackages(ctx, server.ListPackagesParams{
		Namespace: input.Namespace,
		Source:    input.Source,
		Chart:     input.Chart,
		User:      user,
		Groups:    groups,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("packages: %w", err)
	}
	return toJSONResult(resp)
}
