package k8score

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"sigs.k8s.io/yaml"
)

// WorkloadRevision represents a single revision in a workload's rollout history.
type WorkloadRevision struct {
	Number    int64     `json:"number"`
	CreatedAt time.Time `json:"createdAt"`
	Image     string    `json:"image"` // primary container image
	IsCurrent bool      `json:"isCurrent"`
	Replicas  int64     `json:"replicas"`
	Template  string    `json:"template,omitempty"` // Pod template spec as YAML (for revision diff)
}

// UpdateResourceOptions contains options for updating a resource.
type UpdateResourceOptions struct {
	Kind                    string
	Namespace               string
	Name                    string
	YAML                    string // YAML content to apply
	Force                   bool   // Force SSA field ownership conflicts (override Helm/Flux/Argo/kubectl)
	DryRun                  bool
	ExpectedResourceVersion string
}

type PreviewUpdateResourceResult struct {
	Object          *unstructured.Unstructured
	Live            *unstructured.Unstructured
	Submitted       *unstructured.Unstructured
	ResourceVersion string
	Warnings        []string
}

const radarOwnershipReclaimedWarning = "Radar reclaimed field ownership left by an earlier Radar write. No external manager ownership was overridden."

// DeleteResourceOptions contains options for deleting a resource.
type DeleteResourceOptions struct {
	Kind      string
	Group     string // API group, disambiguates kinds that collide across groups (e.g. Knative vs core Service)
	Namespace string
	Name      string
	Force     bool // Force delete with grace period 0
}

// ApplyResourceOptions contains options for creating or applying a resource.
type ApplyResourceOptions struct {
	YAML                    string // Raw YAML manifest
	Mode                    string // "apply" (server-side apply, default) or "create" (strict create)
	DryRun                  bool   // Validate without persisting
	NamespaceOverride       string // If set, overrides the namespace in the YAML
	Force                   bool   // Force SSA field ownership conflicts
	ExpectedResourceVersion string
	ExpectedResourceAbsent  bool
	UseCreateForAbsent      bool // Match a guarded create when the preflight confirms absence
}

// ApplyResourceResult contains the result of a create/apply operation.
type ApplyResourceResult struct {
	Name      string                     `json:"name"`
	Namespace string                     `json:"namespace"`
	Kind      string                     `json:"kind"`
	Created   bool                       `json:"created"` // true if newly created, false if updated
	Action    string                     `json:"action,omitempty"`
	Object    *unstructured.Unstructured `json:"-"`
	Previous  *unstructured.Unstructured `json:"-"`
	Submitted *unstructured.Unstructured `json:"-"`

	// Warnings are advisory notes derived from the actual state of the cluster
	// (e.g., "this resource is reconciled by Helm" or "field X you omitted is
	// still present after apply because manager Y owns it"). They never block
	// the apply — the apply succeeded if no error was returned. Treat each
	// entry as a self-contained sentence.
	Warnings []string `json:"warnings,omitempty"`
}

// WorkloadManager provides workload lifecycle operations using injected clients.
type WorkloadManager struct {
	dynClient dynamic.Interface
	discovery *ResourceDiscovery
}

// NewWorkloadManager creates a WorkloadManager with the given clients.
func NewWorkloadManager(dynClient dynamic.Interface, discovery *ResourceDiscovery) *WorkloadManager {
	return &WorkloadManager{dynClient: dynClient, discovery: discovery}
}

// UpdateResource updates a Kubernetes resource from YAML using server-side apply.
// SSA avoids the resourceVersion round-trip that PUT requires and matches
// kubectl apply --server-side / Lens semantics. Force=true takes ownership of
// fields the user is editing even if another field manager last wrote them.
func (m *WorkloadManager) UpdateResource(ctx context.Context, opts UpdateResourceOptions) (*unstructured.Unstructured, error) {
	obj, gvr, ri, err := m.prepareUpdateResource(opts)
	if err != nil {
		return nil, err
	}
	result, _, err := m.patchUpdateResource(ctx, opts, obj, gvr, ri)
	return result, err
}

func (m *WorkloadManager) PreviewUpdateResource(ctx context.Context, opts UpdateResourceOptions) (*PreviewUpdateResourceResult, error) {
	opts.DryRun = true
	obj, gvr, ri, err := m.prepareUpdateResource(opts)
	if err != nil {
		return nil, err
	}

	submitted := obj.DeepCopy()
	live, err := ri.Get(ctx, opts.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to read resource before preview: %w", err)
	}
	obj.SetResourceVersion(live.GetResourceVersion())
	predicted, reclaimedRadarOwnership, err := m.patchUpdateResource(ctx, opts, obj, gvr, ri)
	if err != nil {
		return nil, err
	}

	warnings := append(
		EnrichObjectWarnings(predicted),
		checkFieldRemoval(obj, live, predicted)...,
	)
	if reclaimedRadarOwnership {
		warnings = append([]string{radarOwnershipReclaimedWarning}, warnings...)
	}
	return &PreviewUpdateResourceResult{
		Object:          predicted,
		Live:            live,
		Submitted:       submitted,
		ResourceVersion: live.GetResourceVersion(),
		Warnings:        warnings,
	}, nil
}

