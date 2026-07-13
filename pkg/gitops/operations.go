package gitops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

// Annotations written by SetArgoAutoSync when suspending auto-sync so that
// the original prune/selfHeal settings can be restored on resume. Exported
// so consumers can identify or clean these annotations up directly.
//
// The legacy* keys are read on resume to honor Applications suspended by
// older Radar builds; they're cleared on resume and never re-written. Safe
// to drop once all installs have rolled through at least one resume cycle
// past this release.
const (
	ArgoSuspendedPruneAnnotation    = "radarhq.io/suspended-prune"
	ArgoSuspendedSelfHealAnnotation = "radarhq.io/suspended-selfheal"

	legacyArgoSuspendedPruneAnnotation    = "skyhook.io/suspended-prune"
	legacyArgoSuspendedSelfHealAnnotation = "skyhook.io/suspended-selfheal"
)

// Sentinel errors so HTTP handlers can map operation outcomes to status
// codes via errors.Is rather than fragile substring matching.
var (
	// ErrOperationInProgress: a sync/rollback couldn't start because the
	// Application already has an in-flight operation. HTTP 409.
	ErrOperationInProgress = errors.New("operation already in progress")
	// ErrNoOperationInProgress: a terminate couldn't fire because there
	// was no active operation. HTTP 400.
	ErrNoOperationInProgress = errors.New("no operation in progress")
	// ErrInvalidResourceSelection: a requested selective sync contains an
	// incomplete resource identity. HTTP 400.
	ErrInvalidResourceSelection = errors.New("invalid resource selection")
	// ErrHistoryEntryNotFound: the requested rollback target id isn't in
	// status.history. HTTP 404.
	ErrHistoryEntryNotFound = errors.New("history entry not found")
	// ErrResourceTerminating: the target resource has metadata.deletionTimestamp
	// set, so any mutating operation against it is futile — the resource is
	// being torn down and reconcile/sync/rollback will either no-op or be
	// undone immediately. The HTTP layer maps this to 409 Conflict so the
	// frontend can show a tailored "resource is pending deletion" toast
	// instead of bubbling up a generic K8s error message. Read-only verbs
	// (Argo Refresh) and op-cancel (Argo Terminate) are *not* gated by this
	// check — refresh just re-reads from Git, while terminate asks Argo to
	// stop its in-flight operation; both are harmless on a Terminating resource.
	ErrResourceTerminating = errors.New("resource is pending deletion")
)

// assertNotTerminating returns ErrResourceTerminating wrapped with a
// caller-friendly message when the object has metadata.deletionTimestamp
// set. The returned error includes the kind+ns/name and finalizers so
// the user sees, in one line, both *what* is terminating and *what's
// blocking cleanup*. Returns nil for healthy (non-Terminating) objects.
//
// The deletionTimestamp check is the single source of truth K8s uses to
// signal "this resource is being deleted, do not start new work on it".
// API server still serves the object (so GETs succeed), but kubelet/the
// owning controller stop accepting new operations against it.
func assertNotTerminating(obj *unstructured.Unstructured, kind, namespace, name string) error {
	if obj == nil {
		return nil
	}
	dt := obj.GetDeletionTimestamp()
	if dt == nil || dt.IsZero() {
		return nil
	}
	finalizers := obj.GetFinalizers()
	suffix := ""
	if len(finalizers) > 0 {
		suffix = fmt.Sprintf(" (finalizers: %s)", strings.Join(finalizers, ", "))
	}
	return fmt.Errorf("%s %s/%s is being deleted%s: %w", kind, namespace, name, suffix, ErrResourceTerminating)
}

func argoOperationInProgress(app *unstructured.Unstructured) bool {
	operation, found, _ := unstructured.NestedFieldNoCopy(app.Object, "operation")
	if found && operation != nil {
		return true
	}
	phase, _, _ := unstructured.NestedString(app.Object, "status", "operationState", "phase")
	return phase == "Running" || phase == "Terminating"
}

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
	sort.Strings(supported)
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

