package mcp

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/k8s"
	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
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
	Namespace string `json:"namespace" jsonschema:"Helm release storage namespace; use storageNamespace from list_helm_releases when present, otherwise namespace"`
	Name      string `json:"name" jsonschema:"release name"`
	Include   string `json:"include,omitempty" jsonschema:"comma-separated extras to include: values (key-aware redacted), history, operations, diff, values_diff, notes_diff, resource_diff. Hook diagnostics are included by default when present. Example: values,history"`
	DiffRev1  int    `json:"diff_revision_1,omitempty" jsonschema:"first revision for revision diffs; used when include contains diff, values_diff, notes_diff, or resource_diff"`
	DiffRev2  int    `json:"diff_revision_2,omitempty" jsonschema:"second revision for revision diffs; used when include contains diff, values_diff, notes_diff, or resource_diff; defaults to current"`
}

// Helm tool handlers

func handleListHelmReleases(ctx context.Context, req *mcp.CallToolRequest, input listHelmReleasesInput) (*mcp.CallToolResult, any, error) {
	helmClient := helm.GetClient()
	if helmClient == nil {
		return nil, nil, fmt.Errorf("helm is not available (no releases found or helm not installed)")
	}

	username, groups := userFromContext(ctx)
	namespaces := resolveHelmListNamespaces(ctx, input.Namespace)
	if namespaces != nil && len(namespaces) == 0 {
		return toJSONResult([]helm.HelmRelease{})
	}
	releases, err := helmClient.ListReleasesAcrossNamespaces(namespaces, username, groups)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list helm releases: %w", err)
	}

	// Return the typed HelmRelease structs directly — they already have
	// health fields (ResourceHealth, HealthIssue, HealthSummary) which
	// provide the AI with actionable status information.
	return toJSONResult(releases)
}

func resolveHelmListNamespaces(ctx context.Context, namespace string) []string {
	if namespace != "" {
		return []string{namespace}
	}
	if pkgauth.UserFromContext(ctx) != nil {
		allowed := filterNamespacesForUser(ctx, nil)
		if allowed != nil {
			return allowed
		}
		if canReadInNamespace(ctx, "", "secrets", "", "list") {
			return nil
		}
		if scoped := filterNamespacesByCanRead(ctx, "", "secrets", "list", mcpAllNamespaceNames(ctx)); len(scoped) > 0 {
			return scoped
		}
		return nil
	}
	return helm.ResolveNoAuthListNamespaces(ctx)
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
	helm.EnrichHookDiagnosticsWithClusterEvidence(ctx, detail, k8s.ClientFromContext(ctx))

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
	if detail.StorageNamespace != "" {
		result["storageNamespace"] = detail.StorageNamespace
	}
	if detail.ResourceHealth != "" {
		result["resourceHealth"] = detail.ResourceHealth
	}
	if detail.HealthIssue != "" {
		result["healthIssue"] = detail.HealthIssue
	}
	if detail.HealthSummary != "" {
		result["healthSummary"] = detail.HealthSummary
	}
	if detail.LastOperation != nil {
		result["lastOperation"] = detail.LastOperation
	}
	if detail.OperationInsight != nil {
		result["operationInsight"] = detail.OperationInsight
	}
	if detail.ManagedByFluxHelmRelease != "" {
		result["managedByFluxHelmRelease"] = detail.ManagedByFluxHelmRelease
	}

	if len(detail.Hooks) > 0 {
		result["hooks"] = detail.Hooks
	}
	if len(detail.HookDiagnostics) > 0 {
		result["hookDiagnostics"] = detail.HookDiagnostics
	}
	if len(detail.Dependencies) > 0 {
		result["dependencies"] = detail.Dependencies
	}

	includes := parseIncludes(input.Include)

	// Mirror the frontend gate on sensitive Helm reads: viewers cannot pull
	// values/manifest/diff. Without this the user would still be blocked
	// by K8s RBAC (view ClusterRole excludes secrets), but the error would
	// be a confusing K8s "secrets is forbidden" rather than the structured
	// cloud_role_insufficient code the frontend emits.
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
				result["values"] = redactedHelmValues(values.UserSupplied)
				result["valuesRedacted"] = true
			}
		}
	}

	if includes["history"] {
		result["history"] = detail.History
	}
	if includes["operations"] {
		result["operations"] = mergeHelmOperations(detail.Operations, detail.LastOperation)
	}

	if includes["diff"] {
		if errMsg := diffRevisionError(input, detail.Revision, "diff"); errMsg != "" {
			// Surface the contract gap instead of silently producing no diff.
			result["diffError"] = errMsg
		} else if gatedSensitive {
			result["diffError"] = fmt.Sprintf("Radar Cloud role %q cannot view Helm release diffs (requires member or higher)", cloudRole.String())
		} else {
			rev1, rev2 := diffRevisions(input, detail.Revision)
			diff, err := helmClient.GetManifestDiffAsUser(input.Namespace, input.Name, rev1, rev2, username, groups)
			if err != nil {
				log.Printf("[mcp] Failed to get manifest diff for %s/%s: %v", input.Namespace, input.Name, err)
				result["diffError"] = err.Error()
			} else {
				result["diff"] = diff
			}
		}
	}
	if includes["values_diff"] {
		if errMsg := diffRevisionError(input, detail.Revision, "values_diff"); errMsg != "" {
			result["valuesDiffError"] = errMsg
		} else if gatedSensitive {
			result["valuesDiffError"] = fmt.Sprintf("Radar Cloud role %q cannot view Helm release value diffs (requires member or higher)", cloudRole.String())
		} else {
			rev1, rev2 := diffRevisions(input, detail.Revision)
			diff, err := helmClient.GetValuesDiffAsUser(input.Namespace, input.Name, rev1, rev2, false, username, groups)
			if err != nil {
				log.Printf("[mcp] Failed to get values diff for %s/%s: %v", input.Namespace, input.Name, err)
				result["valuesDiffError"] = err.Error()
			} else {
				diff.Diff = aicontext.RedactSecrets(diff.Diff)
				result["valuesDiff"] = diff
			}
		}
	}
	if includes["notes_diff"] {
		if errMsg := diffRevisionError(input, detail.Revision, "notes_diff"); errMsg != "" {
			result["notesDiffError"] = errMsg
		} else if gatedSensitive {
			result["notesDiffError"] = fmt.Sprintf("Radar Cloud role %q cannot view Helm release notes diffs (requires member or higher)", cloudRole.String())
		} else {
			rev1, rev2 := diffRevisions(input, detail.Revision)
			diff, err := helmClient.GetNotesDiffAsUser(input.Namespace, input.Name, rev1, rev2, username, groups)
			if err != nil {
				log.Printf("[mcp] Failed to get notes diff for %s/%s: %v", input.Namespace, input.Name, err)
				result["notesDiffError"] = err.Error()
			} else {
				result["notesDiff"] = diff
			}
		}
	}
	if includes["resource_diff"] {
		if errMsg := diffRevisionError(input, detail.Revision, "resource_diff"); errMsg != "" {
			result["resourceDiffError"] = errMsg
		} else if gatedSensitive {
			result["resourceDiffError"] = fmt.Sprintf("Radar Cloud role %q cannot view Helm release resource diffs (requires member or higher)", cloudRole.String())
		} else {
			rev1, rev2 := diffRevisions(input, detail.Revision)
			diff, err := helmClient.GetResourceDiffAsUser(input.Namespace, input.Name, rev1, rev2, username, groups)
			if err != nil {
				log.Printf("[mcp] Failed to get resource diff for %s/%s: %v", input.Namespace, input.Name, err)
				result["resourceDiffError"] = err.Error()
			} else {
				result["resourceDiff"] = diff
			}
		}
	}

	return toJSONResult(result)
}

