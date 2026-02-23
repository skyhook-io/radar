package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/skyhook-io/radar/internal/k8s"
)

// GitOps tool input types

type manageGitOpsInput struct {
	Action    string `json:"action" jsonschema:"action: sync (ArgoCD), reconcile (FluxCD), suspend, or resume"`
	Tool      string `json:"tool" jsonschema:"gitops tool: argocd or fluxcd"`
	Kind      string `json:"kind,omitempty" jsonschema:"resource kind (FluxCD only): kustomization, helmrelease, gitrepository, etc."`
	Namespace string `json:"namespace" jsonschema:"resource namespace"`
	Name      string `json:"name" jsonschema:"resource name"`
}

// ArgoCD GVR
var argoAppGVR = schema.GroupVersionResource{
	Group:    "argoproj.io",
	Version:  "v1alpha1",
	Resource: "applications",
}

// FluxCD GVRs
var fluxGVRs = map[string]schema.GroupVersionResource{
	"gitrepository":  {Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"},
	"ocirepository":  {Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "ocirepositories"},
	"helmrepository": {Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "helmrepositories"},
	"kustomization":  {Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"},
	"helmrelease":    {Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"},
}

// GitOps tool handler

func handleManageGitOps(ctx context.Context, req *mcp.CallToolRequest, input manageGitOpsInput) (*mcp.CallToolResult, any, error) {
	dynClient := k8s.GetDynamicClient()
	if dynClient == nil {
		return nil, nil, fmt.Errorf("not connected to cluster")
	}

	tool := strings.ToLower(input.Tool)
	action := strings.ToLower(input.Action)

	switch tool {
	case "argocd":
		return handleArgoAction(ctx, dynClient, action, input.Namespace, input.Name)
	case "fluxcd":
		return handleFluxAction(ctx, dynClient, action, input.Kind, input.Namespace, input.Name)
	default:
		return nil, nil, fmt.Errorf("unknown tool %q: must be argocd or fluxcd", input.Tool)
	}
}

func handleArgoAction(ctx context.Context, dynClient dynamic.Interface, action, namespace, name string) (*mcp.CallToolResult, any, error) {
	switch action {
	case "sync":
		return argoSync(ctx, dynClient, namespace, name)
	case "suspend":
		return argoSetAutoSync(ctx, dynClient, namespace, name, false)
	case "resume":
		return argoSetAutoSync(ctx, dynClient, namespace, name, true)
	default:
		return nil, nil, fmt.Errorf("unknown ArgoCD action %q: must be sync, suspend, or resume", action)
	}
}

func handleFluxAction(ctx context.Context, dynClient dynamic.Interface, action, kind, namespace, name string) (*mcp.CallToolResult, any, error) {
	if kind == "" {
		return nil, nil, fmt.Errorf("kind is required for FluxCD operations (e.g. kustomization, helmrelease, gitrepository)")
	}

	gvr, ok := fluxGVRs[strings.ToLower(kind)]
	if !ok {
		return nil, nil, fmt.Errorf("unknown FluxCD kind %q: supported kinds are kustomization, helmrelease, gitrepository, ocirepository, helmrepository", kind)
	}

	switch action {
	case "reconcile":
		return fluxReconcile(ctx, dynClient, gvr, kind, namespace, name)
	case "suspend":
		return fluxSetSuspend(ctx, dynClient, gvr, kind, namespace, name, true)
	case "resume":
		return fluxSetSuspend(ctx, dynClient, gvr, kind, namespace, name, false)
	default:
		return nil, nil, fmt.Errorf("unknown FluxCD action %q: must be reconcile, suspend, or resume", action)
	}
}

// argoSync triggers a sync operation on an ArgoCD Application.
func argoSync(ctx context.Context, dynClient dynamic.Interface, namespace, name string) (*mcp.CallToolResult, any, error) {
	// Check if there's already a sync in progress
	app, err := dynClient.Resource(argoAppGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, fmt.Errorf("ArgoCD Application %s/%s not found", namespace, name)
		}
		return nil, nil, fmt.Errorf("failed to get Application: %w", err)
	}

	phase, found, _ := unstructured.NestedString(app.Object, "status", "operationState", "phase")
	if found && phase == "Running" {
		return nil, nil, fmt.Errorf("sync operation already in progress for %s/%s", namespace, name)
	}

	timestamp := time.Now().Format(time.RFC3339Nano)
	patch := map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]string{
				"argocd.argoproj.io/refresh": "hard",
			},
		},
		"operation": map[string]any{
			"initiatedBy": map[string]any{
				"username": "radar",
			},
			"sync": map[string]any{
				"revision": "",
				"prune":    true,
			},
		},
	}

	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create sync patch: %w", err)
	}

	_, err = dynClient.Resource(argoAppGVR).Namespace(namespace).Patch(
		ctx, name, types.MergePatchType, patchBytes, metav1.PatchOptions{},
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to sync Application %s/%s: %w", namespace, name, err)
	}

	return toJSONResult(map[string]string{
		"status":      "ok",
		"message":     fmt.Sprintf("Sync initiated for ArgoCD Application %s/%s", namespace, name),
		"requestedAt": timestamp,
	})
}

