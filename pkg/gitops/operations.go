package gitops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

// Annotations written by SetArgoAutoSync when suspending auto-sync so that
// the original settings can be restored on resume. Exported so consumers can
// identify or clean these annotations up directly.
//
// ArgoSuspendedAutomatedAnnotation holds the whole spec.syncPolicy.automated
// block as JSON, so resume restores every setting (allowEmpty included). The
// prune/selfHeal keys are still written beside it because older Radar builds
// resume from them alone, defaulting both to true when they are missing.
//
// The legacy* keys are read on resume to honor Applications suspended by
// older Radar builds; they're cleared on resume and never re-written. Safe
// to drop once all installs have rolled through at least one resume cycle
// past this release.
const (
	ArgoSuspendedAutomatedAnnotation = "radarhq.io/suspended-automated"
	ArgoSuspendedPruneAnnotation     = "radarhq.io/suspended-prune"
	ArgoSuspendedSelfHealAnnotation  = "radarhq.io/suspended-selfheal"

	legacyArgoSuspendedPruneAnnotation    = "skyhook.io/suspended-prune"
	legacyArgoSuspendedSelfHealAnnotation = "skyhook.io/suspended-selfheal"
)

// Every operation Radar starts carries ArgoInitiatedViaInfo in operation.info
// (stored in the Application JSON/YAML), next to initiatedBy.username. The
// validation dry-run adds argoValidationInfoName to find its own result.
const (
	ArgoInitiatedViaInfo  = "Initiated via"
	ArgoInitiatedViaRadar = "Radar"

	argoRollbackInfoName   = "radarhq.io/rollback"
	argoValidationInfoName = "radar-validation-id"
	argoDefaultInitiator   = "radar"
)

// argoOperationAttempts bounds how often Radar rereads an Application that
// changed between its read and its conditional write.
const argoOperationAttempts = 3

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
	// ErrInvalidRevision: a scalar revision cannot select per-source revisions
	// for a multi-source Application. HTTP 400.
	ErrInvalidRevision = errors.New("invalid revision")
	// ErrHistoryEntryNotFound: the requested rollback target id isn't in
	// status.history. HTTP 404.
	ErrHistoryEntryNotFound = errors.New("history entry not found")
	// ErrHistoryEntryNotRollbackable: the history entry records no source to
	// sync back to, so Argo CD itself refuses to roll back to it. HTTP 400.
	ErrHistoryEntryNotRollbackable = errors.New("history entry records no source")
	// ErrAutoSyncEnabled: a rollback was refused because the Application's
	// automated sync is on. Argo CD refuses the same request: the controller
	// would sync straight back to the target revision. HTTP 409.
	ErrAutoSyncEnabled = errors.New("auto-sync is enabled")
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
	// ErrResourceSuspended: a Flux object in a sync-with-source chain has
	// spec.suspend set, so its controller ignores reconcile requests. HTTP 409.
	ErrResourceSuspended = errors.New("resource is suspended")
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

// ArgoAutoSyncEnabled reports whether an Argo CD Application's automated sync
// is on. Argo CD 3.x can keep spec.syncPolicy.automated (with its prune and
// selfHeal settings) while switching automation off via `enabled: false`, so
// the block being present is not enough: only an absent or true `enabled`
// counts, matching Argo's SyncPolicy.IsAutomatedSyncEnabled.
func ArgoAutoSyncEnabled(app *unstructured.Unstructured) bool {
	automated, found, _ := unstructured.NestedMap(app.Object, "spec", "syncPolicy", "automated")
	if !found {
		return false
	}
	enabled, ok := automated["enabled"].(bool)
	return !ok || enabled
}

// ArgoHistoryEntryRollbackable reports whether a status.history entry can be
// rolled back to: it needs an id and the source(s) it was deployed from,
// which the rollback syncs back to. Argo CD refuses entries without a source.
func ArgoHistoryEntryRollbackable(entry map[string]any) bool {
	if _, ok := argoHistoryEntryID(entry); !ok {
		return false
	}
	sources, _ := entry["sources"].([]any)
	return len(sources) > 0 || !jsonValueIsZero(entry["source"])
}

func argoHistoryEntryID(entry map[string]any) (int64, bool) {
	switch v := entry["id"].(type) {
	case int64:
		return v, true
	case float64:
		return int64(v), true
	}
	return 0, false
}