func diffRevisionError(input getHelmReleaseInput, currentRevision int, include string) string {
	if input.DiffRev1 <= 0 {
		return fmt.Sprintf("include=%s requires diff_revision_1 (the earlier revision to compare); diff_revision_2 defaults to current", include)
	}
	_, rev2 := diffRevisions(input, currentRevision)
	if rev2 <= 0 {
		return fmt.Sprintf("include=%s requires a positive diff_revision_2 or a current release revision", include)
	}
	if input.DiffRev1 == rev2 {
		return fmt.Sprintf("include=%s requires different revisions; diff_revision_1 and diff_revision_2 both resolved to %d", include, rev2)
	}
	return ""
}

func diffRevisions(input getHelmReleaseInput, currentRevision int) (int, int) {
	rev2 := input.DiffRev2
	if rev2 == 0 {
		rev2 = currentRevision
	}
	return input.DiffRev1, rev2
}

func mergeHelmOperations(operations []helm.HelmOperation, lastOperation *helm.HelmOperation) []helm.HelmOperation {
	merged := make([]helm.HelmOperation, 0, len(operations)+1)
	seen := make(map[string]struct{}, len(operations)+1)
	if lastOperation != nil {
		key := helmOperationKey(*lastOperation)
		seen[key] = struct{}{}
		merged = append(merged, *lastOperation)
	}
	for _, op := range operations {
		key := helmOperationKey(op)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		merged = append(merged, op)
	}
	return merged
}

func helmOperationKey(operation helm.HelmOperation) string {
	return fmt.Sprintf(
		"%s:%s:%d:%d:%d:%d",
		operation.Kind,
		operation.Status,
		operation.Revision,
		operation.FailedRevision,
		operation.RollbackRevision,
		operation.TargetRevision,
	)
}

func redactedHelmValues(values map[string]any) map[string]any {
	if values == nil {
		return map[string]any{}
	}
	cloned, ok := cloneHelmValue(values).(map[string]any)
	if !ok {
		return map[string]any{}
	}
	aicontext.RedactInlineSecrets(cloned)
	return cloned
}

func cloneHelmValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = cloneHelmValue(item)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[fmt.Sprint(key)] = cloneHelmValue(item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = cloneHelmValue(item)
		}
		return out
	default:
		return value
	}
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