func (m *WorkloadManager) prepareUpdateResource(opts UpdateResourceOptions) (*unstructured.Unstructured, schema.GroupVersionResource, dynamic.ResourceInterface, error) {
	if m.discovery == nil {
		return nil, schema.GroupVersionResource{}, nil, fmt.Errorf("resource discovery not initialized")
	}
	if m.dynClient == nil {
		return nil, schema.GroupVersionResource{}, nil, fmt.Errorf("dynamic client not initialized")
	}

	obj := &unstructured.Unstructured{}
	if err := yaml.Unmarshal([]byte(opts.YAML), &obj.Object); err != nil {
		return nil, schema.GroupVersionResource{}, nil, fmt.Errorf("invalid YAML: %w", err)
	}

	kindForLookup := opts.Kind
	if obj.GetKind() != "" {
		kindForLookup = obj.GetKind()
	}
	apiGroup := ""
	if apiVersion := obj.GetAPIVersion(); strings.Contains(apiVersion, "/") {
		apiGroup = strings.SplitN(apiVersion, "/", 2)[0]
	}
	var gvr schema.GroupVersionResource
	var ok bool
	if apiGroup != "" {
		gvr, ok = m.discovery.GetGVRWithGroup(kindForLookup, apiGroup)
		if !ok {
			if fallbackGVR, fallbackOK := m.discovery.GetGVR(kindForLookup); fallbackOK && fallbackGVR.Group == apiGroup {
				gvr, ok = fallbackGVR, true
			}
		}
	} else {
		gvr, ok = m.discovery.GetGVR(kindForLookup)
	}
	if !ok {
		return nil, schema.GroupVersionResource{}, nil, fmt.Errorf("unknown resource kind: %s", kindForLookup)
	}
	requestedGVR, requestedOK := m.discovery.GetGVRWithGroup(opts.Kind, apiGroup)
	if !requestedOK {
		requestedGVR, requestedOK = m.discovery.GetGVR(opts.Kind)
	}
	if !requestedOK {
		return nil, schema.GroupVersionResource{}, nil, fmt.Errorf("unknown resource kind: %s", opts.Kind)
	}
	if requestedGVR.Group != gvr.Group || requestedGVR.Resource != gvr.Resource {
		return nil, schema.GroupVersionResource{}, nil, fmt.Errorf("resource kind mismatch: expected %s, got %s", opts.Kind, kindForLookup)
	}

	if obj.GetName() != opts.Name {
		return nil, schema.GroupVersionResource{}, nil, fmt.Errorf("resource name mismatch: expected %s, got %s", opts.Name, obj.GetName())
	}
	if opts.Namespace != "" && obj.GetNamespace() != opts.Namespace {
		return nil, schema.GroupVersionResource{}, nil, fmt.Errorf("resource namespace mismatch: expected %s, got %s", opts.Namespace, obj.GetNamespace())
	}

	stripServerManagedFields(obj)
	var ri dynamic.ResourceInterface
	if opts.Namespace != "" {
		ri = m.dynClient.Resource(gvr).Namespace(opts.Namespace)
	} else {
		ri = m.dynClient.Resource(gvr)
	}
	return obj, gvr, ri, nil
}

func (m *WorkloadManager) patchUpdateResource(ctx context.Context, opts UpdateResourceOptions, obj *unstructured.Unstructured, gvr schema.GroupVersionResource, ri dynamic.ResourceInterface) (*unstructured.Unstructured, bool, error) {
	if opts.ExpectedResourceVersion != "" {
		live, err := ri.Get(ctx, opts.Name, metav1.GetOptions{})
		if err != nil {
			return nil, false, fmt.Errorf("failed to verify reviewed resource version: %w", err)
		}
		if live.GetResourceVersion() != opts.ExpectedResourceVersion {
			return nil, false, apierrors.NewConflict(
				gvr.GroupResource(),
				opts.Name,
				fmt.Errorf("resource changed after review; review the latest version before applying"),
			)
		}
		obj.SetResourceVersion(opts.ExpectedResourceVersion)
	}

	dryRun := []string(nil)
	if opts.DryRun {
		dryRun = []string{metav1.DryRunAll}
	}
	result, reclaimedRadarOwnership, err := patchWithRadarOwnershipReclaim(
		ctx,
		ri,
		opts.Name,
		obj,
		opts.Force,
		dryRun,
	)
	if err != nil {
		return nil, false, fmt.Errorf("failed to update resource: %w", err)
	}
	return result, reclaimedRadarOwnership, nil
}

// stripServerManagedFields removes metadata fields the apiserver owns. SSA
// rejects ownership claims on these, and editor round-trips often round-trip
// them back unchanged. status is intentionally NOT stripped: for resources
// with a status subresource the apiserver ignores it on /apply anyway, and
// for CRDs without a status subresource the user's edit IS the way to set it.
func stripServerManagedFields(obj *unstructured.Unstructured) {
	for _, f := range [][]string{
		{"metadata", "resourceVersion"},
		{"metadata", "managedFields"},
		{"metadata", "uid"},
		{"metadata", "generation"},
		{"metadata", "creationTimestamp"},
		{"metadata", "selfLink"},
	} {
		unstructured.RemoveNestedField(obj.Object, f...)
	}
}