// argoSetAutoSync enables or disables automated sync on an ArgoCD Application.
func argoSetAutoSync(ctx context.Context, dynClient dynamic.Interface, namespace, name string, enable bool) (*mcp.CallToolResult, any, error) {
	app, err := dynClient.Resource(argoAppGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, fmt.Errorf("ArgoCD Application %s/%s not found", namespace, name)
		}
		return nil, nil, fmt.Errorf("failed to get Application: %w", err)
	}

	var patch map[string]any
	action := "suspend"
	pastAction := "suspended"

	if enable {
		action = "resume"
		pastAction = "resumed"
		prune := true
		selfHeal := true

		annotations, _, _ := unstructured.NestedStringMap(app.Object, "metadata", "annotations")
		if annotations != nil {
			if v, ok := annotations["radar.skyhook.io/suspended-prune"]; ok {
				prune = v == "true"
			}
			if v, ok := annotations["radar.skyhook.io/suspended-selfheal"]; ok {
				selfHeal = v == "true"
			}
		}

		patch = map[string]any{
			"metadata": map[string]any{
				"annotations": map[string]any{
					"radar.skyhook.io/suspended-prune":    nil,
					"radar.skyhook.io/suspended-selfheal": nil,
				},
			},
			"spec": map[string]any{
				"syncPolicy": map[string]any{
					"automated": map[string]any{
						"prune":    prune,
						"selfHeal": selfHeal,
					},
				},
			},
		}
	} else {
		prune := false
		selfHeal := false

		automated, found, _ := unstructured.NestedMap(app.Object, "spec", "syncPolicy", "automated")
		if found && automated != nil {
			if v, ok := automated["prune"].(bool); ok {
				prune = v
			}
			if v, ok := automated["selfHeal"].(bool); ok {
				selfHeal = v
			}
		}

		patch = map[string]any{
			"metadata": map[string]any{
				"annotations": map[string]string{
					"radar.skyhook.io/suspended-prune":    fmt.Sprintf("%v", prune),
					"radar.skyhook.io/suspended-selfheal": fmt.Sprintf("%v", selfHeal),
				},
			},
			"spec": map[string]any{
				"syncPolicy": map[string]any{
					"automated": nil,
				},
			},
		}
	}

	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create patch: %w", err)
	}

	_, err = dynClient.Resource(argoAppGVR).Namespace(namespace).Patch(
		ctx, name, types.MergePatchType, patchBytes, metav1.PatchOptions{},
	)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to %s Application %s/%s: %w", action, namespace, name, err)
	}

	return toJSONResult(map[string]string{
		"status":  "ok",
		"message": fmt.Sprintf("ArgoCD Application %s/%s auto-sync %s", namespace, name, pastAction),
	})
}

// fluxReconcile triggers a reconciliation on a FluxCD resource.
func fluxReconcile(ctx context.Context, dynClient dynamic.Interface, gvr schema.GroupVersionResource, kind, namespace, name string) (*mcp.CallToolResult, any, error) {
	timestamp := time.Now().Format(time.RFC3339Nano)
	patch := map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]string{
				"reconcile.fluxcd.io/requestedAt": timestamp,
			},
		},
	}

	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create reconcile patch: %w", err)
	}

	_, err = dynClient.Resource(gvr).Namespace(namespace).Patch(
		ctx, name, types.MergePatchType, patchBytes, metav1.PatchOptions{},
	)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, fmt.Errorf("FluxCD %s %s/%s not found", kind, namespace, name)
		}
		return nil, nil, fmt.Errorf("failed to reconcile %s %s/%s: %w", kind, namespace, name, err)
	}

	return toJSONResult(map[string]string{
		"status":      "ok",
		"message":     fmt.Sprintf("Reconciliation triggered for FluxCD %s %s/%s", kind, namespace, name),
		"requestedAt": timestamp,
	})
}

// fluxSetSuspend sets the suspend field on a FluxCD resource.
func fluxSetSuspend(ctx context.Context, dynClient dynamic.Interface, gvr schema.GroupVersionResource, kind, namespace, name string, suspend bool) (*mcp.CallToolResult, any, error) {
	patch := map[string]any{
		"spec": map[string]any{
			"suspend": suspend,
		},
	}

	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create patch: %w", err)
	}

	_, err = dynClient.Resource(gvr).Namespace(namespace).Patch(
		ctx, name, types.MergePatchType, patchBytes, metav1.PatchOptions{},
	)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil, fmt.Errorf("FluxCD %s %s/%s not found", kind, namespace, name)
		}
		return nil, nil, fmt.Errorf("failed to update %s %s/%s: %w", kind, namespace, name, err)
	}

	action := "suspended"
	if !suspend {
		action = "resumed"
	}

	return toJSONResult(map[string]string{
		"status":  "ok",
		"message": fmt.Sprintf("FluxCD %s %s/%s %s", kind, namespace, name, action),
	})
}
