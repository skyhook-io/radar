package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/topology"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

const deleteStorageWarning = "Backing storage/data may be reclaimed according to the PV reclaim policy. Storage teardown is not enumerated."

var (
	deleteCallTimeout        = 40 * time.Second
	deleteObservationTimeout = 5 * time.Second
	deleteCascadeTimeout     = 5 * time.Second
)

type deleteResourceInput struct {
	Kind        string `json:"kind" jsonschema:"resource kind or plural"`
	Group       string `json:"group,omitempty" jsonschema:"API group to disambiguate colliding kinds"`
	Namespace   string `json:"namespace,omitempty" jsonschema:"required for namespaced resources; omit for cluster-scoped resources"`
	Name        string `json:"name" jsonschema:"one resource name; batch deletion is not supported"`
	Propagation string `json:"propagation,omitempty" jsonschema:"background (default), foreground, or orphan; cannot change a policy recorded by GC finalizers"`
	DryRun      *bool  `json:"dry_run,omitempty" jsonschema:"default true: server-validated preview; false requires its reviewed confirm token"`
	Confirm     string `json:"confirm,omitempty" jsonschema:"reviewed preview token; expires after 5 minutes"`
}

type deleteConfirmation struct {
	Resource        string
	Namespace       string
	Name            string
	UID             string
	Generation      int64
	Finalizers      []string
	ResourceVersion string `json:",omitempty"`
	Propagation     metav1.DeletionPropagation
}

type deleteResourceResult struct {
	Summary                    string                        `json:"summary,omitempty"`
	Status                     string                        `json:"status"`
	DryRun                     bool                          `json:"dry_run"`
	Kind                       string                        `json:"kind"`
	Group                      string                        `json:"group"`
	Namespace                  string                        `json:"namespace"`
	Name                       string                        `json:"name"`
	UID                        types.UID                     `json:"uid"`
	ResourceVersion            string                        `json:"resourceVersion"`
	Generation                 int64                         `json:"generation"`
	Propagation                metav1.DeletionPropagation    `json:"propagation"`
	Confirm                    string                        `json:"confirm,omitempty"`
	Finalizers                 *[]string                     `json:"finalizers,omitempty"`
	DeletionTimestamp          string                        `json:"deletionTimestamp,omitempty"`
	Cascade                    map[string]any                `json:"cascade,omitempty"`
	Message                    string                        `json:"message"`
	FinalizerGuidance          string                        `json:"finalizerGuidance,omitempty"`
	NamespaceFinalizers        *[]string                     `json:"namespaceFinalizers,omitempty"`
	NamespaceFinalizerGuidance string                        `json:"namespaceFinalizerGuidance,omitempty"`
	Observation                *k8score.DeleteResourceResult `json:"observation,omitempty"`
}

type deleteDependentCounts struct {
	ByKind map[string]int `json:"byKind"`
	Total  int            `json:"total"`
	Basis  string         `json:"basis"`
}

