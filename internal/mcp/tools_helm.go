package mcp

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/skyhook-io/radar/internal/helm"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

// userFromContext extracts the auth user attached by the HTTP middleware,
// returning ("", nil) when no user is present (auth disabled / local binary).
// The *AsUser Helm methods treat empty username as "use the SA identity",
// so callers can thread this straight through.
func userFromContext(ctx context.Context) (string, []string) {
	if user := pkgauth.UserFromContext(ctx); user != nil {
		return user.Username, user.Groups
	}
	return "", nil
}

// Helm tool input types

type listHelmReleasesInput struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"filter to a specific namespace"`
}

type getHelmReleaseInput struct {
	Namespace string `json:"namespace" jsonschema:"release namespace"`
	Name      string `json:"name" jsonschema:"release name"`
	Include   string `json:"include,omitempty" jsonschema:"comma-separated extras to include: values, history, diff. Example: values,history"`
	DiffRev1  int    `json:"diff_revision_1,omitempty" jsonschema:"first revision for diff; only used when include contains diff"`
	DiffRev2  int    `json:"diff_revision_2,omitempty" jsonschema:"second revision for diff; only used when include contains diff, defaults to current"`
}

// Helm tool handlers

func handleListHelmReleases(ctx context.Context, req *mcp.CallToolRequest, input listHelmReleasesInput) (*mcp.CallToolResult, any, error) {
	helmClient := helm.GetClient()
	if helmClient == nil {
		return nil, nil, fmt.Errorf("helm is not available (no releases found or helm not installed)")
	}

	username, groups := userFromContext(ctx)
	releases, err := helmClient.ListReleasesAsUser(input.Namespace, username, groups)
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

	username, groups := userFromContext(ctx)
	detail, err := helmClient.GetReleaseAsUser(input.Namespace, input.Name, username, groups)
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

	// Mirror the SPA gate on sensitive Helm reads: viewers cannot pull
	// values/manifest/diff. Without this the user would still be blocked
	// by K8s RBAC (view ClusterRole excludes secrets), but the error would
	// be a confusing K8s "secrets is forbidden" rather than the structured
	// cloud_role_insufficient code the SPA emits.
	cloudRole := pkgauth.CloudRoleFromContext(ctx)
	gatedSensitive := !cloudRole.AtLeast(pkgauth.RoleMember)

	if includes["values"] {
		if gatedSensitive {
			result["valuesError"] = fmt.Sprintf("Radar Cloud role %q cannot view Helm release values (requires member or higher)", cloudRole.String())
		} else {
			values, err := helmClient.GetValuesAsUser(input.Namespace, input.Name, false, username, groups)
			if err != nil {
				log.Printf("[mcp] Failed to get values for %s/%s: %v", input.Namespace, input.Name, err)
				result["valuesError"] = err.Error()
			} else {
				result["values"] = values.UserSupplied
			}
		}
	}

	if includes["history"] {
		result["history"] = detail.History
	}

	if includes["diff"] {
		switch {
		case input.DiffRev1 <= 0:
			// Surface the contract gap instead of silently producing no `diff`
			// field — the agent can't tell whether the call was a no-op
			// versus the diff being empty.
			result["diffError"] = "include=diff requires diff_revision_1 (the earlier revision to compare); diff_revision_2 defaults to current"
		case gatedSensitive:
			result["diffError"] = fmt.Sprintf("Radar Cloud role %q cannot view Helm release diffs (requires member or higher)", cloudRole.String())
		default:
			rev2 := input.DiffRev2
			if rev2 == 0 {
				rev2 = detail.Revision // default to current revision
			}
			diff, err := helmClient.GetManifestDiffAsUser(input.Namespace, input.Name, input.DiffRev1, rev2, username, groups)
			if err != nil {
				log.Printf("[mcp] Failed to get manifest diff for %s/%s: %v", input.Namespace, input.Name, err)
				result["diffError"] = err.Error()
			} else {
				result["diff"] = diff
			}
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