// ApplyResource creates or updates a Kubernetes resource from YAML.
// In "apply" mode (default), uses server-side apply (idempotent create-or-update).
// In "create" mode, uses strict create (fails if resource exists).
func (m *WorkloadManager) ApplyResource(ctx context.Context, opts ApplyResourceOptions) (*ApplyResourceResult, error) {
	if m.discovery == nil {
		return nil, fmt.Errorf("resource discovery not initialized")
	}
	if m.dynClient == nil {
		return nil, fmt.Errorf("dynamic client not initialized")
	}

	obj := &unstructured.Unstructured{}
	if err := yaml.Unmarshal([]byte(opts.YAML), &obj.Object); err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}

	kind := obj.GetKind()
	if kind == "" {
		return nil, fmt.Errorf("YAML must include 'kind'")
	}
	apiVersion := obj.GetAPIVersion()
	if apiVersion == "" {
		return nil, fmt.Errorf("YAML must include 'apiVersion'")
	}
	name := obj.GetName()
	if name == "" {
		return nil, fmt.Errorf("YAML must include 'metadata.name'")
	}

	// Apply namespace override if provided
	if opts.NamespaceOverride != "" {
		obj.SetNamespace(opts.NamespaceOverride)
	}

	// Resolve GVR using group from apiVersion for disambiguation
	group := ""
	if parts := strings.SplitN(apiVersion, "/", 2); len(parts) == 2 {
		group = parts[0]
	}

	var gvr schema.GroupVersionResource
	var ok bool
	if group != "" {
		gvr, ok = m.discovery.GetGVRWithGroup(kind, group)
	} else {
		gvr, ok = m.discovery.GetGVR(kind)
	}
	if !ok {
		return nil, fmt.Errorf("unknown resource kind: %s (apiVersion: %s)", kind, apiVersion)
	}

	ns := obj.GetNamespace()
	mode := opts.Mode
	if mode == "" {
		mode = "apply"
	}

	dryRun := []string{}
	if opts.DryRun {
		dryRun = []string{metav1.DryRunAll}
	}

	result := &ApplyResourceResult{
		Name:      name,
		Namespace: ns,
		Kind:      kind,
		Submitted: obj.DeepCopy(),
	}

	var client dynamic.ResourceInterface
	if ns != "" {
		client = m.dynClient.Resource(gvr).Namespace(ns)
	} else {
		client = m.dynClient.Resource(gvr)
	}
	// Pre-apply GET: feeds create/update classification, the external-manager warning, and the SSA
	// field-removal verification. Best-effort — a NotFound just means the
	// resource is being newly created, and other errors shouldn't block the
	// apply itself.
	var pre *unstructured.Unstructured
	var preGetErr error
	got, getErr := client.Get(ctx, name, metav1.GetOptions{})
	if getErr == nil {
		pre = got
		result.Action = "update"
	} else if apierrors.IsNotFound(getErr) {
		result.Action = "create"
	} else {
		preGetErr = getErr
		result.Action = "unknown"
		log.Printf("[k8s] apply_resource: pre-apply GET %s/%s/%s failed: %v", kind, ns, name, getErr)
	}
	result.Previous = pre
	if opts.ExpectedResourceAbsent {
		if preGetErr != nil {
			return result, fmt.Errorf("failed to verify reviewed resource absence: %w", preGetErr)
		}
		if pre != nil {
			return result, apierrors.NewConflict(
				gvr.GroupResource(),
				name,
				fmt.Errorf("resource was created after review; review the latest version before applying"),
			)
		}
	} else if opts.ExpectedResourceVersion != "" {
		if preGetErr != nil {
			return result, fmt.Errorf("failed to verify reviewed resource version: %w", preGetErr)
		}
		if pre == nil || pre.GetResourceVersion() != opts.ExpectedResourceVersion {
			return result, apierrors.NewConflict(
				gvr.GroupResource(),
				name,
				fmt.Errorf("resource changed after review; review the latest version before applying"),
			)
		}
		obj.SetResourceVersion(opts.ExpectedResourceVersion)
	}

	if mode == "create" {
		created, err := client.Create(ctx, obj, metav1.CreateOptions{
			DryRun:          dryRun,
			FieldManager:    "radar",
			FieldValidation: metav1.FieldValidationStrict,
		})
		if err != nil {
			return result, fmt.Errorf("failed to create resource: %w", err)
		}
		result.Created = true
		result.Action = "create"
		result.Object = created
		m.populateApplyWarnings(ctx, result, obj, pre, created, ns, kind, name, opts.DryRun)
		return result, nil
	}

	if opts.ExpectedResourceAbsent || (opts.UseCreateForAbsent && pre == nil && preGetErr == nil) {
		// SSA has no must-not-exist precondition, so Create makes the reviewed
		// absence check atomic. Preview uses the same operation when absence is
		// observed so its admitted object matches the final write.
		created, err := client.Create(ctx, obj, metav1.CreateOptions{
			DryRun:          dryRun,
			FieldManager:    "radar",
			FieldValidation: metav1.FieldValidationStrict,
		})
		if err != nil {
			return result, fmt.Errorf("failed to apply reviewed create: %w", err)
		}
		result.Created = true
		result.Action = "create"
		result.Object = created
		m.populateApplyWarnings(ctx, result, obj, pre, created, ns, kind, name, opts.DryRun)
		return result, nil
	}

	// Apply mode: server-side apply
	applied, reclaimedRadarOwnership, err := patchWithRadarOwnershipReclaim(
		ctx,
		client,
		name,
		obj,
		opts.Force,
		dryRun,
	)
	if err != nil {
		return result, fmt.Errorf("failed to apply resource: %w", err)
	}
	if reclaimedRadarOwnership {
		result.Warnings = append(result.Warnings, radarOwnershipReclaimedWarning)
	}

	// SSA results report "applied"; Action carries create/update classification.
	result.Created = false
	result.Object = applied

	// Post-apply GET feeds the state-derived warnings (run against what
	// actually landed) and the field-removal diff. Fetch it for creates too,
	// not just updates — an SSA apply that creates a resource still wants the
	// external-manager / terminating-namespace warnings (field-removal stays
	// gated on pre != nil below).
	var post *unstructured.Unstructured
	if opts.DryRun {
		post = applied
	} else {
		got, getErr := client.Get(ctx, name, metav1.GetOptions{})
		if getErr == nil {
			post = got
			result.Object = got
		} else {
			log.Printf("[k8s] apply_resource: post-apply GET %s/%s/%s failed: %v", kind, ns, name, getErr)
		}
	}

	// A non-NotFound pre-apply GET error means the object likely existed but we
	// couldn't read it, so field-retention can't be diffed (pre is nil). Tell the
	// agent the verification was skipped rather than letting silence imply a clean
	// apply — a partial manifest may have dropped fields without warning.
	if !opts.DryRun && pre == nil && preGetErr != nil {
		result.Warnings = append(result.Warnings, "Pre-apply GET failed; Radar could not compute field-retention warnings — a partial manifest may have silently dropped fields.")
	}

	m.populateApplyWarnings(ctx, result, obj, pre, post, ns, kind, name, opts.DryRun)
	return result, nil
}