// ArgoSyncResource identifies a resource selected for an ArgoCD selective sync.
type ArgoSyncResource struct {
	Group     string `json:"group,omitempty"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

// ArgoSyncOptions controls an ArgoCD sync operation. Pointer-bool fields
// let callers say "not set" via nil; DryRun/Force/ApplyOnly are only
// written when non-nil so Argo's server default applies otherwise. Prune
// is the exception — always written, defaulting to true when nil, to
// match Argo's server default.
type ArgoSyncOptions struct {
	Resources   []ArgoSyncResource `json:"resources,omitempty"`
	Revision    string             `json:"revision,omitempty"`
	Prune       *bool              `json:"prune,omitempty"`
	DryRun      *bool              `json:"dryRun,omitempty"`
	Force       *bool              `json:"force,omitempty"`
	ApplyOnly   *bool              `json:"applyOnly,omitempty"`
	SyncOptions []string           `json:"syncOptions,omitempty"`
}

type ArgoResourceValidationResult struct {
	Outcome  string                        `json:"outcome"`
	Message  string                        `json:"message"`
	Resource *ArgoResourceValidationTarget `json:"resource,omitempty"`
}

type ArgoResourceValidationTarget struct {
	Group     string `json:"group,omitempty"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Status    string `json:"status,omitempty"`
	Message   string `json:"message,omitempty"`
}

// ArgoRollbackOptions controls an ArgoCD rollback operation. ID is the
// history entry to roll back to (matches HistoryItem.ID surfaced by the
// insights builder). Argo's rollback uses the same operation slot as sync
// — only one is in flight at a time.
type ArgoRollbackOptions struct {
	ID     int64 `json:"id"`
	Prune  *bool `json:"prune,omitempty"`
	DryRun *bool `json:"dryRun,omitempty"`
}

// --- ArgoCD operations ---

// SyncArgoApp triggers a sync operation on an ArgoCD Application.
func SyncArgoApp(ctx context.Context, dynClient dynamic.Interface, namespace, name string, opts ArgoSyncOptions) (OperationResult, error) {
	return syncArgoApp(ctx, dynClient, namespace, name, opts, "")
}