// jsonValueIsZero reports whether a decoded JSON value carries no data. Argo
// writes a multi-source history entry's unused `source` as {"repoURL": ""},
// so an empty map is not the only zero shape.
func jsonValueIsZero(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case bool:
		return !t
	case int64:
		return t == 0
	case float64:
		return t == 0
	case map[string]any:
		for _, field := range t {
			if !jsonValueIsZero(field) {
				return false
			}
		}
		return true
	case []any:
		return len(t) == 0
	}
	return false
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
	"helmchart":      {GVR: schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "helmcharts"}, Kind: "HelmChart"},
	"bucket":         {GVR: schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "buckets"}, Kind: "Bucket"},
	"kustomization":  {GVR: schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}, Kind: "Kustomization"},
	"helmrelease":    {GVR: schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}, Kind: "HelmRelease"},
	"alert":          {GVR: schema.GroupVersionResource{Group: "notification.toolkit.fluxcd.io", Version: "v1beta3", Resource: "alerts"}, Kind: "Alert"},
}

// FluxGVRResolver returns the version of a Flux kind the cluster serves. Flux
// promotes its APIs over releases (Bucket reached v1 in Flux 2.4, OCIRepository in
// 2.6), so the static table names only the newest version.
type FluxGVRResolver func(group, kind string) (schema.GroupVersionResource, bool)