// populateApplyWarnings appends advisory warnings to result.Warnings.
//
//   - State-derived warnings (external manager, deletionTimestamp, etc.) come
//     from the shared EnrichObjectWarnings; we run it against the admitted
//     object so the caller sees the resource the way any read of it would.
//   - Apply-specific checks (SSA field-removal verification, ConfigMap/Secret
//     consumer reload reminder) require knowledge of what was submitted vs.
//     what landed and so live here.
//
// Best-effort throughout — a failed check never fails the apply itself.
func (m *WorkloadManager) populateApplyWarnings(ctx context.Context, result *ApplyResourceResult, submitted, pre, post *unstructured.Unstructured, namespace, kind, name string, dryRun bool) {
	// State-derived: prefer the admitted object, then fall back to the prior live
	// object only when post-apply verification failed.
	target := post
	if target == nil {
		target = pre
	}
	result.Warnings = append(result.Warnings, EnrichObjectWarnings(target)...)

	if pre != nil && post != nil {
		result.Warnings = append(result.Warnings, checkFieldRemoval(submitted, pre, post)...)
	} else if !dryRun && pre != nil && post == nil {
		result.Warnings = append(result.Warnings, "Post-apply verification GET failed; Radar could not compute field-retention warnings from the live object.")
	}
	// The consumer-reload reminder only makes sense for a persisted edit — on a
	// dry run nothing landed, so the "restart consumers" advice would be
	// premature (and the namespace-wide LISTs wasted).
	if !dryRun && (kind == "ConfigMap" || kind == "Secret") && namespace != "" {
		consumers, partial := findConfigMapSecretConsumers(ctx, m.dynClient, m.discovery, namespace, kind, name)
		if w := formatConsumerWarning(kind, name, consumers, partial); w != "" {
			result.Warnings = append(result.Warnings, w)
		}
	}
}

func boolPtr(b bool) *bool { return &b }

func patchWithRadarOwnershipReclaim(
	ctx context.Context,
	client dynamic.ResourceInterface,
	name string,
	obj *unstructured.Unstructured,
	force bool,
	dryRun []string,
) (*unstructured.Unstructured, bool, error) {
	patch := func(body []byte, patchForce bool) (*unstructured.Unstructured, error) {
		return client.Patch(ctx, name, types.ApplyPatchType, body, metav1.PatchOptions{
			FieldManager:    "radar",
			FieldValidation: metav1.FieldValidationStrict,
			Force:           boolPtr(patchForce),
			DryRun:          dryRun,
		})
	}
	body, err := json.Marshal(obj.Object)
	if err != nil {
		return nil, false, fmt.Errorf("failed to marshal resource: %w", err)
	}
	applied, err := patch(body, force)
	if err == nil || force || !isOnlyRadarFieldManagerConflict(err) {
		return applied, false, err
	}

	retryObj := obj
	if retryObj.GetResourceVersion() == "" {
		live, getErr := client.Get(ctx, name, metav1.GetOptions{})
		if getErr != nil {
			return nil, false, err
		}
		retryObj = obj.DeepCopy()
		retryObj.SetResourceVersion(live.GetResourceVersion())
		body, err = json.Marshal(retryObj.Object)
		if err != nil {
			return nil, false, fmt.Errorf("failed to marshal resource: %w", err)
		}
		applied, err = patch(body, false)
		if err == nil || !isOnlyRadarFieldManagerConflict(err) {
			return applied, false, err
		}
	}

	applied, err = patch(body, true)
	return applied, err == nil, err
}