func syncArgoApp(ctx context.Context, dynClient dynamic.Interface, namespace, name string, opts ArgoSyncOptions, validationID string) (OperationResult, error) {
	for i, resource := range opts.Resources {
		if resource.Kind == "" || resource.Name == "" {
			return OperationResult{}, fmt.Errorf("sync resource %d requires kind and name: %w", i+1, ErrInvalidResourceSelection)
		}
	}

	app, err := dynClient.Resource(argoAppGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return OperationResult{}, fmt.Errorf("ArgoCD Application %s/%s not found: %w", namespace, name, err)
		}
		return OperationResult{}, fmt.Errorf("failed to get Application: %w", err)
	}

	if err := assertNotTerminating(app, "ArgoCD Application", namespace, name); err != nil {
		return OperationResult{}, err
	}

	if argoOperationInProgress(app) {
		return OperationResult{}, fmt.Errorf("sync operation already in progress for %s/%s: %w", namespace, name, ErrOperationInProgress)
	}

	timestamp := time.Now().Format(time.RFC3339Nano)
	prune := true
	if opts.Prune != nil {
		prune = *opts.Prune
	}
	sync := map[string]any{
		"revision": opts.Revision,
		"prune":    prune,
	}
	if opts.DryRun != nil {
		sync["dryRun"] = *opts.DryRun
	}
	// Argo's syncStrategy is a oneof — `apply` skips PreSync/PostSync/SyncFail
	// hooks entirely, while `hook` runs the full hook lifecycle. Force lives
	// inside whichever strategy is in use. Three cases:
	//   ApplyOnly: pick `apply` (hooks skipped, with optional force).
	//   Force only: pick `hook` (default lifecycle, with force) — using
	//     `apply` here would silently drop the user's hooks, which is the
	//     opposite of what most operators want when force-syncing.
	//   Neither: omit syncStrategy and let Argo use its default (hook).
	applyOnly := opts.ApplyOnly != nil && *opts.ApplyOnly
	force := opts.Force != nil && *opts.Force
	switch {
	case applyOnly:
		applyMap := map[string]any{}
		if force {
			applyMap["force"] = true
		}
		sync["syncStrategy"] = map[string]any{"apply": applyMap}
	case force:
		sync["syncStrategy"] = map[string]any{"hook": map[string]any{"force": true}}
	}
	if len(opts.SyncOptions) > 0 {
		// Argo accepts free-form key=value entries here (Replace=true,
		// ServerSideApply=true, PruneLast=true, ApplyOutOfSyncOnly=true,
		// etc). Caller is responsible for spelling them right.
		sync["syncOptions"] = opts.SyncOptions
	}
	if len(opts.Resources) > 0 {
		resources := make([]map[string]any, 0, len(opts.Resources))
		for _, res := range opts.Resources {
			resources = append(resources, map[string]any{
				"group":     res.Group,
				"kind":      res.Kind,
				"namespace": res.Namespace,
				"name":      res.Name,
			})
		}
		sync["resources"] = resources
	}
	operation := map[string]any{
		"initiatedBy": map[string]any{
			"username": "radar",
		},
		"sync": sync,
	}
	if validationID != "" {
		operation["info"] = []any{map[string]any{
			"name":  "radar-validation-id",
			"value": validationID,
		}}
	}
	patch := map[string]any{
		"operation": operation,
		"metadata": map[string]any{
			"resourceVersion": app.GetResourceVersion(),
		},
	}
	dryRun := opts.DryRun != nil && *opts.DryRun
	if !dryRun {
		patch["metadata"] = map[string]any{
			"resourceVersion": app.GetResourceVersion(),
			"annotations": map[string]string{
				"argocd.argoproj.io/refresh": "hard",
			},
		}
	}

	if err := mergePatch(ctx, dynClient, argoAppGVR, namespace, name, patch); err != nil {
		if apierrors.IsConflict(err) {
			return OperationResult{}, fmt.Errorf("Application changed before sync could start for %s/%s: %w", namespace, name, ErrOperationInProgress)
		}
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

func ValidateArgoResource(ctx context.Context, dynClient dynamic.Interface, namespace, name string, resource ArgoSyncResource, opts ArgoSyncOptions) (ArgoResourceValidationResult, error) {
	if resource.Kind == "" || resource.Name == "" {
		return ArgoResourceValidationResult{}, errors.New("validation requires a resource kind and name")
	}
	validationID := uuid.NewString()
	falseValue := false
	trueValue := true
	opts.Resources = []ArgoSyncResource{resource}
	opts.Revision = ""
	opts.Prune = &falseValue
	opts.DryRun = &trueValue
	opts.ApplyOnly = &falseValue
	if _, err := syncArgoApp(ctx, dynClient, namespace, name, opts, validationID); err != nil {
		return ArgoResourceValidationResult{}, err
	}

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		app, err := dynClient.Resource(argoAppGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return ArgoResourceValidationResult{}, fmt.Errorf("ArgoCD Application %s/%s not found while validating: %w", namespace, name, err)
			}
			return ArgoResourceValidationResult{}, fmt.Errorf("failed to read Application validation result: %w", err)
		}
		if err := assertNotTerminating(app, "ArgoCD Application", namespace, name); err != nil {
			return ArgoResourceValidationResult{}, err
		}
		if result, done := argoResourceValidationResult(app, validationID, resource); done {
			return result, nil
		}

		select {
		case <-ctx.Done():
			return ArgoResourceValidationResult{}, fmt.Errorf("timed out waiting for ArgoCD validation result: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func argoResourceValidationResult(app *unstructured.Unstructured, validationID string, target ArgoSyncResource) (ArgoResourceValidationResult, bool) {
	operation, found, _ := unstructured.NestedMap(app.Object, "status", "operationState", "operation")
	if !found || !argoOperationHasInfo(operation, "radar-validation-id", validationID) {
		return ArgoResourceValidationResult{}, false
	}
	phase, _, _ := unstructured.NestedString(app.Object, "status", "operationState", "phase")
	if phase == "" || phase == "Running" {
		return ArgoResourceValidationResult{}, false
	}

	message, _, _ := unstructured.NestedString(app.Object, "status", "operationState", "message")
	resources, _, _ := unstructured.NestedSlice(app.Object, "status", "operationState", "syncResult", "resources")
	for _, item := range resources {
		entry, ok := item.(map[string]any)
		if !ok || !argoResourceResultMatches(entry, target) {
			continue
		}
		resource := &ArgoResourceValidationTarget{
			Group:     stringValue(entry["group"]),
			Kind:      stringValue(entry["kind"]),
			Namespace: stringValue(entry["namespace"]),
			Name:      stringValue(entry["name"]),
			Status:    stringValue(entry["status"]),
			Message:   stringValue(entry["message"]),
		}
		if phase == "Succeeded" {
			return ArgoResourceValidationResult{Outcome: "succeeded", Message: "Argo's dry-run completed without applying changes. API admission can still reject the real sync.", Resource: resource}, true
		}
		if message == "" {
			message = resource.Message
		}
		return ArgoResourceValidationResult{Outcome: "failed", Message: message, Resource: resource}, true
	}
	if phase == "Succeeded" {
		return ArgoResourceValidationResult{Outcome: "inconclusive", Message: "Argo completed the dry-run but did not report a result for the selected resource."}, true
	}
	if message == "" {
		message = fmt.Sprintf("Argo validation finished with phase %s.", phase)
	}
	return ArgoResourceValidationResult{Outcome: "failed", Message: message}, true
}

func argoOperationHasInfo(operation map[string]any, name, value string) bool {
	info, ok := operation["info"].([]any)
	if !ok {
		return false
	}
	for _, item := range info {
		entry, ok := item.(map[string]any)
		if ok && entry["name"] == name && entry["value"] == value {
			return true
		}
	}
	return false
}

func argoResourceResultMatches(entry map[string]any, target ArgoSyncResource) bool {
	if stringValue(entry["group"]) != target.Group ||
		stringValue(entry["kind"]) != target.Kind ||
		stringValue(entry["name"]) != target.Name {
		return false
	}
	return target.Namespace == "" || stringValue(entry["namespace"]) == target.Namespace
}

func stringValue(value any) string {
	result, _ := value.(string)
	return result
}

// SetArgoAutoSync enables or disables automated sync on an ArgoCD Application.
func SetArgoAutoSync(ctx context.Context, dynClient dynamic.Interface, namespace, name string, enable bool) (OperationResult, error) {
	app, err := dynClient.Resource(argoAppGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return OperationResult{}, fmt.Errorf("ArgoCD Application %s/%s not found: %w", namespace, name, err)
		}
		return OperationResult{}, fmt.Errorf("failed to get Application: %w", err)
	}

	if err := assertNotTerminating(app, "ArgoCD Application", namespace, name); err != nil {
		return OperationResult{}, err
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
			return OperationResult{}, fmt.Errorf("ArgoCD Application %s/%s not found: %w", namespace, name, err)
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
			return OperationResult{}, fmt.Errorf("ArgoCD Application %s/%s not found: %w", namespace, name, err)
		}
		return OperationResult{}, fmt.Errorf("failed to get Application: %w", err)
	}

	phase, found, _ := unstructured.NestedString(app.Object, "status", "operationState", "phase")
	operation, operationFound, _ := unstructured.NestedFieldNoCopy(app.Object, "operation")
	if found && phase == "Terminating" && operationFound && operation != nil {
		return OperationResult{
			Message:   "Termination already in progress",
			Operation: "terminate",
			Tool:      "argocd",
			Kind:      "Application",
			Namespace: namespace,
			Name:      name,
		}, nil
	}
	if !found || phase != "Running" {
		return OperationResult{}, fmt.Errorf("no sync operation in progress for %s/%s: %w", namespace, name, ErrNoOperationInProgress)
	}

	timestamp := time.Now().Format(time.RFC3339Nano)
	message := "Termination requested"
	patch := make([]map[string]any, 0, 4)
	patch = append(patch, map[string]any{"op": "test", "path": "/status/operationState/phase", "value": "Running"})
	if operationFound && operation != nil {
		patch = append(patch,
			map[string]any{"op": "test", "path": "/operation", "value": operation},
			map[string]any{"op": "replace", "path": "/status/operationState/phase", "value": "Terminating"},
		)
	} else {
		message = "Stale operation status cleared"
		patch = append(patch,
			map[string]any{"op": "replace", "path": "/status/operationState/phase", "value": "Error"},
			map[string]any{"op": "add", "path": "/status/operationState/message", "value": "The operation was cleared before termination completed."},
			map[string]any{"op": "add", "path": "/status/operationState/finishedAt", "value": timestamp},
		)
	}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return OperationResult{}, fmt.Errorf("failed to marshal terminate patch: %w", err)
	}
	_, err = dynClient.Resource(argoAppGVR).Namespace(namespace).Patch(
		ctx, name, types.JSONPatchType, patchBytes, metav1.PatchOptions{},
	)
	if err != nil {
		if apierrors.IsInvalid(err) {
			return OperationResult{}, fmt.Errorf("no sync operation in progress for %s/%s (completed before terminate could fire): %w", namespace, name, ErrNoOperationInProgress)
		}
		return OperationResult{}, fmt.Errorf("failed to terminate sync for Application %s/%s: %w", namespace, name, err)
	}

	return OperationResult{
		Message:     message,
		Operation:   "terminate",
		Tool:        "argocd",
		Kind:        "Application",
		Namespace:   namespace,
		Name:        name,
		RequestedAt: timestamp,
	}, nil
}

