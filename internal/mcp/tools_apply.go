package mcp

import (
	"context"
	"errors"
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
	DryRun    bool   `json:"dry_run,omitempty" jsonschema:"validate and preview the server-side result without persisting changes"`
	Namespace string `json:"namespace,omitempty" jsonschema:"override namespace for the resource"`
	Verify    *bool  `json:"verify,omitempty" jsonschema:"return compact post-mutation state, rollout/pod status, and related issues; on dry_run return a preview diff. Default true; set false for a terse write result."`
	Force     bool   `json:"force,omitempty" jsonschema:"apply mode only: force server-side apply field ownership conflicts and take ownership from other managers. Default false; use only when you intend to override Helm/Flux/GitOps/kubectl ownership."`
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
	var partialFailure bool
	for i, doc := range docs {
		target, targetErr := applyDocMutationTarget(doc, input.Namespace)
		var before *unstructured.Unstructured
		var beforeErr string
		var targetGVR schema.GroupVersionResource
		if verify && targetErr == nil {
			targetGVR, before, beforeErr = preReadApplyMutationTarget(ctx, dynClient, target)
		}
		result, err := k8s.ApplyResourceWithClient(ctx, k8s.ApplyResourceOptions{
			YAML:              doc,
			Mode:              mode,
			DryRun:            input.DryRun,
			NamespaceOverride: input.Namespace,
			Force:             input.Force,
		}, dynClient)
		if err != nil {
			if len(docs) > 1 {
				results = append(results, applyFailureEntry(i+1, err))
				partialFailure = true
				continue
			}
			return nil, nil, formatApplyResourceError(err)
		}

		entry := map[string]any{
			"document":  i + 1,
			"status":    applyDocumentStatus(result.Created),
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
		if verify {
			if targetErr != nil {
				entry["verification"] = map[string]any{"error": targetErr.Error()}
			} else {
				desired := applyDocDesiredObject(doc)
				entry["verification"] = buildMutationVerification(ctx, dynClient, mutationVerificationOptions{
					Kind:        target.Kind,
					Group:       target.Group,
					Namespace:   target.Namespace,
					Name:        target.Name,
					GVR:         targetGVR,
					Post:        result.Object,
					Before:      before,
					BeforeErr:   beforeErr,
					Desired:     desired,
					PreviewDiff: input.DryRun,
				})
			}
		}
		results = append(results, entry)
	}

	if partialFailure {
		return toJSONResult(map[string]any{
			"status":    "partial_failure",
			"message":   "One or more documents failed; successful documents may already be applied",
			"resources": results,
		})
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
		namespace, _ := results[0]["namespace"].(string)
		name, _ := results[0]["name"].(string)
		results[0]["message"] = fmt.Sprintf("Successfully %s %s %s", action, results[0]["kind"], resourceDisplayName(namespace, name))
		return toJSONResult(results[0])
	}

	return toJSONResult(map[string]any{
		"status":    "ok",
		"message":   fmt.Sprintf("Successfully processed %d resources", len(results)),
		"resources": results,
	})
}

func applyDocumentStatus(created bool) string {
	if created {
		return "created"
	}
	return "applied"
}

func applyFailureEntry(document int, err error) map[string]any {
	entry := map[string]any{
		"document": document,
		"status":   "failed",
		"error":    formatApplyResourceError(err).Error(),
	}
	if conflict, conflictType, ok := applyConflictDetails(err); ok {
		entry["error_type"] = conflictType
		entry["conflict"] = conflict
	}
	return entry
}

func formatApplyResourceError(err error) error {
	if conflict, conflictType, ok := applyConflictDetails(err); ok && conflictType == "ssa_field_ownership_conflict" {
		if msg, _ := conflict["message"].(string); msg != "" {
			return fmt.Errorf("server-side apply field ownership conflict: %s", msg)
		}
		return fmt.Errorf("server-side apply field ownership conflict: %w", err)
	}
	return err
}

func applyConflictDetails(err error) (map[string]any, string, bool) {
	if !apierrors.IsConflict(err) {
		return nil, "", false
	}
	out := map[string]any{
		"kind": "conflict",
	}
	conflictType := "conflict"
	var statusErr *apierrors.StatusError
	if errors.As(err, &statusErr) {
		out["message"] = statusErr.ErrStatus.Message
		if statusErr.ErrStatus.Reason != "" {
			out["reason"] = string(statusErr.ErrStatus.Reason)
		}
		if statusErr.ErrStatus.Details != nil {
			if statusErr.ErrStatus.Details.Name != "" {
				out["name"] = statusErr.ErrStatus.Details.Name
			}
			if statusErr.ErrStatus.Details.Group != "" {
				out["group"] = statusErr.ErrStatus.Details.Group
			}
			if statusErr.ErrStatus.Details.Kind != "" {
				out["resource"] = statusErr.ErrStatus.Details.Kind
			}
			if len(statusErr.ErrStatus.Details.Causes) > 0 {
				causes := make([]map[string]any, 0, len(statusErr.ErrStatus.Details.Causes))
				for _, cause := range statusErr.ErrStatus.Details.Causes {
					entry := map[string]any{}
					if cause.Type != "" {
						entry["type"] = string(cause.Type)
						if cause.Type == metav1.CauseTypeFieldManagerConflict {
							conflictType = "ssa_field_ownership_conflict"
							out["kind"] = "server_side_apply_field_ownership"
						}
					}
					if cause.Field != "" {
						entry["field"] = cause.Field
					}
					if cause.Message != "" {
						entry["message"] = cause.Message
					}
					if len(entry) > 0 {
						causes = append(causes, entry)
					}
				}
				if len(causes) > 0 {
					out["causes"] = causes
				}
			}
		}
	} else {
		out["message"] = err.Error()
	}
	return out, conflictType, true
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