func isOnlyRadarFieldManagerConflict(err error) bool {
	var statusErr *apierrors.StatusError
	if !errors.As(err, &statusErr) || statusErr.ErrStatus.Details == nil {
		return false
	}
	causes := statusErr.ErrStatus.Details.Causes
	if len(causes) == 0 {
		return false
	}
	for _, cause := range causes {
		if cause.Type != metav1.CauseTypeFieldManagerConflict ||
			!strings.Contains(cause.Message, `conflict with "radar"`) {
			return false
		}
	}
	return true
}

// DeleteResource deletes a Kubernetes resource.
func (m *WorkloadManager) DeleteResource(ctx context.Context, opts DeleteResourceOptions) error {
	if m.discovery == nil {
		return fmt.Errorf("resource discovery not initialized")
	}
	if m.dynClient == nil {
		return fmt.Errorf("dynamic client not initialized")
	}

	gvr, ok := m.discovery.GetGVRWithGroup(opts.Kind, opts.Group)
	if !ok {
		return fmt.Errorf("unknown resource kind: %s", opts.Kind)
	}

	if opts.Force {
		finalizerPatch := []byte(`{"metadata":{"finalizers":null}}`)
		var patchErr error
		if opts.Namespace != "" {
			_, patchErr = m.dynClient.Resource(gvr).Namespace(opts.Namespace).Patch(ctx, opts.Name, types.MergePatchType, finalizerPatch, metav1.PatchOptions{})
		} else {
			_, patchErr = m.dynClient.Resource(gvr).Patch(ctx, opts.Name, types.MergePatchType, finalizerPatch, metav1.PatchOptions{})
		}
		if patchErr != nil && !apierrors.IsNotFound(patchErr) {
			if apierrors.IsForbidden(patchErr) {
				return fmt.Errorf("force delete requires patch permission to strip finalizers: %w", patchErr)
			}
			log.Printf("[delete] Failed to strip finalizers from %s %s/%s: %v", opts.Kind, opts.Namespace, opts.Name, patchErr)
		}
	}

	deleteOpts := metav1.DeleteOptions{}
	if opts.Force {
		gracePeriod := int64(0)
		deleteOpts.GracePeriodSeconds = &gracePeriod
	}

	var err error
	if opts.Namespace != "" {
		err = m.dynClient.Resource(gvr).Namespace(opts.Namespace).Delete(ctx, opts.Name, deleteOpts)
	} else {
		err = m.dynClient.Resource(gvr).Delete(ctx, opts.Name, deleteOpts)
	}
	if err != nil {
		if opts.Force && apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to delete resource: %w", err)
	}

	if !opts.Force {
		var obj *unstructured.Unstructured
		if opts.Namespace != "" {
			obj, _ = m.dynClient.Resource(gvr).Namespace(opts.Namespace).Get(ctx, opts.Name, metav1.GetOptions{})
		} else {
			obj, _ = m.dynClient.Resource(gvr).Get(ctx, opts.Name, metav1.GetOptions{})
		}
		if obj != nil && obj.GetDeletionTimestamp() != nil && len(obj.GetFinalizers()) > 0 {
			return fmt.Errorf("resource is stuck in Terminating state due to finalizers — use force delete to remove it")
		}
	}

	return nil
}

// TriggerCronJob creates a Job from a CronJob.
func (m *WorkloadManager) TriggerCronJob(ctx context.Context, namespace, name string) (*unstructured.Unstructured, error) {
	if m.dynClient == nil {
		return nil, fmt.Errorf("dynamic client not initialized")
	}
	if m.discovery == nil {
		return nil, fmt.Errorf("resource discovery not initialized")
	}

	cronJobGVR, ok := m.discovery.GetGVR("cronjobs")
	if !ok {
		return nil, fmt.Errorf("cronjobs resource not found")
	}

	cronJob, err := m.dynClient.Resource(cronJobGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get cronjob: %w", err)
	}

	jobTemplate, found, err := unstructured.NestedMap(cronJob.Object, "spec", "jobTemplate")
	if err != nil {
		return nil, fmt.Errorf("failed to get job template from cronjob %s/%s: %w", namespace, name, err)
	}
	if !found {
		return nil, fmt.Errorf("cronjob %s/%s has no spec.jobTemplate", namespace, name)
	}

	jobName := fmt.Sprintf("%s-manual-%d", name, time.Now().Unix())
	job := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "batch/v1",
			"kind":       "Job",
			"metadata": map[string]any{
				"name":      jobName,
				"namespace": namespace,
				"annotations": map[string]any{
					"cronjob.kubernetes.io/instantiate": "manual",
				},
				"ownerReferences": []any{
					map[string]any{
						"apiVersion":         cronJob.GetAPIVersion(),
						"kind":               cronJob.GetKind(),
						"name":               cronJob.GetName(),
						"uid":                string(cronJob.GetUID()),
						"controller":         true,
						"blockOwnerDeletion": true,
					},
				},
			},
		},
	}

	if spec, found, _ := unstructured.NestedMap(jobTemplate, "spec"); found {
		if err := unstructured.SetNestedMap(job.Object, spec, "spec"); err != nil {
			return nil, fmt.Errorf("failed to set job spec: %w", err)
		}
	}
	if labels, found, _ := unstructured.NestedStringMap(jobTemplate, "metadata", "labels"); found {
		if err := unstructured.SetNestedStringMap(job.Object, labels, "metadata", "labels"); err != nil {
			return nil, fmt.Errorf("failed to set job labels: %w", err)
		}
	}

	jobGVR, ok := m.discovery.GetGVR("jobs")
	if !ok {
		return nil, fmt.Errorf("jobs resource not found")
	}

	result, err := m.dynClient.Resource(jobGVR).Namespace(namespace).Create(ctx, job, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to create job: %w", err)
	}

	return result, nil
}

