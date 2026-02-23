package mcp

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/skyhook-io/radar/internal/helm"
)

// Helm tool input types

type listHelmReleasesInput struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"filter to a specific namespace"`
}

type getHelmReleaseInput struct {
	Namespace string `json:"namespace" jsonschema:"release namespace"`
	Name      string `json:"name" jsonschema:"release name"`
	Include   string `json:"include,omitempty" jsonschema:"comma-separated extras to include: values, history, diff. Example: values,history"`
	DiffRev1  int    `json:"diff_revision_1,omitempty" jsonschema:"first revision for diff (requires include=diff)"`
	DiffRev2  int    `json:"diff_revision_2,omitempty" jsonschema:"second revision for diff (requires include=diff), defaults to current"`
}

// Helm tool handlers

func handleListHelmReleases(ctx context.Context, req *mcp.CallToolRequest, input listHelmReleasesInput) (*mcp.CallToolResult, any, error) {
	helmClient := helm.GetClient()
	if helmClient == nil {
		return nil, nil, fmt.Errorf("helm is not available (no releases found or helm not installed)")
	}

	releases, err := helmClient.ListReleases(input.Namespace)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list helm releases: %w", err)
	}

	// Return the typed HelmRelease structs directly — they already have
	// health fields (ResourceHealth, HealthIssue, HealthSummary) which
	// provide the AI with actionable status information.
	return toJSONResult(releases)
}

func handleGetHelmRelease(ctx context.Context, req *mcp.CallToolRequest, input getHelmReleaseInput) (*mcp.CallToolResult, any, error) {
	helmClient := helm.GetClient()
	if helmClient == nil {
		return nil, nil, fmt.Errorf("helm is not available (no releases found or helm not installed)")
	}

	detail, err := helmClient.GetRelease(input.Namespace, input.Name)
	if err != nil {
		return nil, nil, fmt.Errorf("release %s/%s not found: %w", input.Namespace, input.Name, err)
	}

	// Build a response map starting with the core detail
	result := map[string]any{
		"name":         detail.Name,
		"namespace":    detail.Namespace,
		"chart":        detail.Chart,
		"chartVersion": detail.ChartVersion,
		"appVersion":   detail.AppVersion,
		"status":       detail.Status,
		"revision":     detail.Revision,
		"updated":      detail.Updated,
		"description":  detail.Description,
		"resources":    detail.Resources,
	}

	if len(detail.Hooks) > 0 {
		result["hooks"] = detail.Hooks
	}
	if len(detail.Dependencies) > 0 {
		result["dependencies"] = detail.Dependencies
	}

	includes := parseIncludes(input.Include)

	if includes["values"] {
		values, err := helmClient.GetValues(input.Namespace, input.Name, false)
		if err != nil {
			log.Printf("[mcp] Failed to get values for %s/%s: %v", input.Namespace, input.Name, err)
			result["valuesError"] = err.Error()
		} else {
			result["values"] = values.UserSupplied
		}
	}

	if includes["history"] {
		result["history"] = detail.History
	}

	if includes["diff"] && input.DiffRev1 > 0 {
		rev2 := input.DiffRev2
		if rev2 == 0 {
			rev2 = detail.Revision // default to current revision
		}
		diff, err := helmClient.GetManifestDiff(input.Namespace, input.Name, input.DiffRev1, rev2)
		if err != nil {
			log.Printf("[mcp] Failed to get manifest diff for %s/%s: %v", input.Namespace, input.Name, err)
			result["diffError"] = err.Error()
		} else {
			result["diff"] = diff
		}
	}

	return toJSONResult(result)
}

// parseIncludes parses a comma-separated include string into a set.
func parseIncludes(s string) map[string]bool {
	result := make(map[string]bool)
	if s == "" {
		return result
	}
	for _, part := range strings.Split(s, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result[trimmed] = true
		}
	}
	return result
}