// RollbackArgoApp rolls an ArgoCD Application back to a prior revision
// by ID (matches HistoryItem.ID surfaced by the insights builder). Like
// sync, rollback uses the operation slot — fails if a sync is in flight.
func RollbackArgoApp(ctx context.Context, dynClient dynamic.Interface, namespace, name string, opts ArgoRollbackOptions) (OperationResult, error) {
	if opts.ID <= 0 {
		return OperationResult{}, fmt.Errorf("rollback requires a positive history id")
	}
	app, err := dynClient.Resource(argoAppGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return OperationResult{}, fmt.Errorf("ArgoCD Application %s/%s not found: %w", namespace, name, err)
		}
		return OperationResult{}, fmt.Errorf("failed to get Application: %w", err)
	}

	if err := assertNotTerminating(app, "ArgoCD Application", namespace, name); err != nil {
		return OperationResult{}, err
	}

	if argoOperationInProgress(app) {
		return OperationResult{}, fmt.Errorf("cannot rollback while another operation is in progress for %s/%s: %w", namespace, name, ErrOperationInProgress)
	}

	// Verify the requested history ID actually exists, otherwise Argo silently
	// accepts the operation and never executes — confusing failure mode.
	history, _, _ := unstructured.NestedSlice(app.Object, "status", "history")
	matched := false
	for _, item := range history {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		switch v := m["id"].(type) {
		case int64:
			if v == opts.ID {
				matched = true
			}
		case float64:
			if int64(v) == opts.ID {
				matched = true
			}
		}
	}
	if !matched {
		return OperationResult{}, fmt.Errorf("history entry id=%d not found on Application %s/%s: %w", opts.ID, namespace, name, ErrHistoryEntryNotFound)
	}

	timestamp := time.Now().Format(time.RFC3339Nano)
	rollback := map[string]any{
		"id": opts.ID,
	}
	if opts.Prune != nil {
		rollback["prune"] = *opts.Prune
	}
	if opts.DryRun != nil {
		rollback["dryRun"] = *opts.DryRun
	}
	patch := map[string]any{
		"metadata": map[string]any{"resourceVersion": app.GetResourceVersion()},
		"operation": map[string]any{
			"initiatedBy": map[string]any{"username": "radar"},
			"rollback":    rollback,
		},
	}

	if err := mergePatch(ctx, dynClient, argoAppGVR, namespace, name, patch); err != nil {
		if apierrors.IsConflict(err) {
			return OperationResult{}, fmt.Errorf("Application changed before rollback could start for %s/%s: %w", namespace, name, ErrOperationInProgress)
		}
		return OperationResult{}, fmt.Errorf("failed to rollback Application %s/%s: %w", namespace, name, err)
	}

	return OperationResult{
		Message:     fmt.Sprintf("Rollback to history #%d initiated for ArgoCD Application %s/%s", opts.ID, namespace, name),
		Operation:   "rollback",
		Tool:        "argocd",
		Kind:        "Application",
		Namespace:   namespace,
		Name:        name,
		RequestedAt: timestamp,
	}, nil
}