// SetCronJobSuspend sets the suspend field on a CronJob.
func (m *WorkloadManager) SetCronJobSuspend(ctx context.Context, namespace, name string, suspend bool) error {
	if m.dynClient == nil {
		return fmt.Errorf("dynamic client not initialized")
	}
	if m.discovery == nil {
		return fmt.Errorf("resource discovery not initialized")
	}

	cronJobGVR, ok := m.discovery.GetGVR("cronjobs")
	if !ok {
		return fmt.Errorf("cronjobs resource not found")
	}

	patch := fmt.Sprintf(`{"spec":{"suspend":%t}}`, suspend)
	_, err := m.dynClient.Resource(cronJobGVR).Namespace(namespace).Patch(
		ctx, name, types.MergePatchType, []byte(patch), metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("failed to patch cronjob: %w", err)
	}

	return nil
}

// RestartWorkload performs a rolling restart on a Deployment, StatefulSet, or DaemonSet.
func (m *WorkloadManager) RestartWorkload(ctx context.Context, kind, namespace, name string) error {
	if m.dynClient == nil {
		return fmt.Errorf("dynamic client not initialized")
	}
	if m.discovery == nil {
		return fmt.Errorf("resource discovery not initialized")
	}

	gvr, ok := m.discovery.GetGVR(kind)
	if !ok {
		return fmt.Errorf("unknown resource kind: %s", kind)
	}

	restartTime := time.Now().Format(time.RFC3339)
	patch := fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":"%s"}}}}}`, restartTime)

	_, err := m.dynClient.Resource(gvr).Namespace(namespace).Patch(
		ctx, name, types.MergePatchType, []byte(patch), metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("failed to restart workload: %w", err)
	}

	return nil
}

// ScaleWorkload scales a Deployment or StatefulSet to the specified replica count.
func (m *WorkloadManager) ScaleWorkload(ctx context.Context, kind, namespace, name string, replicas int32) error {
	if m.dynClient == nil {
		return fmt.Errorf("dynamic client not initialized")
	}
	if m.discovery == nil {
		return fmt.Errorf("resource discovery not initialized")
	}

	normalizedKind := NormalizeWorkloadKind(kind)
	if normalizedKind != "deployments" && normalizedKind != "statefulsets" {
		return fmt.Errorf("scaling not supported for %s (only deployments and statefulsets)", kind)
	}

	gvr, ok := m.discovery.GetGVR(normalizedKind)
	if !ok {
		return fmt.Errorf("unknown resource kind: %s", kind)
	}

	patch := fmt.Sprintf(`{"spec":{"replicas":%d}}`, replicas)
	_, err := m.dynClient.Resource(gvr).Namespace(namespace).Patch(
		ctx, name, types.MergePatchType, []byte(patch), metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("failed to scale workload: %w", err)
	}

	return nil
}

// ScaleWorkloadDirect scales a Deployment or StatefulSet without requiring a WorkloadManager.
// It accepts a dynamic.Interface directly, for use by callers that don't have a full WorkloadManager.
func ScaleWorkloadDirect(ctx context.Context, dynClient dynamic.Interface, kind, namespace, name string, replicas int32) error {
	if dynClient == nil {
		return fmt.Errorf("dynamic client must not be nil")
	}

	normalizedKind := NormalizeWorkloadKind(kind)

	var gvr schema.GroupVersionResource
	switch normalizedKind {
	case "deployments":
		gvr = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	case "statefulsets":
		gvr = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}
	default:
		return fmt.Errorf("scaling not supported for %s (only deployments and statefulsets)", kind)
	}

	patch := fmt.Sprintf(`{"spec":{"replicas":%d}}`, replicas)
	_, err := dynClient.Resource(gvr).Namespace(namespace).Patch(
		ctx, name, types.MergePatchType, []byte(patch), metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("failed to scale workload: %w", err)
	}

	return nil
}

// ListWorkloadRevisions returns the revision history for a Deployment, StatefulSet, or DaemonSet.
func (m *WorkloadManager) ListWorkloadRevisions(ctx context.Context, kind, namespace, name string) ([]WorkloadRevision, error) {
	if m.dynClient == nil {
		return nil, fmt.Errorf("dynamic client not initialized")
	}
	if m.discovery == nil {
		return nil, fmt.Errorf("resource discovery not initialized")
	}

	normalizedKind := NormalizeWorkloadKind(kind)

	workloadGVR, ok := m.discovery.GetGVR(normalizedKind)
	if !ok {
		return nil, fmt.Errorf("unknown resource kind: %s", kind)
	}

	workload, err := m.dynClient.Resource(workloadGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get workload: %w", err)
	}
	workloadUID := string(workload.GetUID())

	switch normalizedKind {
	case "deployments":
		return m.listDeploymentRevisions(ctx, namespace, name, workloadUID)
	case "statefulsets", "daemonsets":
		return m.listControllerRevisions(ctx, namespace, name, workloadUID)
	default:
		return nil, fmt.Errorf("revision history not supported for %s", kind)
	}
}

