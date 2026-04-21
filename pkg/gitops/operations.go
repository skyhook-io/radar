package gitops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

// Annotation keys written by SetArgoAutoSync when suspending auto-sync, to remember
// the original prune/selfHeal settings so they can be restored on resume.
// Exported so consumers can identify or clean up these annotations independently.
//
// Legacy* constants honor Applications suspended by older Radar builds so
// resuming still restores their prune/selfHeal settings. Legacy keys are
// cleared on resume and never re-written.
//
// TODO(2026-Q3): remove legacy constants once all installs have rolled
// through at least one resume cycle past this release (paired with the
// node-debug legacy-label cleanup in pkg/k8score/node_debug.go).
const (
	ArgoSuspendedPruneAnnotation    = "radarhq.io/suspended-prune"
	ArgoSuspendedSelfHealAnnotation = "radarhq.io/suspended-selfheal"

	legacyArgoSuspendedPruneAnnotation    = "skyhook.io/suspended-prune"
	legacyArgoSuspendedSelfHealAnnotation = "skyhook.io/suspended-selfheal"
)

// argoAppGVR is the GVR for ArgoCD Application resources.
var argoAppGVR = schema.GroupVersionResource{
	Group:    "argoproj.io",
	Version:  "v1alpha1",
	Resource: "applications",
}

// FluxKindEntry maps a lowercase kind to its GVR and canonical Kind name.
type FluxKindEntry struct {
	GVR  schema.GroupVersionResource
	Kind string // e.g. "GitRepository"
}

// fluxKinds is the authoritative map of supported FluxCD resource kinds.
var fluxKinds = map[string]FluxKindEntry{
	"gitrepository":  {GVR: schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"}, Kind: "GitRepository"},
	"ocirepository":  {GVR: schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "ocirepositories"}, Kind: "OCIRepository"},
	"helmrepository": {GVR: schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "helmrepositories"}, Kind: "HelmRepository"},
	"kustomization":  {GVR: schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}, Kind: "Kustomization"},
	"helmrelease":    {GVR: schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}, Kind: "HelmRelease"},
	"alert":          {GVR: schema.GroupVersionResource{Group: "notification.toolkit.fluxcd.io", Version: "v1beta3", Resource: "alerts"}, Kind: "Alert"},
}

// ResolveFluxKind resolves any form (singular, plural, any case) to FluxKindEntry.
func ResolveFluxKind(kind string) (FluxKindEntry, error) {
	k := strings.ToLower(kind)

	// Direct match on singular key
	if entry, ok := fluxKinds[k]; ok {
		return entry, nil
	}

	// Match on plural resource name or canonical Kind (case-insensitive)
	for _, entry := range fluxKinds {
		if strings.ToLower(entry.GVR.Resource) == k || strings.ToLower(entry.Kind) == k {
			return entry, nil
		}
	}

	supported := make([]string, 0, len(fluxKinds))
	for k := range fluxKinds {
		supported = append(supported, k)
	}
	return FluxKindEntry{}, fmt.Errorf("unknown FluxCD kind %q: supported kinds are %s", kind, strings.Join(supported, ", "))
}

// OperationResult is a transport-neutral result from a GitOps operation.
type OperationResult struct {
	Message     string
	Operation   string // sync, reconcile, suspend, resume, refresh, terminate
	Tool        string // argocd, fluxcd
	Kind        string // Application, Kustomization, etc.
	Namespace   string
	Name        string
	RequestedAt string     // RFC3339Nano, empty if not timed
	Source      *SourceRef // only for sync-with-source
}

// SourceRef identifies the source resource in sync-with-source operations.
type SourceRef struct {
	Kind      string
	Namespace string
	Name      string
}

// --- ArgoCD operations ---