// --- FluxCD operations ---

// ReconcileFlux triggers a reconciliation on a FluxCD resource.
func ReconcileFlux(ctx context.Context, dynClient dynamic.Interface, entry FluxKindEntry, namespace, name string) (OperationResult, error) {
	// Pre-flight GET so we can return ErrResourceTerminating cleanly.
	// Without this we patch a Terminating resource — the patch may even
	// "succeed" (annotation lands), but the controller will skip the
	// reconcile because it's processing finalizers, and the user gets a
	// false-positive "Reconciliation triggered" toast. Costs one extra
	// round-trip on the happy path; trade is correctness.
	obj, err := dynClient.Resource(entry.GVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return OperationResult{}, fmt.Errorf("FluxCD %s %s/%s not found: %w", entry.Kind, namespace, name, err)
		}
		return OperationResult{}, fmt.Errorf("failed to get %s %s/%s: %w", entry.Kind, namespace, name, err)
	}
	if err := assertNotTerminating(obj, "FluxCD "+entry.Kind, namespace, name); err != nil {
		return OperationResult{}, err
	}

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
			return OperationResult{}, fmt.Errorf("FluxCD %s %s/%s not found: %w", entry.Kind, namespace, name, err)
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
	obj, err := dynClient.Resource(entry.GVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return OperationResult{}, fmt.Errorf("FluxCD %s %s/%s not found: %w", entry.Kind, namespace, name, err)
		}
		return OperationResult{}, fmt.Errorf("failed to get %s %s/%s: %w", entry.Kind, namespace, name, err)
	}
	if err := assertNotTerminating(obj, "FluxCD "+entry.Kind, namespace, name); err != nil {
		return OperationResult{}, err
	}

	patch := map[string]any{
		"spec": map[string]any{
			"suspend": suspend,
		},
	}

	if err := mergePatch(ctx, dynClient, entry.GVR, namespace, name, patch); err != nil {
		if apierrors.IsNotFound(err) {
			return OperationResult{}, fmt.Errorf("FluxCD %s %s/%s not found: %w", entry.Kind, namespace, name, err)
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
			return OperationResult{}, fmt.Errorf("FluxCD %s %s/%s not found: %w", entry.Kind, namespace, name, err)
		}
		return OperationResult{}, fmt.Errorf("failed to get %s %s/%s: %w", entry.Kind, namespace, name, err)
	}
	if err := assertNotTerminating(resource, "FluxCD "+entry.Kind, namespace, name); err != nil {
		return OperationResult{}, err
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

	// Preflight the source for Terminating: a finalizer-stuck source receives
	// the reconcile annotation but its controller will never act on it. Without
	// this check the operator would see a green "Sync triggered" toast and
	// believe progress is being made when the source is in fact a zombie.
	sourceObj, err := dynClient.Resource(sourceEntry.GVR).Namespace(sourceNamespace).Get(ctx, sourceName, metav1.GetOptions{})
	if err == nil {
		if err := assertNotTerminating(sourceObj, "FluxCD "+sourceEntry.Kind, sourceNamespace, sourceName); err != nil {
			return OperationResult{}, err
		}
	}
	// (If the source can't be fetched here, fall through to the patch — the
	// patch will surface the same error with a clearer "could not be patched"
	// message below.)

	// Source patch: K8s returns 404 for either a missing source resource
	// (typo'd sourceRef.name, source already deleted) OR a missing source
	// namespace (sourceRef points to a namespace that no longer exists, common
	// when the source is a finalizer-stuck zombie). The error message is
	// intentionally neutral about cause — leading the operator to one of
	// the two possibilities by guessing wastes their time when it was the
	// other case. The wrapped err preserves the apierrors chain so handlers
	// still map this to 404.
	if err := mergePatch(ctx, dynClient, sourceEntry.GVR, sourceNamespace, sourceName, reconcilePatch); err != nil {
		if apierrors.IsNotFound(err) {
			return OperationResult{}, fmt.Errorf(
				"source %s %s/%s could not be patched (verify the resource and its namespace still exist; if the namespace was deleted, this resource may be a finalizer-stuck zombie): %w",
				sourceEntry.Kind, sourceNamespace, sourceName, err,
			)
		}
		return OperationResult{}, fmt.Errorf("failed to reconcile source %s %s/%s: %w", sourceEntry.Kind, sourceNamespace, sourceName, err)
	}

	// Then, reconcile the resource itself
	if err := mergePatch(ctx, dynClient, entry.GVR, namespace, name, reconcilePatch); err != nil {
		return OperationResult{}, fmt.Errorf("failed to reconcile %s %s/%s (note: source %s/%s was reconciled): %w",
			entry.Kind, namespace, name, sourceNamespace, sourceName, err)
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
