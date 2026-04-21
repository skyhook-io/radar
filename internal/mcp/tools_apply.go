package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/skyhook-io/radar/internal/k8s"
)

type applyResourceInput struct {
	YAML      string `json:"yaml" jsonschema:"YAML manifest to apply (supports multi-document with --- separator)"`
	Mode      string `json:"mode,omitempty" jsonschema:"'apply' (default, create-or-update) or 'create' (fail if exists)"`
	DryRun    bool   `json:"dry_run,omitempty" jsonschema:"validate without persisting changes"`
	Namespace string `json:"namespace,omitempty" jsonschema:"override namespace for the resource"`
}

func handleApplyResource(ctx context.Context, req *mcp.CallToolRequest, input applyResourceInput) (*mcp.CallToolResult, any, error) {
	yamlContent := strings.TrimSpace(input.YAML)
	if yamlContent == "" {
		return nil, nil, fmt.Errorf("yaml is required")
	}

	mode := input.Mode
	if mode == "" {
		mode = "apply"
	}
	if mode != "apply" && mode != "create" {
		return nil, nil, fmt.Errorf("mode must be 'apply' or 'create', got %q", mode)
	}

	// Split multi-document YAML
	docs := k8s.SplitYAMLDocuments(yamlContent)
	if len(docs) == 0 {
		return nil, nil, fmt.Errorf("no valid YAML documents found")
	}

	dynClient := k8s.DynamicClientFromContext(ctx)
	if dynClient == nil {
		return nil, nil, fmt.Errorf("not connected to cluster")
	}

	var results []map[string]any
	for i, doc := range docs {
		result, err := k8s.ApplyResourceWithClient(ctx, k8s.ApplyResourceOptions{
			YAML:              doc,
			Mode:              mode,
			DryRun:            input.DryRun,
			NamespaceOverride: input.Namespace,
		}, dynClient)
		if err != nil {
			if len(docs) > 1 {
				return nil, nil, fmt.Errorf("failed on document %d: %w", i+1, err)
			}
			return nil, nil, err
		}

		entry := map[string]any{
			"kind":      result.Kind,
			"name":      result.Name,
			"namespace": result.Namespace,
			"created":   result.Created,
		}
		if input.DryRun {
			entry["dry_run"] = true
		}
		results = append(results, entry)
	}

	if len(results) == 1 {
		results[0]["status"] = "ok"
		action := "applied"
		if mode == "create" {
			action = "created"
		}
		if input.DryRun {
			action += " (dry run)"
		}
		results[0]["message"] = fmt.Sprintf("Successfully %s %s %s/%s", action, results[0]["kind"], results[0]["namespace"], results[0]["name"])
		return toJSONResult(results[0])
	}

	return toJSONResult(map[string]any{
		"status":    "ok",
		"message":   fmt.Sprintf("Successfully processed %d resources", len(results)),
		"resources": results,
	})
}