func handleDeleteResource(ctx context.Context, req *mcp.CallToolRequest, input deleteResourceInput) (_ *mcp.CallToolResult, _ any, retErr error) {
	ctx, cancel := context.WithTimeout(ctx, deleteCallTimeout)
	defer cancel()
	deleteAttempted := false
	defer func() {
		if !deleteAttempted && ctx.Err() != nil && (errors.Is(retErr, context.DeadlineExceeded) || errors.Is(retErr, context.Canceled)) {
			retErr = fmt.Errorf("deletion call budget exhausted or canceled before DELETE; no deletion attempted: %w", retErr)
		}
	}()
	if err := requireConnected(); err != nil {
		return nil, nil, err
	}
	kind, group := strings.TrimSpace(input.Kind), strings.TrimSpace(input.Group)
	namespace, name := strings.TrimSpace(input.Namespace), strings.TrimSpace(input.Name)
	if kind == "" || name == "" {
		return nil, nil, fmt.Errorf("kind and name are required")
	}
	dryRun := input.DryRun == nil || *input.DryRun
	auditRequest := (&http.Request{Method: http.MethodDelete, URL: &url.URL{Path: "/mcp"}}).WithContext(ctx)
	audit := auth.AuditActionDetails{Action: "delete_resource", Kind: kind, Group: group, Namespace: namespace, Name: name, Source: "mcp", Outcome: "failed"}
	if dryRun {
		audit.Outcome = "preview_failed"
	}
	defer func() { auth.AuditLogAction(auditRequest, audit) }()
	if !dryRun && input.Confirm == "" {
		return nil, nil, fmt.Errorf("confirm is required; run dry_run=true and review the preview first")
	}
	dyn, contextName, operation, err := k8s.DeletionClientSnapshot(ctx)
	if err != nil {
		return nil, nil, err
	}
	stopOperation := context.AfterFunc(operation, cancel)
	defer stopOperation()
	ctx = context.WithValue(ctx, confirmationContextKey{}, contextName)
	if dyn == nil {
		return nil, nil, errNotConnected()
	}
	type resolved struct {
		gvr        schema.GroupVersionResource
		namespaced bool
	}
	r, err := waitStep(ctx, func() (resolved, error) {
		gvr, namespaced, err := resolveMutationGVR(kind, group)
		return resolved{gvr, namespaced}, err
	})
	if err != nil {
		return nil, nil, err
	}
	if err := operation.Err(); err != nil {
		return nil, nil, fmt.Errorf("cluster connection changed while resolving deletion target: %w", err)
	}
	gvr, namespaced := r.gvr, r.namespaced
	audit.Group = gvr.Group
	if namespaced && namespace == "" {
		return nil, nil, fmt.Errorf("namespace is required for namespaced kind %q", kind)
	}
	if !namespaced && namespace != "" {
		return nil, nil, fmt.Errorf("namespace must be empty for cluster-scoped kind %q", kind)
	}
	propagation := metav1.DeletePropagationBackground
	switch strings.ToLower(strings.TrimSpace(input.Propagation)) {
	case "", "background":
	case "foreground":
		propagation = metav1.DeletePropagationForeground
	case "orphan":
		propagation = metav1.DeletePropagationOrphan
	default:
		return nil, nil, fmt.Errorf("propagation must be background, foreground, or orphan")
	}
	var client dynamic.ResourceInterface = dyn.Resource(gvr)
	if namespaced {
		client = dyn.Resource(gvr).Namespace(namespace)
	}
	var approved deleteConfirmation
	if !dryRun {
		target, err := verifyMutationConfirmation(ctx, "delete_resource", input.Confirm)
		if err != nil {
			return nil, nil, err
		}
		if json.Unmarshal([]byte(target), &approved) != nil || approved.UID == "" || (approved.Generation == 0 && approved.ResourceVersion == "") {
			return nil, nil, fmt.Errorf("confirm is malformed; run dry_run=true again and review the new preview")
		}
		if approved.Resource != gvr.String() || approved.Namespace != namespace || approved.Name != name {
			return nil, nil, fmt.Errorf("deletion target or propagation changed since preview; run dry_run=true again and review the new preview")
		}
	}
	obj, err := waitStep(ctx, func() (*unstructured.Unstructured, error) {
		return client.Get(ctx, name, metav1.GetOptions{})
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read deletion target: %w", err)
	}
	audit.Kind = obj.GetKind()
	uid, rv := obj.GetUID(), obj.GetResourceVersion()
	if uid == "" || rv == "" {
		return nil, nil, fmt.Errorf("deletion target is missing UID or resourceVersion; no deletion attempted")
	}
	pendingPropagation, hasPendingPolicy := k8score.PendingDeletionPolicy(obj.GetFinalizers())
	if hasPendingPolicy {
		if strings.TrimSpace(input.Propagation) != "" && propagation != pendingPropagation {
			if obj.GetDeletionTimestamp() != nil {
				return nil, nil, fmt.Errorf("already deleting with %s; a pending deletion's propagation can't be changed", strings.ToLower(string(pendingPropagation)))
			}
			return nil, nil, fmt.Errorf("carries a recorded %s deletion policy; a pending deletion's propagation can't be changed", strings.ToLower(string(pendingPropagation)))
		}
		propagation = pendingPropagation
	}
	var cascade map[string]any
	token := ""
	if dryRun {
		cascade = deleteCascadePreview(ctx, obj, propagation, operation)
		if cascade["rootResolved"] == true {
			current := deleteConfirmation{Resource: gvr.String(), Namespace: namespace, Name: name, UID: string(uid), Generation: obj.GetGeneration(), Finalizers: slices.Clone(obj.GetFinalizers()), Propagation: propagation}
			slices.Sort(current.Finalizers)
			if current.Generation == 0 {
				current.ResourceVersion = rv
			}
			target, err := json.Marshal(current)
			if err != nil {
				return nil, nil, err
			}
			token, err = issueMutationConfirmation(ctx, "delete_resource", string(target))
			if err != nil {
				return nil, nil, err
			}
		}
	} else if err := approved.check(obj, propagation); err != nil {
		return nil, nil, err
	}
	opts := metav1.DeleteOptions{PropagationPolicy: &propagation, Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &rv}}
	if hasPendingPolicy {
		opts.PropagationPolicy = nil
	}
	if dryRun {
		opts.DryRun = []string{metav1.DryRunAll}
	}
	if err := operation.Err(); err != nil {
		return nil, nil, fmt.Errorf("cluster connection changed before DELETE; no deletion attempted: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	deleteAttempted = true
	err = client.Delete(ctx, name, opts)
	if err == nil {
		audit.Outcome = "accepted"
		if dryRun {
			audit.Outcome = "preview"
		}
	}
	if err != nil {
		if dryRun {
			return nil, nil, fmt.Errorf("deletion preview failed; no deletion was persisted: %w", err)
		}
		var status apierrors.APIStatus
		definitiveRejection := errors.As(err, &status) && status.Status().Code >= 400 && status.Status().Code < 500 && status.Status().Code != http.StatusRequestTimeout
		if !definitiveRejection {
			audit.Outcome = "unknown"
			return nil, nil, fmt.Errorf("deletion outcome unknown: DELETE may have been accepted; inspect the resource before retrying: %w", err)
		}
		if apierrors.IsConflict(err) {
			return nil, nil, fmt.Errorf("failed to delete resource: %w; the object changed, run dry_run=true again", err)
		}
		return nil, nil, fmt.Errorf("failed to delete resource: %w", err)
	}
	result := deleteResourceResult{Status: "ok", DryRun: dryRun, Kind: obj.GetKind(), Group: gvr.Group, Namespace: namespace, Name: name, UID: uid, ResourceVersion: rv, Generation: obj.GetGeneration(), Propagation: propagation}
	if dryRun {
		result.Confirm = token
		finalizers := obj.GetFinalizers()
		result.Finalizers = &finalizers
		result.Cascade = cascade
		result.Summary = deletePreviewSummary(obj, propagation, result.Cascade)
		result.Message = "Deletion preview validated by the API server; no deletion was persisted. Review the cascade limits before confirming."

		if ts := obj.GetDeletionTimestamp(); ts != nil {
			result.DeletionTimestamp = ts.UTC().Format(time.RFC3339)
			result.Message = "Already deleting. Cleanup may continue, and DELETE may shorten a remaining grace period."
			if hasPendingPolicy {
				result.Message = "Already deleting; confirmation keeps the pending propagation policy. Cleanup may continue, and DELETE may shorten a remaining grace period."
			}
		}
		if len(obj.GetFinalizers()) > 0 {
			result.FinalizerGuidance = "Finalizers may keep this object deleting while controllers finish cleanup. This tool does not patch finalizers; the API server and controllers manage finalizers during deletion. Inspect cleanup before using patch_resource as a separate, explicit step; removing finalizers can orphan resources."
		}
		if gvr.Group == "" && gvr.Resource == "namespaces" {
			finalizers, _, _ := unstructured.NestedStringSlice(obj.Object, "spec", "finalizers")
			result.NamespaceFinalizers = &finalizers
			if len(finalizers) > 0 {
				result.NamespaceFinalizerGuidance = "Namespace lifecycle finalizers may keep this Namespace deleting until its contents are cleaned up. Inspect status.conditions and remaining contents; this tool does not clear namespace lifecycle finalizers."
			}
		}
		if token == "" {
			result.Message = "Impact preview unavailable; no confirmation token was issued. " + result.Message + " Inspect cascade.reason and cascade.unavailableReason before retrying; this tool requires caller-readable impact. Use the UI or kubectl if Radar cannot provide it."
		}
	} else {
		result.Message = "Deletion accepted; completion is asynchronous."
		observationCtx, cancel := context.WithTimeout(ctx, deleteObservationTimeout)
		defer cancel()
		result.Observation, err = waitStep(observationCtx, func() (*k8score.DeleteResourceResult, error) {
			return k8score.ObserveResourceDeletion(observationCtx, client, name, uid), nil
		})
		if err != nil {
			result.Observation = &k8score.DeleteResourceResult{ObservationError: err.Error()}
		}
		if result.Observation.ObservationError != "" {
			switch {
			case operation.Err() != nil:
				result.Observation.ObservationError = "Deletion accepted, but the cluster connection changed before cleanup was observed: " + result.Observation.ObservationError
			case errors.Is(observationCtx.Err(), context.DeadlineExceeded):
				result.Observation.ObservationError = "Deletion accepted, but cleanup was not observed within the observation budget: " + result.Observation.ObservationError
			default:
				result.Observation.ObservationError = "Deletion accepted, but cleanup could not be observed because the follow-up read failed: " + result.Observation.ObservationError
			}
		}
	}
	return toJSONResult(result)
}

func (approved deleteConfirmation) check(obj *unstructured.Unstructured, propagation metav1.DeletionPropagation) error {
	finalizers := slices.Clone(obj.GetFinalizers())
	slices.Sort(finalizers)
	switch {
	case approved.UID != string(obj.GetUID()):
		return fmt.Errorf("deletion object replaced since preview (UID differs); run dry_run=true again and review the replacement")
	case !slices.Equal(approved.Finalizers, finalizers):
		return fmt.Errorf("deletion object changed since preview: its finalizers are now %v; inspect them, then run dry_run=true again and review the new preview", finalizers)
	case approved.Generation != obj.GetGeneration():
		return fmt.Errorf("deletion object changed since preview: its spec changed (generation %d, previewed %d); run dry_run=true again and review the new preview", obj.GetGeneration(), approved.Generation)
	case approved.Generation == 0 && approved.ResourceVersion != obj.GetResourceVersion():
		return fmt.Errorf("deletion object changed since preview: its resourceVersion differs, and this kind has no generation to tell spec from status; run dry_run=true again and review the new preview")
	case approved.Propagation != propagation:
		return fmt.Errorf("deletion propagation changed since preview: now %s; run dry_run=true again and review the new preview", propagation)
	}
	return nil
}

func deleteCascadePreview(ctx context.Context, obj *unstructured.Unstructured, propagation metav1.DeletionPropagation, operation context.Context) map[string]any {
	ctx, cancel := context.WithTimeout(ctx, deleteCascadeTimeout)
	defer cancel()
	counts := &deleteDependentCounts{ByKind: map[string]int{}, Basis: "observed owner references (not exhaustive)"}
	result := map[string]any{"approximation": true, "dependents": []topology.ResourceRef{}, "dependentCounts": counts, "coverage": "unknown", "rootResolved": false}
	if len(obj.GetFinalizers()) > 0 {
		result["notEnumerated"] = []string{"controller-finalizer cleanup"}
	}
	if auth.UserFromContext(ctx) != nil {
		result["visibilityNote"] = "Only resources Radar observes and you can read are listed."
	}
	switch {
	case obj.GetKind() == "Namespace" && obj.GetAPIVersion() == "v1":
		result["notEnumerated"] = []string{"namespace contents"}
		result["scopeWarning"] = "Deleting this Namespace deletes ALL namespace contents, including resources not shown here. Orphan propagation does not prevent namespace cleanup."
	case obj.GetKind() == "CustomResourceDefinition" && obj.GetAPIVersion() == "apiextensions.k8s.io/v1":
		result["notEnumerated"] = []string{"CRD instances"}
		result["scopeWarning"] = "Deleting this CRD deletes ALL instances across the cluster. Orphan propagation does not preserve CRD instances."
	case obj.GetAPIVersion() == "v1" && (obj.GetKind() == "PersistentVolumeClaim" || obj.GetKind() == "PersistentVolume"):
		result["notEnumerated"] = []string{"storage reclamation"}
		result["scopeWarning"] = deleteStorageWarning
	}
	if propagation == metav1.DeletePropagationOrphan {
		result["propagationEffect"] = "Owner-reference dependents will be orphaned; namespace, CRD and controller cleanup can still delete resources."
	}
	preview, err := waitStep(ctx, func() (*topology.CascadeDeletePreview, error) {
		return k8s.DeletionPreview(ctx, obj, operation, func(c *ownership.Catalog) ownership.Access { return ownershipAccess(ctx, c) }), nil
	})
	if err != nil {
		result["incomplete"] = true
		result["reason"] = "Deletion preview budget exhausted; dependents are unknown."
		return result
	}
	result["rootResolved"] = preview.RootResolved
	if !preview.RootResolved {
		result["unavailableReason"] = preview.UnavailableReason
		if action, ok := readreason.ActionFor(readreason.Code(preview.UnavailableReason)); ok {
			result["recoveryAction"] = action
		}
		result["reason"] = "Ownership preview unavailable: " + preview.UnavailableReason
		return result
	}
	result["basis"] = preview.Basis
	result["coverage"] = "partial"
	result["ownershipCoverage"] = preview.Coverage
	result["reason"] = "Observed caller-readable owner-reference projection, not a guarantee of completed deletion."
	for _, ref := range preview.Dependents {
		counts.Total++
		counts.ByKind[ref.Kind]++
	}
	if len(preview.Dependents) > 100 {
		result["dependents"] = preview.Dependents[:100]
		result["truncated"] = true
	} else {
		result["dependents"] = preview.Dependents
	}
	if len(preview.PossibleDependents) > 100 {
		result["possibleDependents"] = preview.PossibleDependents[:100]
		result["possibleTruncated"] = true
	} else if len(preview.PossibleDependents) > 0 {
		result["possibleDependents"] = preview.PossibleDependents
	}
	result["possibleDependentCount"] = len(preview.PossibleDependents)
	if preview.Coverage != nil && preview.Coverage.Truncated {
		result["incomplete"] = true
	}
	if preview.ControllerTeardown != nil {
		if len(preview.ControllerTeardown.Resources) > 100 {
			preview.ControllerTeardown.Resources = preview.ControllerTeardown.Resources[:100]
			preview.ControllerTeardown.Coverage.Truncated = true
		}
		result["controllerTeardown"] = preview.ControllerTeardown
	}
	if len(preview.NotEnumerated) > 0 {
		result["notEnumerated"] = preview.NotEnumerated
	}
	if preview.MayReclaimStorage {
		warning, _ := result["scopeWarning"].(string)
		if warning != deleteStorageWarning {
			result["scopeWarning"] = strings.TrimSpace(warning + " " + deleteStorageWarning)
		}
	}
	return result
}

func deletePreviewSummary(obj *unstructured.Unstructured, propagation metav1.DeletionPropagation, cascade map[string]any) string {
	name := obj.GetName()
	if obj.GetNamespace() != "" {
		name = obj.GetNamespace() + "/" + name
	}
	summary := fmt.Sprintf("Deletes %s %s", obj.GetKind(), name)
	deleting := obj.GetDeletionTimestamp()
	if deleting != nil {
		summary = fmt.Sprintf("Already deleting %s %s since %s", obj.GetKind(), name, deleting.UTC().Format(time.RFC3339))
	}
	if pending, ok := k8score.PendingDeletionPolicy(obj.GetFinalizers()); ok {
		if deleting != nil {
			summary += "; keeping pending propagation: " + strings.ToLower(string(pending))
		} else {
			summary += "; carries a recorded " + strings.ToLower(string(pending)) + " deletion policy"
		}
	}
	switch {
	case obj.GetKind() == "Namespace" && obj.GetAPIVersion() == "v1":
		summary += " and all namespace contents (count unknown)"
	case obj.GetKind() == "CustomResourceDefinition" && obj.GetAPIVersion() == "apiextensions.k8s.io/v1":
		summary += " and all CRD instances (count unknown)"
	}
	counts := cascade["dependentCounts"].(*deleteDependentCounts)
	if cascade["coverage"] == "unknown" {
		summary += "; dependents unknown"
	} else if counts.Total == 0 {
		summary += "; no caller-readable dependents observed"
	} else {
		kinds := make([]string, 0, len(counts.ByKind))
		for kind := range counts.ByKind {
			kinds = append(kinds, kind)
		}
		slices.Sort(kinds)
		parts := make([]string, 0, len(kinds))
		for _, kind := range kinds {
			parts = append(parts, fmt.Sprintf("%s: %d", kind, counts.ByKind[kind]))
		}
		effect := "observed dependents"
		if propagation == metav1.DeletePropagationOrphan {
			effect = "observed related resources (owner-reference dependents will be orphaned)"
		}
		summary += fmt.Sprintf("; %s: %s (%d total, not exhaustive)", effect, strings.Join(parts, ", "), counts.Total)
	}
	if possible, ok := cascade["possibleDependentCount"].(int); ok && possible > 0 {
		effect := "possible owner-reference deletions"
		if propagation == metav1.DeletePropagationOrphan {
			effect = "possible owner-reference dependents (will be orphaned)"
		}
		summary += fmt.Sprintf("; %s: %d (other owners unresolved)", effect, possible)
	}
	if teardown, ok := cascade["controllerTeardown"].(*topology.ControllerTeardown); ok {
		summary += fmt.Sprintf("; controller cleanup: %s %s (destination %s; inventory is not exhaustive)", teardown.Controller, teardown.Action, teardown.Destination)
	}
	if coverage, ok := cascade["ownershipCoverage"].(*topology.ChildrenCoverage); ok && !coverage.Complete {
		summary += "; garbage-collection coverage incomplete"
	}
	if omitted, ok := cascade["notEnumerated"].([]string); ok && len(omitted) > 0 {
		summary += "; cleanup not enumerated: " + strings.Join(omitted, ", ")
	}
	finalizers := obj.GetFinalizers()
	if obj.GetKind() == "Namespace" && obj.GetAPIVersion() == "v1" {
		namespaceFinalizers, _, _ := unstructured.NestedStringSlice(obj.Object, "spec", "finalizers")
		finalizers = append(finalizers, namespaceFinalizers...)
	}
	switch {
	case len(finalizers) > 0 && deleting != nil:
		summary += "; waiting on finalizers: " + strings.Join(finalizers, ", ") + "; this tool does not patch them; cleanup may continue"
	case len(finalizers) > 0:
		summary += "; may wait on finalizers: " + strings.Join(finalizers, ", ")
	}
	return summary
}