// SyncArgoApp triggers a sync operation on an ArgoCD Application.
func SyncArgoApp(ctx context.Context, dynClient dynamic.Interface, namespace, name string) (OperationResult, error) {
	app, err := dynClient.Resource(argoAppGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return OperationResult{}, fmt.Errorf("ArgoCD Application %s/%s not found", namespace, name)
		}
		return OperationResult{}, fmt.Errorf("failed to get Application: %w", err)
	}

	phase, found, _ := unstructured.NestedString(app.Object, "status", "operationState", "phase")
	if found && phase == "Running" {
		return OperationResult{}, fmt.Errorf("sync operation already in progress for %s/%s", namespace, name)
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

	if err := mergePatch(ctx, dynClient, argoAppGVR, namespace, name, patch); err != nil {
		return OperationResult{}, fmt.Errorf("failed to sync Application %s/%s: %w", namespace, name, err)
	}

	return OperationResult{
		Message:     fmt.Sprintf("Sync initiated for ArgoCD Application %s/%s", namespace, name),
		Operation:   "sync",
		Tool:        "argocd",
		Kind:        "Application",
		Namespace:   namespace,
		Name:        name,
		RequestedAt: timestamp,
	}, nil
}

// SetArgoAutoSync enables or disables automated sync on an ArgoCD Application.
func SetArgoAutoSync(ctx context.Context, dynClient dynamic.Interface, namespace, name string, enable bool) (OperationResult, error) {
	app, err := dynClient.Resource(argoAppGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return OperationResult{}, fmt.Errorf("ArgoCD Application %s/%s not found", namespace, name)
		}
		return OperationResult{}, fmt.Errorf("failed to get Application: %w", err)
	}

	var patch map[string]any
	operation := "suspend"
	pastAction := "suspended"

	if enable {
		operation = "resume"
		pastAction = "resumed"
		prune := true
		selfHeal := true

		annotations, _, _ := unstructured.NestedStringMap(app.Object, "metadata", "annotations")
		if annotations != nil {
			// Prefer the current key; fall back to the legacy key for
			// Applications suspended by older Radar builds.
			if v, ok := annotations[ArgoSuspendedPruneAnnotation]; ok {
				prune = v == "true"
			} else if v, ok := annotations[legacyArgoSuspendedPruneAnnotation]; ok {
				prune = v == "true"
			}
			if v, ok := annotations[ArgoSuspendedSelfHealAnnotation]; ok {
				selfHeal = v == "true"
			} else if v, ok := annotations[legacyArgoSuspendedSelfHealAnnotation]; ok {
				selfHeal = v == "true"
			}
		}

		patch = map[string]any{
			"metadata": map[string]any{
				"annotations": map[string]any{
					ArgoSuspendedPruneAnnotation:          nil,
					ArgoSuspendedSelfHealAnnotation:       nil,
					legacyArgoSuspendedPruneAnnotation:    nil,
					legacyArgoSuspendedSelfHealAnnotation: nil,
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
					ArgoSuspendedPruneAnnotation:    fmt.Sprintf("%v", prune),
					ArgoSuspendedSelfHealAnnotation: fmt.Sprintf("%v", selfHeal),
				},
			},
			"spec": map[string]any{
				"syncPolicy": map[string]any{
					"automated": nil,
				},
			},
		}
	}

	if err := mergePatch(ctx, dynClient, argoAppGVR, namespace, name, patch); err != nil {
		return OperationResult{}, fmt.Errorf("failed to %s Application %s/%s: %w", operation, namespace, name, err)
	}

	return OperationResult{
		Message:   fmt.Sprintf("ArgoCD Application %s/%s auto-sync %s", namespace, name, pastAction),
		Operation: operation,
		Tool:      "argocd",
		Kind:      "Application",
		Namespace: namespace,
		Name:      name,
	}, nil
}

// RefreshArgoApp triggers a refresh on an ArgoCD Application.
func RefreshArgoApp(ctx context.Context, dynClient dynamic.Interface, namespace, name, refreshType string) (OperationResult, error) {
	timestamp := time.Now().Format(time.RFC3339Nano)
	patch := map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]string{
				"argocd.argoproj.io/refresh": refreshType,
			},
		},
	}

	if err := mergePatch(ctx, dynClient, argoAppGVR, namespace, name, patch); err != nil {
		if apierrors.IsNotFound(err) {
			return OperationResult{}, fmt.Errorf("ArgoCD Application %s/%s not found", namespace, name)
		}
		return OperationResult{}, fmt.Errorf("failed to refresh Application %s/%s: %w", namespace, name, err)
	}

	return OperationResult{
		Message:     fmt.Sprintf("Refresh (%s) triggered for ArgoCD Application %s/%s", refreshType, namespace, name),
		Operation:   "refresh",
		Tool:        "argocd",
		Kind:        "Application",
		Namespace:   namespace,
		Name:        name,
		RequestedAt: timestamp,
	}, nil
}