func (m *WorkloadManager) listDeploymentRevisions(ctx context.Context, namespace, name, workloadUID string) ([]WorkloadRevision, error) {
	rsGVR, ok := m.discovery.GetGVR("replicasets")
	if !ok {
		return nil, fmt.Errorf("replicasets resource not found")
	}

	rsList, err := m.dynClient.Resource(rsGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list replicasets: %w", err)
	}

	return BuildDeploymentRevisions(rsList.Items, workloadUID), nil
}

// BuildDeploymentRevisions builds WorkloadRevision[] from unstructured ReplicaSets owned by a Deployment.
// workloadUID filters by UID-based ownership; pass empty string to skip UID-based filter.
func BuildDeploymentRevisions(rsList []unstructured.Unstructured, workloadUID string) []WorkloadRevision {
	var revisions []WorkloadRevision
	var maxRevision int64

	for _, rs := range rsList {
		if workloadUID != "" {
			owned := false
			for _, ref := range rs.GetOwnerReferences() {
				if string(ref.UID) == workloadUID {
					owned = true
					break
				}
			}
			if !owned {
				continue
			}
		}

		revStr, ok := rs.GetAnnotations()["deployment.kubernetes.io/revision"]
		if !ok {
			continue
		}
		revNum, err := strconv.ParseInt(revStr, 10, 64)
		if err != nil {
			continue
		}

		image := extractContainerImage(rs.Object)
		replicas, _, _ := unstructured.NestedInt64(rs.Object, "spec", "replicas")

		var templateStr string
		if template, found, _ := unstructured.NestedMap(rs.Object, "spec", "template"); found && template != nil {
			if templateYAML, err := yaml.Marshal(template); err == nil {
				templateStr = string(templateYAML)
			}
		}

		if revNum > maxRevision {
			maxRevision = revNum
		}

		revisions = append(revisions, WorkloadRevision{
			Number:    revNum,
			CreatedAt: rs.GetCreationTimestamp().Time,
			Image:     image,
			Replicas:  replicas,
			Template:  templateStr,
		})
	}

	for i := range revisions {
		if revisions[i].Number == maxRevision {
			revisions[i].IsCurrent = true
		}
	}

	sort.Slice(revisions, func(i, j int) bool { return revisions[i].Number > revisions[j].Number })

	return revisions
}

func (m *WorkloadManager) listControllerRevisions(ctx context.Context, namespace, name, workloadUID string) ([]WorkloadRevision, error) {
	crGVR, ok := m.discovery.GetGVR("controllerrevisions")
	if !ok {
		return nil, fmt.Errorf("controllerrevisions resource not found")
	}

	crList, err := m.dynClient.Resource(crGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list controllerrevisions: %w", err)
	}

	return BuildControllerRevisions(crList.Items, workloadUID), nil
}

// BuildControllerRevisions builds WorkloadRevision[] from unstructured ControllerRevisions owned by a StatefulSet or DaemonSet.
// workloadUID filters by UID-based ownership; pass empty string to skip UID-based filter.
func BuildControllerRevisions(crList []unstructured.Unstructured, workloadUID string) []WorkloadRevision {
	var revisions []WorkloadRevision
	var maxRevision int64

	for _, cr := range crList {
		if workloadUID != "" {
			owned := false
			for _, ref := range cr.GetOwnerReferences() {
				if string(ref.UID) == workloadUID {
					owned = true
					break
				}
			}
			if !owned {
				continue
			}
		}

		revNum, found, err := unstructured.NestedInt64(cr.Object, "revision")
		if err != nil || !found {
			continue
		}

		image := extractContainerImageFromData(cr.Object)

		var templateStr string
		if data, found, _ := unstructured.NestedMap(cr.Object, "data"); found && data != nil {
			if template, tFound, _ := unstructured.NestedMap(data, "spec", "template"); tFound && template != nil {
				if templateYAML, err := yaml.Marshal(template); err == nil {
					templateStr = string(templateYAML)
				}
			} else {
				if dataYAML, err := yaml.Marshal(data); err == nil {
					templateStr = string(dataYAML)
				}
			}
		}

		if revNum > maxRevision {
			maxRevision = revNum
		}

		revisions = append(revisions, WorkloadRevision{
			Number:    revNum,
			CreatedAt: cr.GetCreationTimestamp().Time,
			Image:     image,
			Template:  templateStr,
		})
	}

	for i := range revisions {
		if revisions[i].Number == maxRevision {
			revisions[i].IsCurrent = true
		}
	}

	sort.Slice(revisions, func(i, j int) bool { return revisions[i].Number > revisions[j].Number })

	return revisions
}