// Served returns the entry with the version the cluster serves; a nil resolver
// or an unknown kind keeps the static version.
func (e FluxKindEntry) Served(resolve FluxGVRResolver) FluxKindEntry {
	if resolve == nil {
		return e
	}
	if gvr, ok := resolve(e.GVR.Group, e.Kind); ok && gvr.Resource == e.GVR.Resource {
		e.GVR = gvr
	}
	return e
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
	NoChange    bool       // request was already in the desired in-progress state
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
// written when non-nil. Prune is always written and is false unless set, as
// in Argo CD's UI and CLI.
//
// SyncOptions follows argocd-server: nil uses the Application's own
// spec.syncPolicy.syncOptions, while a non-nil list, even an empty one,
// replaces them for this sync. A JSON body that omits syncOptions (or sends
// null) decodes to nil; one that sends [] decodes to an empty list.
type ArgoSyncOptions struct {
	Resources   []ArgoSyncResource `json:"resources,omitempty"`
	Revision    string             `json:"revision,omitempty"`
	Prune       *bool              `json:"prune,omitempty"`
	DryRun      *bool              `json:"dryRun,omitempty"`
	Force       *bool              `json:"force,omitempty"`
	ApplyOnly   *bool              `json:"applyOnly,omitempty"`
	SyncOptions []string           `json:"syncOptions"`
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
// insights builder); Argo numbers history from 0, so 0 is a valid target.
// Prune and DryRun default to false, as in Argo CD's own rollback.
type ArgoRollbackOptions struct {
	ID     int64 `json:"id"`
	Prune  *bool `json:"prune,omitempty"`
	DryRun *bool `json:"dryRun,omitempty"`
}

// --- ArgoCD operations ---

// argoInitiator uses the Cloud email group for display when present, otherwise
// the authenticated username or "radar" without per-user auth. It is attribution
// only: anyone who can patch the Application can write any name there, and
// the Kubernetes audit log records the identity that made the write.
func argoInitiator(ctx context.Context) string {
	if user := pkgauth.UserFromContext(ctx); user != nil {
		for _, group := range user.Groups {
			if email, ok := strings.CutPrefix(group, "radar:email:"); ok && email != "" {
				return email
			}
		}
		if user.Username != "" {
			return user.Username
		}
	}
	return argoDefaultInitiator
}

func argoOperationInfo(validationID string) []any {
	info := []any{map[string]any{"name": ArgoInitiatedViaInfo, "value": ArgoInitiatedViaRadar}}
	if validationID != "" {
		info = append(info, map[string]any{"name": argoValidationInfoName, "value": validationID})
	}
	return info
}

func getArgoApp(ctx context.Context, dynClient dynamic.Interface, namespace, name string) (*unstructured.Unstructured, error) {
	app, err := dynClient.Resource(argoAppGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("ArgoCD Application %s/%s not found: %w", namespace, name, err)
		}
		return nil, fmt.Errorf("failed to get Application: %w", err)
	}
	return app, nil
}

// updateArgoApp reads the Application, lets build validate it and return a
// merge patch (nil writes nothing), and writes the patch conditional on that
// read's resourceVersion. A conflict means the Application changed in
// between, so the next attempt reads and validates it again: nothing built
// from a stale read is ever written. Errors from the read and from build are
// returned as they are; a failed write is wrapped with verb, and a conflict
// on the last attempt maps to ErrOperationInProgress.
func updateArgoApp(ctx context.Context, dynClient dynamic.Interface, namespace, name, verb string, build func(app *unstructured.Unstructured) (map[string]any, error)) error {
	for attempt := 1; ; attempt++ {
		app, err := getArgoApp(ctx, dynClient, namespace, name)
		if err != nil {
			return err
		}
		patch, err := build(app)
		if err != nil || patch == nil {
			return err
		}
		metadata, _ := patch["metadata"].(map[string]any)
		if metadata == nil {
			metadata = map[string]any{}
			patch["metadata"] = metadata
		}
		metadata["resourceVersion"] = app.GetResourceVersion()
		err = mergePatch(ctx, dynClient, argoAppGVR, namespace, name, patch)
		switch {
		case err == nil:
			return nil
		case !apierrors.IsConflict(err):
			return fmt.Errorf("failed to %s Application %s/%s: %w", verb, namespace, name, err)
		case attempt == argoOperationAttempts:
			return fmt.Errorf("the Application %s/%s kept changing before %s could start; try again: %w", namespace, name, verb, ErrOperationInProgress)
		}
	}
}

// argoOperationPatch installs operation the way Argo CD's SetAppOperation
// does: in the same write, it drops the completed status.operationState. The
// controller merge-patches a new operation's state over the old one, so
// fields the new state omits (initiatedBy.automated, retryCount, syncResult)
// would survive into it. And its sync-window check reads the old state, so a
// leftover automated one would hold a manual sync behind a window that allows
// manual syncs. Callers refuse an Application whose operation is still in
// progress before building this.
func argoOperationPatch(app *unstructured.Unstructured, operation map[string]any) map[string]any {
	patch := map[string]any{"operation": operation}
	if _, found, _ := unstructured.NestedFieldNoCopy(app.Object, "status", "operationState"); found {
		patch["status"] = map[string]any{"operationState": nil}
	}
	return patch
}

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

	timestamp := time.Now().Format(time.RFC3339Nano)
	dryRun := opts.DryRun != nil && *opts.DryRun
	err := updateArgoApp(ctx, dynClient, namespace, name, "sync", func(app *unstructured.Unstructured) (map[string]any, error) {
		if err := assertNotTerminating(app, "ArgoCD Application", namespace, name); err != nil {
			return nil, err
		}
		if argoOperationInProgress(app) {
			return nil, fmt.Errorf("sync operation already in progress for %s/%s: %w", namespace, name, ErrOperationInProgress)
		}
		if sources, _, _ := unstructured.NestedSlice(app.Object, "spec", "sources"); len(sources) > 0 && opts.Revision != "" {
			return nil, fmt.Errorf("cannot override revision for multi-source Application %s/%s; omit revision to use each source's targetRevision: %w", namespace, name, ErrInvalidRevision)
		}
		patch := argoOperationPatch(app, argoSyncOperation(ctx, app, opts, validationID))
		if !dryRun {
			patch["metadata"] = map[string]any{
				"annotations": map[string]any{"argocd.argoproj.io/refresh": "hard"},
			}
		}
		return patch, nil
	})
	if err != nil {
		return OperationResult{}, err
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

// argoSyncOperation builds the operation argocd-server's Sync writes, from
// the Application as read now.
//
// A sync can wait in the queue, for instance behind a sync window. Like
// argocd-server, Radar fixes the source(s) when the sync is requested, so an
// edit to spec.source(s) after that doesn't change what the waiting sync
// deploys (except on an Argo CD 2.11 controller, which takes a multi-source
// Application's sources from its spec). Unlike argocd-server, which asks its
// repo-server to resolve the revision to a commit or chart version at request
// time, Radar has no repo-server to ask: the revision (the caller's for a
// single-source Application, or each source's targetRevision when omitted)
// is resolved by the controller when the sync runs.
//
// The Application's spec.syncPolicy.retry is copied too, except for Radar's
// own validation dry-run, whose result would otherwise wait out the backoff.
func argoSyncOperation(ctx context.Context, app *unstructured.Unstructured, opts ArgoSyncOptions, validationID string) map[string]any {
	sync := map[string]any{
		"revision": opts.Revision,
		"prune":    opts.Prune != nil && *opts.Prune,
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
	// Argo accepts free-form key=value entries here (Replace=true,
	// ServerSideApply=true, PruneLast=true, ApplyOutOfSyncOnly=true, etc).
	// Caller is responsible for spelling them right.
	if opts.SyncOptions != nil {
		if len(opts.SyncOptions) > 0 {
			sync["syncOptions"] = opts.SyncOptions
		}
	} else if syncOptions, _, _ := unstructured.NestedSlice(app.Object, "spec", "syncPolicy", "syncOptions"); len(syncOptions) > 0 {
		sync["syncOptions"] = syncOptions
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
	if sources, _, _ := unstructured.NestedSlice(app.Object, "spec", "sources"); len(sources) > 0 {
		sync["sources"] = sources
	} else if source, _, _ := unstructured.NestedMap(app.Object, "spec", "source"); len(source) > 0 {
		sync["source"] = source
	}

	operation := map[string]any{
		"initiatedBy": map[string]any{"username": argoInitiator(ctx)},
		"info":        argoOperationInfo(validationID),
		"sync":        sync,
	}
	if validationID == "" {
		if retry, _, _ := unstructured.NestedMap(app.Object, "spec", "syncPolicy", "retry"); len(retry) > 0 {
			operation["retry"] = retry
		}
	}
	return operation
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
	if !found || !argoOperationHasInfo(operation, argoValidationInfoName, validationID) {
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

// SetArgoAutoSync suspends (enable=false) or resumes (enable=true) automated
// sync on an ArgoCD Application.
//
// Suspend saves the whole spec.syncPolicy.automated block in annotations and
// removes the block. If auto-sync is already off it changes nothing, so a
// second suspend can't replace the saved settings with empty ones.
//
// Resume turns back on the newest settings it can find:
//   - a block switched off in place with `enabled: false` (by Argo CD 3.x
//     tooling, or an edit made while suspended): drop `enabled`, keep the rest;
//   - otherwise the block Radar saved on suspend;
//   - otherwise prune and selfHeal from the per-field annotations older Radar
//     builds wrote. With none, the Application was never suspended by Radar:
//     prune starts off, as in Argo CD's UI, and selfHeal on.
//
// If auto-sync is already on, for example re-enabled outside Radar while
// suspended, the current block is kept and only Radar's saved copy is
// dropped. Every write is conditional on the read it was built from.
func SetArgoAutoSync(ctx context.Context, dynClient dynamic.Interface, namespace, name string, enable bool) (OperationResult, error) {
	operation, pastAction := "suspend", "suspended"
	if enable {
		operation, pastAction = "resume", "resumed"
	}
	noChange := false
	err := updateArgoApp(ctx, dynClient, namespace, name, operation, func(app *unstructured.Unstructured) (map[string]any, error) {
		if err := assertNotTerminating(app, "ArgoCD Application", namespace, name); err != nil {
			return nil, err
		}
		var patch map[string]any
		var err error
		if enable {
			patch, noChange = argoResumePatch(app)
		} else {
			patch, noChange, err = argoSuspendPatch(app)
		}
		return patch, err
	})
	if err != nil {
		return OperationResult{}, err
	}

	message := fmt.Sprintf("ArgoCD Application %s/%s auto-sync %s", namespace, name, pastAction)
	if noChange && enable {
		message = fmt.Sprintf("ArgoCD Application %s/%s auto-sync is already on", namespace, name)
	} else if noChange {
		message = fmt.Sprintf("ArgoCD Application %s/%s auto-sync is already off", namespace, name)
	}
	return OperationResult{
		Message:   message,
		Operation: operation,
		Tool:      "argocd",
		Kind:      "Application",
		Namespace: namespace,
		Name:      name,
		NoChange:  noChange,
	}, nil
}

func argoSuspendPatch(app *unstructured.Unstructured) (map[string]any, bool, error) {
	if !ArgoAutoSyncEnabled(app) {
		return nil, true, nil
	}
	automated, _, _ := unstructured.NestedMap(app.Object, "spec", "syncPolicy", "automated")
	// An explicit `enabled: true` means the same as none, and resume drops it.
	delete(automated, "enabled")
	saved, err := json.Marshal(automated)
	if err != nil {
		return nil, false, fmt.Errorf("failed to save the automated sync settings: %w", err)
	}
	prune, _ := automated["prune"].(bool)
	selfHeal, _ := automated["selfHeal"].(bool)
	return map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]any{
				ArgoSuspendedAutomatedAnnotation: string(saved),
				ArgoSuspendedPruneAnnotation:     strconv.FormatBool(prune),
				ArgoSuspendedSelfHealAnnotation:  strconv.FormatBool(selfHeal),
			},
		},
		"spec": map[string]any{
			"syncPolicy": map[string]any{"automated": nil},
		},
	}, false, nil
}

func argoResumePatch(app *unstructured.Unstructured) (map[string]any, bool) {
	annotations := app.GetAnnotations()
	clearSaved := map[string]any{}
	for _, key := range []string{
		ArgoSuspendedAutomatedAnnotation,
		ArgoSuspendedPruneAnnotation,
		ArgoSuspendedSelfHealAnnotation,
		legacyArgoSuspendedPruneAnnotation,
		legacyArgoSuspendedSelfHealAnnotation,
	} {
		clearSaved[key] = nil
	}
	metadata := map[string]any{"annotations": clearSaved}

	if ArgoAutoSyncEnabled(app) {
		for key := range clearSaved {
			if _, ok := annotations[key]; ok {
				return map[string]any{"metadata": metadata}, true
			}
		}
		return nil, true
	}

	var automated map[string]any
	if _, found, _ := unstructured.NestedMap(app.Object, "spec", "syncPolicy", "automated"); found {
		// Merge-patching only `enabled` keeps every other field of the block.
		automated = map[string]any{"enabled": nil}
	} else if saved := argoSavedAutomated(annotations); saved != nil {
		automated = saved
	} else {
		automated = map[string]any{
			"prune":    argoSavedFlag(annotations, ArgoSuspendedPruneAnnotation, legacyArgoSuspendedPruneAnnotation, false),
			"selfHeal": argoSavedFlag(annotations, ArgoSuspendedSelfHealAnnotation, legacyArgoSuspendedSelfHealAnnotation, true),
		}
	}
	return map[string]any{
		"metadata": metadata,
		"spec": map[string]any{
			"syncPolicy": map[string]any{"automated": automated},
		},
	}, false
}

// argoSavedAutomated returns the automated block saved on suspend. An older
// Radar's later suspend rewrites only the per-field annotations, so a saved
// block that disagrees with them is from an earlier cycle and isn't used.
func argoSavedAutomated(annotations map[string]string) map[string]any {
	raw, ok := annotations[ArgoSuspendedAutomatedAnnotation]
	if !ok {
		return nil
	}
	var saved map[string]any
	if err := json.Unmarshal([]byte(raw), &saved); err != nil || saved == nil {
		return nil
	}
	delete(saved, "enabled")
	for key, field := range map[string]string{
		ArgoSuspendedPruneAnnotation:    "prune",
		ArgoSuspendedSelfHealAnnotation: "selfHeal",
	} {
		value, _ := saved[field].(bool)
		if annotated, ok := annotations[key]; ok && annotated != strconv.FormatBool(value) {
			return nil
		}
	}
	return saved
}

// argoSavedFlag reads a per-field suspend annotation, preferring the current
// key over the legacy one, and returns fallback when neither is set.
func argoSavedFlag(annotations map[string]string, key, legacyKey string, fallback bool) bool {
	if v, ok := annotations[key]; ok {
		return v == "true"
	}
	if v, ok := annotations[legacyKey]; ok {
		return v == "true"
	}
	return fallback
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
			NoChange:  true,
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

// RollbackArgoApp rolls an ArgoCD Application back to a prior history entry
// by ID (matches HistoryItem.ID surfaced by the insights builder).
//
// The Application CRD has no rollback operation — the API server prunes an
// `operation.rollback` and the controller then fails with "no operation
// specified". Argo CD's own API server implements rollback as a sync pinned
// to the history entry's revision(s) and source(s), with the apply strategy
// and the app's sync options, and refuses it while automated sync is on.
// This writes that same operation.
func RollbackArgoApp(ctx context.Context, dynClient dynamic.Interface, namespace, name string, opts ArgoRollbackOptions) (OperationResult, error) {
	if opts.ID < 0 {
		return OperationResult{}, fmt.Errorf("rollback requires a non-negative history id")
	}
	timestamp := time.Now().Format(time.RFC3339Nano)
	err := updateArgoApp(ctx, dynClient, namespace, name, "rollback", func(app *unstructured.Unstructured) (map[string]any, error) {
		if err := assertNotTerminating(app, "ArgoCD Application", namespace, name); err != nil {
			return nil, err
		}
		if ArgoAutoSyncEnabled(app) {
			return nil, fmt.Errorf("cannot roll back %s/%s: %w, so Argo CD would sync it forward again; disable auto-sync first", namespace, name, ErrAutoSyncEnabled)
		}
		if argoOperationInProgress(app) {
			return nil, fmt.Errorf("cannot rollback while another operation is in progress for %s/%s: %w", namespace, name, ErrOperationInProgress)
		}
		entry := argoHistoryEntry(app, opts.ID)
		if entry == nil {
			return nil, fmt.Errorf("history entry id=%d not found on Application %s/%s: %w", opts.ID, namespace, name, ErrHistoryEntryNotFound)
		}
		if !ArgoHistoryEntryRollbackable(entry) {
			return nil, fmt.Errorf("cannot roll back %s/%s to history entry id=%d: %w (written by Argo CD v0.11 or older); sync to its revision instead", namespace, name, opts.ID, ErrHistoryEntryNotRollbackable)
		}
		// Like argocd-server's Rollback: no retry strategy, unlike a sync.
		return argoOperationPatch(app, map[string]any{
			"initiatedBy": map[string]any{"username": argoInitiator(ctx)},
			"info":        append(argoOperationInfo(""), map[string]any{"name": argoRollbackInfoName, "value": "true"}),
			"sync":        argoRollbackSyncOperation(app, entry, opts),
		}), nil
	})
	if err != nil {
		return OperationResult{}, err
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

// ArgoSuccessfulRollback returns the applied revision(s) and initiator when
// the last completed operation was a Radar rollback. Argo's native rollback
// is indistinguishable from a pinned sync in operationState, so only the
// explicit operation marker records rollback intent; a later sync replaces
// that evidence.
func ArgoSuccessfulRollback(app *unstructured.Unstructured) (revision, username string) {
	targets, revisions, username, sourcesMismatch := argoCompletedRollbackRevisions(app)
	if len(revisions) == 0 || !slices.Equal(targets, revisions) || sourcesMismatch {
		return "", ""
	}
	return strings.Join(revisions, ", "), username
}

// ArgoRollbackRevisionMismatch returns the requested and applied revisions when
// a completed, marked rollback deployed different sources or revisions. The
// returned revisions can be equal when only sources differ. A successful
// operation phase alone does not prove the controller honored the target.
func ArgoRollbackRevisionMismatch(app *unstructured.Unstructured) (target, applied string) {
	targets, revisions, _, sourcesMismatch := argoCompletedRollbackRevisions(app)
	if len(targets) == 0 || len(revisions) == 0 || (slices.Equal(targets, revisions) && !sourcesMismatch) {
		return "", ""
	}
	return strings.Join(targets, ", "), strings.Join(revisions, ", ")
}

func argoCompletedRollbackRevisions(app *unstructured.Unstructured) (targets, revisions []string, username string, sourcesMismatch bool) {
	phase, _, _ := unstructured.NestedString(app.Object, "status", "operationState", "phase")
	if phase != "Succeeded" || argoOperationInProgress(app) {
		return nil, nil, "", false
	}
	operation, _, _ := unstructured.NestedMap(app.Object, "status", "operationState", "operation")
	if !argoOperationHasInfo(operation, argoRollbackInfoName, "true") {
		return nil, nil, "", false
	}
	dryRun, _, _ := unstructured.NestedBool(operation, "sync", "dryRun")
	if dryRun {
		return nil, nil, "", false
	}
	targets, _, _ = unstructured.NestedStringSlice(operation, "sync", "revisions")
	revisions, _, _ = unstructured.NestedStringSlice(app.Object, "status", "operationState", "syncResult", "revisions")
	if len(targets) == 0 && len(revisions) == 0 {
		target, _, _ := unstructured.NestedString(operation, "sync", "revision")
		revision, _, _ := unstructured.NestedString(app.Object, "status", "operationState", "syncResult", "revision")
		if target != "" && revision != "" {
			targets, revisions = []string{target}, []string{revision}
		}
	}
	username, _, _ = unstructured.NestedString(operation, "initiatedBy", "username")
	sync, _, _ := unstructured.NestedMap(operation, "sync")
	result, _, _ := unstructured.NestedMap(app.Object, "status", "operationState", "syncResult")
	targetSources, resultSources := argoSyncSources(sync), argoSyncSources(result)
	sourcesMismatch = len(targetSources) > 0 && len(resultSources) > 0 && !reflect.DeepEqual(targetSources, resultSources)
	return targets, revisions, username, sourcesMismatch
}

func argoSyncSources(sync map[string]any) []any {
	if sources, _ := sync["sources"].([]any); len(sources) > 0 {
		return sources
	}
	if !jsonValueIsZero(sync["source"]) {
		return []any{sync["source"]}
	}
	return nil
}

func argoHistoryEntry(app *unstructured.Unstructured, id int64) map[string]any {
	history, _, _ := unstructured.NestedSlice(app.Object, "status", "history")
	for _, item := range history {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if entryID, ok := argoHistoryEntryID(entry); ok && entryID == id {
			return entry
		}
	}
	return nil
}

// argoRollbackSyncOperation builds the operation.sync Argo CD's API server
// writes for a rollback: the entry's revision and source for a single-source
// deploy, or its revisions and sources for a multi-source one. The apply
// strategy means sync hooks don't run, as in Argo's own rollback.
func argoRollbackSyncOperation(app *unstructured.Unstructured, entry map[string]any, opts ArgoRollbackOptions) map[string]any {
	sync := map[string]any{
		"prune":        opts.Prune != nil && *opts.Prune,
		"syncStrategy": map[string]any{"apply": map[string]any{}},
	}
	if opts.DryRun != nil && *opts.DryRun {
		sync["dryRun"] = true
	}
	if revision := StringValue(entry["revision"]); revision != "" {
		sync["revision"] = revision
	}
	if revisions, _ := entry["revisions"].([]any); len(revisions) > 0 {
		sync["revisions"] = revisions
	}
	if !jsonValueIsZero(entry["source"]) {
		sync["source"] = entry["source"]
	}
	if sources, _ := entry["sources"].([]any); len(sources) > 0 {
		sync["sources"] = sources
	}
	if syncOptions, found, _ := unstructured.NestedSlice(app.Object, "spec", "syncPolicy", "syncOptions"); found && len(syncOptions) > 0 {
		sync["syncOptions"] = syncOptions
	}
	return sync
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

// SyncFluxWithSource does what `flux reconcile <kind> --with-source` requests:
// it reconciles the consumer's source chain upstream-first, then the consumer.
// For a HelmRelease whose chart is a HelmChart, the chain also includes the
// chart's own source, because the chart builds from that source's artifact.
// Every object is checked before anything is written, so a suspended or
// terminating link fails the request instead of reporting a sync that its
// controller will ignore. Unlike the CLI, it does not wait for each step.
// served is optional; without it the static API versions are used.
func SyncFluxWithSource(ctx context.Context, dynClient dynamic.Interface, kind, namespace, name string, served ...FluxGVRResolver) (OperationResult, error) {
	var resolve FluxGVRResolver
	if len(served) > 0 {
		resolve = served[0]
	}
	entry, err := ResolveFluxKind(kind)
	if err != nil {
		return OperationResult{}, err
	}
	entry = entry.Served(resolve)

	resource, err := dynClient.Resource(entry.GVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return OperationResult{}, fmt.Errorf("FluxCD %s %s/%s not found: %w", entry.Kind, namespace, name, err)
		}
		return OperationResult{}, fmt.Errorf("failed to get %s %s/%s: %w", entry.Kind, namespace, name, err)
	}
	if err := assertSyncable(resource, entry.Kind, namespace, name); err != nil {
		return OperationResult{}, err
	}

	if entry.Kind != "Kustomization" && entry.Kind != "HelmRelease" {
		return OperationResult{}, fmt.Errorf("sync-with-source only supported for Kustomization and HelmRelease")
	}
	if _, ok := resource.Object["spec"].(map[string]any); !ok {
		return OperationResult{}, fmt.Errorf("invalid resource spec for %s %s/%s", entry.Kind, namespace, name)
	}
	source, ok := syncSourceFor(resource)
	if !ok {
		return OperationResult{}, fmt.Errorf("no source reference found in %s %s/%s", entry.Kind, namespace, name)
	}

	timestamp := time.Now().Format(time.RFC3339Nano)
	reconcilePatch := map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]string{
				"reconcile.fluxcd.io/requestedAt": timestamp,
			},
		},
	}
	// An ExternalArtifact is written by the controller that generates it; it has
	// no reconcile request to honor, so only the consumer is reconciled.
	if source.Kind == "ExternalArtifact" {
		if err := mergePatch(ctx, dynClient, entry.GVR, namespace, name, reconcilePatch); err != nil {
			return OperationResult{}, fmt.Errorf("failed to reconcile %s %s/%s: %w", entry.Kind, namespace, name, err)
		}
		return OperationResult{
			Message:     fmt.Sprintf("Reconcile triggered for FluxCD %s %s/%s; its ExternalArtifact %s/%s is produced by another controller and was not reconciled", entry.Kind, namespace, name, source.Namespace, source.Name),
			Operation:   "reconcile",
			Tool:        "fluxcd",
			Kind:        entry.Kind,
			Namespace:   namespace,
			Name:        name,
			RequestedAt: timestamp,
		}, nil
	}

	type link struct {
		entry           FluxKindEntry
		namespace, name string
	}
	var chain []link
	for ref := &source; ref != nil; {
		sourceEntry, err := ResolveFluxKind(ref.Kind)
		if err != nil {
			return OperationResult{}, fmt.Errorf("unknown source kind: %s", ref.Kind)
		}
		sourceEntry = sourceEntry.Served(resolve)
		chain = append([]link{{sourceEntry, ref.Namespace, ref.Name}}, chain...)
		// Every link must be read before anything is written: without the
		// object there is no suspend or deletion state to check, and no chart
		// sourceRef to follow.
		obj, err := dynClient.Resource(sourceEntry.GVR).Namespace(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return OperationResult{}, sourceNotPatchable(sourceEntry.Kind, ref.Namespace, ref.Name, err)
			}
			return OperationResult{}, fmt.Errorf("failed to read source %s %s/%s: %w", sourceEntry.Kind, ref.Namespace, ref.Name, err)
		}
		if err := assertSyncable(obj, sourceEntry.Kind, ref.Namespace, ref.Name); err != nil {
			return OperationResult{}, err
		}
		ref = nil
		if sourceEntry.Kind == "HelmChart" {
			if upstream := FluxSourceReferences(obj); len(upstream) > 0 {
				ref = &upstream[0]
			}
		}
	}

	for _, l := range chain {
		if err := mergePatch(ctx, dynClient, l.entry.GVR, l.namespace, l.name, reconcilePatch); err != nil {
			if apierrors.IsNotFound(err) {
				return OperationResult{}, sourceNotPatchable(l.entry.Kind, l.namespace, l.name, err)
			}
			return OperationResult{}, fmt.Errorf("failed to reconcile source %s %s/%s: %w", l.entry.Kind, l.namespace, l.name, err)
		}
	}
	if err := mergePatch(ctx, dynClient, entry.GVR, namespace, name, reconcilePatch); err != nil {
		return OperationResult{}, fmt.Errorf("failed to reconcile %s %s/%s (note: its sources were reconciled): %w",
			entry.Kind, namespace, name, err)
	}

	steps := make([]string, 0, len(chain))
	for _, l := range chain {
		steps = append(steps, fmt.Sprintf("%s %s/%s", l.entry.Kind, l.namespace, l.name))
	}
	return OperationResult{
		Message:     fmt.Sprintf("Sync with source triggered for FluxCD %s %s/%s after %s", entry.Kind, namespace, name, strings.Join(steps, ", then ")),
		Operation:   "reconcile",
		Tool:        "fluxcd",
		Kind:        entry.Kind,
		Namespace:   namespace,
		Name:        name,
		RequestedAt: timestamp,
		Source:      &SourceRef{Kind: source.Kind, Namespace: source.Namespace, Name: source.Name},
	}, nil
}

// assertSyncable rejects a chain member its controller won't act on: one being
// deleted, or one with spec.suspend set.
func assertSyncable(obj *unstructured.Unstructured, kind, namespace, name string) error {
	if err := assertNotTerminating(obj, "FluxCD "+kind, namespace, name); err != nil {
		return err
	}
	if suspended, _, _ := unstructured.NestedBool(obj.Object, "spec", "suspend"); suspended {
		return fmt.Errorf("FluxCD %s %s/%s is suspended; resume it to sync: %w", kind, namespace, name, ErrResourceSuspended)
	}
	return nil
}

// sourceNotPatchable covers both causes of a 404 on a source: the object is
// gone, or its namespace is (a finalizer-stuck zombie). The wrapped error keeps
// the apierrors chain so handlers still map it to 404.
func sourceNotPatchable(kind, namespace, name string, err error) error {
	return fmt.Errorf(
		"source %s %s/%s could not be patched (verify the resource and its namespace still exist; if the namespace was deleted, this resource may be a finalizer-stuck zombie): %w",
		kind, namespace, name, err,
	)
}

// syncSourceFor picks the consumer's immediate source the way the Flux CLI
// does. An inline HelmRelease pulls its chart through the HelmChart Flux
// generates, named <release namespace>-<release name> in the source's
// namespace. The name comes from the current spec rather than
// status.helmChart, which still names the previous chart while the controller
// replaces it after a source change.
func syncSourceFor(resource *unstructured.Unstructured) (FluxSourceRef, bool) {
	refs := FluxSourceReferences(resource)
	if resource.GetKind() == "HelmRelease" {
		if _, chartRef, _ := unstructured.NestedMap(resource.Object, "spec", "chartRef"); !chartRef {
			for _, ref := range refs {
				if ref.Kind == "HelmRepository" || ref.Kind == "GitRepository" || ref.Kind == "Bucket" {
					chart := ref
					chart.Kind = "HelmChart"
					chart.Name = resource.GetNamespace() + "-" + resource.GetName()
					return chart, true
				}
			}
			return FluxSourceRef{}, false
		}
	}
	if len(refs) == 0 {
		return FluxSourceRef{}, false
	}
	return refs[0], true
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