// TerminateArgoSync terminates an ongoing sync operation on an ArgoCD Application.
func TerminateArgoSync(ctx context.Context, dynClient dynamic.Interface, namespace, name string) (OperationResult, error) {
	app, err := dynClient.Resource(argoAppGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return OperationResult{}, fmt.Errorf("ArgoCD Application %s/%s not found", namespace, name)
		}
		return OperationResult{}, fmt.Errorf("failed to get Application: %w", err)
	}

	phase, found, _ := unstructured.NestedString(app.Object, "status", "operationState", "phase")
	if !found || phase != "Running" {
		return OperationResult{}, fmt.Errorf("no sync operation in progress for %s/%s", namespace, name)
	}

	patchBytes := []byte(`[{"op": "remove", "path": "/operation"}]`)
	_, err = dynClient.Resource(argoAppGVR).Namespace(namespace).Patch(
		ctx, name, types.JSONPatchType, patchBytes, metav1.PatchOptions{},
	)
	if err != nil {
		if strings.Contains(err.Error(), "nonexistent") {
			return OperationResult{
				Message:   fmt.Sprintf("No operation to terminate for ArgoCD Application %s/%s (may have already completed)", namespace, name),
				Operation: "terminate",
				Tool:      "argocd",
				Kind:      "Application",
				Namespace: namespace,
				Name:      name,
			}, nil
		}
		return OperationResult{}, fmt.Errorf("failed to terminate sync for Application %s/%s: %w", namespace, name, err)
	}

	return OperationResult{
		Message:   fmt.Sprintf("Sync operation terminated for ArgoCD Application %s/%s", namespace, name),
		Operation: "terminate",
		Tool:      "argocd",
		Kind:      "Application",
		Namespace: namespace,
		Name:      name,
	}, nil
}

// --- FluxCD operations ---

// ReconcileFlux triggers a reconciliation on a FluxCD resource.
func ReconcileFlux(ctx context.Context, dynClient dynamic.Interface, entry FluxKindEntry, namespace, name string) (OperationResult, error) {
	timestamp := time.Now().Format(time.RFC3339Nano)
	patch := map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]string{
				"reconcile.fluxcd.io/requestedAt": timestamp,
			},
		},
	}

	if err := mergePatch(ctx, dynClient, entry.GVR, namespace, name, patch); err != nil {
		if apierrors.IsNotFound(err) {
			return OperationResult{}, fmt.Errorf("FluxCD %s %s/%s not found", entry.Kind, namespace, name)
		}
		return OperationResult{}, fmt.Errorf("failed to reconcile %s %s/%s: %w", entry.Kind, namespace, name, err)
	}

	return OperationResult{
		Message:     fmt.Sprintf("Reconciliation triggered for FluxCD %s %s/%s", entry.Kind, namespace, name),
		Operation:   "reconcile",
		Tool:        "fluxcd",
		Kind:        entry.Kind,
		Namespace:   namespace,
		Name:        name,
		RequestedAt: timestamp,
	}, nil
}

// SetFluxSuspend sets the suspend field on a FluxCD resource.
func SetFluxSuspend(ctx context.Context, dynClient dynamic.Interface, entry FluxKindEntry, namespace, name string, suspend bool) (OperationResult, error) {
	patch := map[string]any{
		"spec": map[string]any{
			"suspend": suspend,
		},
	}

	if err := mergePatch(ctx, dynClient, entry.GVR, namespace, name, patch); err != nil {
		if apierrors.IsNotFound(err) {
			return OperationResult{}, fmt.Errorf("FluxCD %s %s/%s not found", entry.Kind, namespace, name)
		}
		return OperationResult{}, fmt.Errorf("failed to update %s %s/%s: %w", entry.Kind, namespace, name, err)
	}

	operation := "suspend"
	action := "suspended"
	if !suspend {
		operation = "resume"
		action = "resumed"
	}

	return OperationResult{
		Message:   fmt.Sprintf("FluxCD %s %s/%s %s", entry.Kind, namespace, name, action),
		Operation: operation,
		Tool:      "fluxcd",
		Kind:      entry.Kind,
		Namespace: namespace,
		Name:      name,
	}, nil
}

