package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/yaml"

	"github.com/skyhook-io/radar/internal/k8s"
)

type applyResourceInput struct {
	YAML      string `json:"yaml" jsonschema:"YAML manifest to apply (supports multi-document with --- separator)"`
	Mode      string `json:"mode,omitempty" jsonschema:"'apply' (default, create-or-update) or 'create' (fail if exists)"`
	DryRun    bool   `json:"dry_run,omitempty" jsonschema:"validate without persisting changes"`
	Namespace string `json:"namespace,omitempty" jsonschema:"override namespace for the resource"`
	Verify    *bool  `json:"verify,omitempty" jsonschema:"return compact post-mutation state, rollout/pod status for workloads, and current related issues. Default true; set false for a terse write result."`
}

type applyMutationTarget struct {
	Kind      string
	Group     string
	Namespace string
	Name      string
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

	verify := input.Verify == nil || *input.Verify
	var results []map[string]any
	for i, doc := range docs {
		target, targetErr := applyDocMutationTarget(doc, input.Namespace)
		var before *unstructured.Unstructured
		var beforeErr string
		var targetGVR schema.GroupVersionResource
		if verify && !input.DryRun && targetErr == nil {
			targetGVR, before, beforeErr = preReadApplyMutationTarget(ctx, dynClient, target)
		}
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
		if target.Group != "" {
			entry["group"] = target.Group
		}
		if input.DryRun {
			entry["dry_run"] = true
		}
		if len(result.Warnings) > 0 {
			entry["warnings"] = result.Warnings
		}
		if verify && !input.DryRun {
			if targetErr != nil {
				entry["verification"] = map[string]any{"error": targetErr.Error()}
			} else {
				desired := applyDocDesiredObject(doc)
				entry["verification"] = buildMutationVerification(ctx, dynClient, mutationVerificationOptions{
					Kind:      target.Kind,
					Group:     target.Group,
					Namespace: target.Namespace,
					Name:      target.Name,
					GVR:       targetGVR,
					Before:    before,
					BeforeErr: beforeErr,
					Desired:   desired,
				})
			}
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

func applyDocDesiredObject(doc string) *unstructured.Unstructured {
	var obj unstructured.Unstructured
	if err := yaml.Unmarshal([]byte(doc), &obj.Object); err != nil {
		return nil
	}
	return &obj
}

func preReadApplyMutationTarget(ctx context.Context, dynClient dynamic.Interface, target applyMutationTarget) (schema.GroupVersionResource, *unstructured.Unstructured, string) {
	gvr, namespaced, err := resolveMutationGVR(target.Kind, target.Group)
	if err != nil {
		return schema.GroupVersionResource{}, nil, err.Error()
	}
	if namespaced && target.Namespace == "" {
		return gvr, nil, fmt.Sprintf("namespace is required for namespaced kind %q", target.Kind)
	}
	if !namespaced && target.Namespace != "" {
		return gvr, nil, fmt.Sprintf("namespace must be empty for cluster-scoped kind %q", target.Kind)
	}

	resClient := dynClient.Resource(gvr)
	var client dynamic.ResourceInterface = resClient
	if target.Namespace != "" {
		client = resClient.Namespace(target.Namespace)
	}
	got, err := client.Get(ctx, target.Name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return gvr, nil, ""
		}
		return gvr, nil, err.Error()
	}
	return gvr, got, ""
}

func applyDocMutationTarget(doc, namespaceOverride string) (applyMutationTarget, error) {
	var obj unstructured.Unstructured
	if err := yaml.Unmarshal([]byte(doc), &obj.Object); err != nil {
		return applyMutationTarget{}, fmt.Errorf("failed to parse applied resource for verification: %w", err)
	}
	kind := obj.GetKind()
	if kind == "" {
		return applyMutationTarget{}, fmt.Errorf("applied resource has no kind")
	}
	name := obj.GetName()
	if name == "" {
		return applyMutationTarget{}, fmt.Errorf("applied %s has no metadata.name", kind)
	}
	namespace := obj.GetNamespace()
	if namespaceOverride != "" {
		namespace = namespaceOverride
	}
	gvk := schema.FromAPIVersionAndKind(obj.GetAPIVersion(), kind)
	return applyMutationTarget{
		Kind:      kind,
		Group:     gvk.Group,
		Namespace: namespace,
		Name:      name,
	}, nil
}