// RollbackWorkload rolls back a Deployment, StatefulSet, or DaemonSet to a specific revision.
func (m *WorkloadManager) RollbackWorkload(ctx context.Context, kind, namespace, name string, revision int64) error {
	if m.dynClient == nil {
		return fmt.Errorf("dynamic client not initialized")
	}
	if m.discovery == nil {
		return fmt.Errorf("resource discovery not initialized")
	}

	normalizedKind := NormalizeWorkloadKind(kind)

	workloadGVR, ok := m.discovery.GetGVR(normalizedKind)
	if !ok {
		return fmt.Errorf("unknown resource kind: %s", kind)
	}

	workload, err := m.dynClient.Resource(workloadGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get workload: %w", err)
	}
	workloadUID := string(workload.GetUID())

	switch normalizedKind {
	case "deployments":
		return m.rollbackDeployment(ctx, namespace, name, workloadUID, revision)
	case "statefulsets", "daemonsets":
		return m.rollbackControllerRevision(ctx, normalizedKind, namespace, name, workloadUID, revision)
	default:
		return fmt.Errorf("rollback not supported for %s", kind)
	}
}

func (m *WorkloadManager) rollbackDeployment(ctx context.Context, namespace, name, workloadUID string, revision int64) error {
	rsGVR, ok := m.discovery.GetGVR("replicasets")
	if !ok {
		return fmt.Errorf("replicasets resource not found")
	}

	rsList, err := m.dynClient.Resource(rsGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list replicasets: %w", err)
	}

	var targetRS *unstructured.Unstructured
	for i := range rsList.Items {
		rs := &rsList.Items[i]
		owned := false
		for _, ref := range rs.GetOwnerReferences() {
			if string(ref.UID) == workloadUID {
				owned = true
				break
			}
		}
		if !owned {
			continue
		}

		revStr, ok := rs.GetAnnotations()["deployment.kubernetes.io/revision"]
		if !ok {
			continue
		}
		revNum, err := strconv.ParseInt(revStr, 10, 64)
		if err != nil {
			continue
		}
		if revNum == revision {
			targetRS = rs
			break
		}
	}

	if targetRS == nil {
		return fmt.Errorf("revision %d not found", revision)
	}

	podTemplate, found, err := unstructured.NestedMap(targetRS.Object, "spec", "template")
	if err != nil || !found {
		return fmt.Errorf("failed to extract pod template from revision %d", revision)
	}

	patchData := map[string]any{"spec": map[string]any{"template": podTemplate}}
	patchBytes, err := json.Marshal(patchData)
	if err != nil {
		return fmt.Errorf("failed to build rollback patch: %w", err)
	}

	deployGVR, ok := m.discovery.GetGVR("deployments")
	if !ok {
		return fmt.Errorf("deployments resource not found")
	}

	_, err = m.dynClient.Resource(deployGVR).Namespace(namespace).Patch(
		ctx, name, types.MergePatchType, patchBytes, metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("failed to rollback deployment: %w", err)
	}

	return nil
}

func (m *WorkloadManager) rollbackControllerRevision(ctx context.Context, normalizedKind, namespace, name, workloadUID string, revision int64) error {
	crGVR, ok := m.discovery.GetGVR("controllerrevisions")
	if !ok {
		return fmt.Errorf("controllerrevisions resource not found")
	}

	crList, err := m.dynClient.Resource(crGVR).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("failed to list controllerrevisions: %w", err)
	}

	var targetCR *unstructured.Unstructured
	for i := range crList.Items {
		cr := &crList.Items[i]
		owned := false
		for _, ref := range cr.GetOwnerReferences() {
			if string(ref.UID) == workloadUID {
				owned = true
				break
			}
		}
		if !owned {
			continue
		}

		revNum, found, err := unstructured.NestedInt64(cr.Object, "revision")
		if err != nil || !found {
			continue
		}
		if revNum == revision {
			targetCR = cr
			break
		}
	}

	if targetCR == nil {
		return fmt.Errorf("revision %d not found", revision)
	}

	data, found, err := unstructured.NestedMap(targetCR.Object, "data")
	if err != nil || !found {
		return fmt.Errorf("failed to extract data from revision %d", revision)
	}

	patchBytes, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to build rollback patch: %w", err)
	}

	gvr, ok := m.discovery.GetGVR(normalizedKind)
	if !ok {
		return fmt.Errorf("unknown resource kind: %s", normalizedKind)
	}

	_, err = m.dynClient.Resource(gvr).Namespace(namespace).Patch(
		ctx, name, types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("failed to rollback workload: %w", err)
	}

	return nil
}

// NormalizeWorkloadKind converts various kind formats to the plural lowercase form.
func NormalizeWorkloadKind(kind string) string {
	switch kind {
	case "Deployment", "deployment", "deployments":
		return "deployments"
	case "StatefulSet", "statefulset", "statefulsets":
		return "statefulsets"
	case "DaemonSet", "daemonset", "daemonsets":
		return "daemonsets"
	default:
		return kind
	}
}

func extractContainerImage(obj map[string]any) string {
	containers, found, _ := unstructured.NestedSlice(obj, "spec", "template", "spec", "containers")
	if !found || len(containers) == 0 {
		return ""
	}
	if container, ok := containers[0].(map[string]any); ok {
		if image, ok := container["image"].(string); ok {
			return image
		}
	}
	return ""
}

func extractContainerImageFromData(obj map[string]any) string {
	data, found, _ := unstructured.NestedMap(obj, "data")
	if !found {
		return ""
	}
	return extractContainerImage(data)
}