// SyncFluxWithSource reconciles the source first, then the resource itself.
func SyncFluxWithSource(ctx context.Context, dynClient dynamic.Interface, kind, namespace, name string) (OperationResult, error) {
	entry, err := ResolveFluxKind(kind)
	if err != nil {
		return OperationResult{}, err
	}

	// Get the resource to extract sourceRef
	resource, err := dynClient.Resource(entry.GVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return OperationResult{}, fmt.Errorf("FluxCD %s %s/%s not found", entry.Kind, namespace, name)
		}
		return OperationResult{}, fmt.Errorf("failed to get %s %s/%s: %w", entry.Kind, namespace, name, err)
	}

	// Extract sourceRef based on kind
	var sourceKind, sourceName, sourceNamespace string
	spec, ok := resource.Object["spec"].(map[string]any)
	if !ok {
		return OperationResult{}, fmt.Errorf("invalid resource spec for %s %s/%s", entry.Kind, namespace, name)
	}

	switch entry.Kind {
	case "Kustomization":
		if sourceRef, ok := spec["sourceRef"].(map[string]any); ok {
			sourceKind, _ = sourceRef["kind"].(string)
			sourceName, _ = sourceRef["name"].(string)
			sourceNamespace, _ = sourceRef["namespace"].(string)
		}
	case "HelmRelease":
		if chart, ok := spec["chart"].(map[string]any); ok {
			if chartSpec, ok := chart["spec"].(map[string]any); ok {
				if sourceRef, ok := chartSpec["sourceRef"].(map[string]any); ok {
					sourceKind, _ = sourceRef["kind"].(string)
					sourceName, _ = sourceRef["name"].(string)
					sourceNamespace, _ = sourceRef["namespace"].(string)
				}
			}
		}
	default:
		return OperationResult{}, fmt.Errorf("sync-with-source only supported for Kustomization and HelmRelease")
	}

	if sourceName == "" {
		return OperationResult{}, fmt.Errorf("no source reference found in %s %s/%s", entry.Kind, namespace, name)
	}

	if sourceNamespace == "" {
		sourceNamespace = namespace
	}

	sourceEntry, err := ResolveFluxKind(sourceKind)
	if err != nil {
		return OperationResult{}, fmt.Errorf("unknown source kind: %s", sourceKind)
	}

	timestamp := time.Now().Format(time.RFC3339Nano)
	reconcilePatch := map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]string{
				"reconcile.fluxcd.io/requestedAt": timestamp,
			},
		},
	}

	// First, reconcile the source
	if err := mergePatch(ctx, dynClient, sourceEntry.GVR, sourceNamespace, sourceName, reconcilePatch); err != nil {
		return OperationResult{}, fmt.Errorf("failed to reconcile source %s %s/%s: %w", sourceEntry.Kind, sourceNamespace, sourceName, err)
	}

	// Then, reconcile the resource itself
	if err := mergePatch(ctx, dynClient, entry.GVR, namespace, name, reconcilePatch); err != nil {
		return OperationResult{}, fmt.Errorf("failed to reconcile %s %s/%s (note: source %s/%s was reconciled): %w",
			entry.Kind, namespace, name, sourceName, sourceNamespace, err)
	}

	return OperationResult{
		Message:     fmt.Sprintf("Sync with source triggered for FluxCD %s %s/%s", entry.Kind, namespace, name),
		Operation:   "reconcile",
		Tool:        "fluxcd",
		Kind:        entry.Kind,
		Namespace:   namespace,
		Name:        name,
		RequestedAt: timestamp,
		Source:      &SourceRef{Kind: sourceEntry.Kind, Namespace: sourceNamespace, Name: sourceName},
	}, nil
}

// mergePatch is a helper that applies a merge patch to a resource.
func mergePatch(ctx context.Context, dynClient dynamic.Interface, gvr schema.GroupVersionResource, namespace, name string, patch map[string]any) error {
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("failed to marshal patch: %w", err)
	}
	_, err = dynClient.Resource(gvr).Namespace(namespace).Patch(
		ctx, name, types.MergePatchType, patchBytes, metav1.PatchOptions{},
	)
	return err
}
