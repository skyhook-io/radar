package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

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
	gvr, namespaced := r.gvr, r.namespaced
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
	dryRun := input.DryRun == nil || *input.DryRun
	auditRequest := (&http.Request{Method: http.MethodDelete, URL: &url.URL{Path: "/mcp"}}).WithContext(ctx)
	audit := auth.AuditActionDetails{Action: "delete_resource", Kind: kind, Group: gvr.Group, Namespace: namespace, Name: name, Source: "mcp", Outcome: "failed"}
	if dryRun {
		audit.Outcome = "preview_failed"
	}
	defer func() { auth.AuditLogAction(auditRequest, audit) }()
	if !dryRun && input.Confirm == "" {
		return nil, nil, fmt.Errorf("confirm is required; run dry_run=true and review the preview first")
	}
	dyn := k8s.DynamicClientFromContext(ctx)
	if dyn == nil {
		return nil, nil, errNotConnected()
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
	requestedPropagation := propagation
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
	current := deleteConfirmation{Resource: gvr.String(), Namespace: namespace, Name: name, UID: string(uid), Generation: obj.GetGeneration(), Finalizers: slices.Clone(obj.GetFinalizers()), Propagation: propagation}
	slices.Sort(current.Finalizers)
	if current.Generation == 0 {
		current.ResourceVersion = rv
	}
	if !dryRun {
		if err := approved.check(obj, propagation); err != nil {
			return nil, nil, err
		}
	}
	targetBytes, err := json.Marshal(current)
	if err != nil {
		return nil, nil, err
	}
	target := string(targetBytes)
	token := ""
	if dryRun {
		token, err = issueMutationConfirmation(ctx, "delete_resource", target)
		if err != nil {
			return nil, nil, err
		}
	}
	opts := metav1.DeleteOptions{PropagationPolicy: &propagation, Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &rv}}
	if hasPendingPolicy {
		opts.PropagationPolicy = nil
	}
	if dryRun {
		opts.DryRun = []string{metav1.DryRunAll}
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	deleteAttempted = true
	err = client.Delete(ctx, name, opts)
	if !dryRun && apierrors.IsConflict(err) {
		obj, err = waitStep(ctx, func() (*unstructured.Unstructured, error) {
			return client.Get(ctx, name, metav1.GetOptions{})
		})
		if err != nil {
			return nil, nil, fmt.Errorf("DELETE was rejected due to conflict; no retry attempted: failed to re-read deletion target: %w", err)
		}
		propagation = requestedPropagation
		pendingPropagation, hasPendingPolicy = k8score.PendingDeletionPolicy(obj.GetFinalizers())
		if hasPendingPolicy {
			propagation = pendingPropagation
		}
		if err := approved.check(obj, propagation); err != nil {
			return nil, nil, err
		}
		rv = obj.GetResourceVersion()
		if rv == "" {
			return nil, nil, fmt.Errorf("deletion target is missing resourceVersion; no retry attempted")
		}
		opts.PropagationPolicy = &propagation
		if hasPendingPolicy {
			opts.PropagationPolicy = nil
		}
		if err := ctx.Err(); err != nil {
			return nil, nil, fmt.Errorf("deletion retry not attempted after conflict: %w", err)
		}
		err = client.Delete(ctx, name, opts)
	}
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
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
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
		result.Cascade = deleteCascadePreview(ctx, obj, propagation)
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
			if observationCtx.Err() != nil {
				result.Observation.ObservationError = "Deletion accepted, but cleanup was not observed within the observation budget: " + result.Observation.ObservationError
			} else {
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

func deleteCascadePreview(ctx context.Context, obj *unstructured.Unstructured, propagation metav1.DeletionPropagation) map[string]any {
	ctx, cancel := context.WithTimeout(ctx, deleteCascadeTimeout)
	defer cancel()
	dependents := []topology.ResourceRef{}
	counts := &deleteDependentCounts{ByKind: map[string]int{}, Basis: "observed (not exhaustive)"}
	result := map[string]any{"approximation": true, "dependents": dependents, "dependentCounts": counts, "coverage": "unknown"}
	notEnumerated := []string{}
	if len(obj.GetFinalizers()) > 0 {
		notEnumerated = append(notEnumerated, "controller-finalizer cleanup")
	}
	if auth.UserFromContext(ctx) != nil {
		result["visibilityNote"] = "Dependents you can't read are not listed."
	}
	switch {
	case obj.GetKind() == "Namespace" && obj.GetAPIVersion() == "v1":
		notEnumerated = append(notEnumerated, "namespace contents")
		result["scopeWarning"] = "Deleting this Namespace deletes ALL namespace contents, including resources not shown here. Contents are not enumerated; their count is unknown. Orphan propagation does not prevent namespace cleanup."
	case obj.GetKind() == "CustomResourceDefinition" && obj.GetAPIVersion() == "apiextensions.k8s.io/v1":
		notEnumerated = append(notEnumerated, "CRD instances")
		result["scopeWarning"] = "Deleting this CRD deletes ALL instances of the CRD across the cluster. Instances are not enumerated; their count is unknown. Orphan propagation does not preserve CRD instances."
	default:
		if obj.GetNamespace() == "" {
			notEnumerated = append(notEnumerated, "cluster-scoped cascades outside cached topology")
		}
	}
	if len(notEnumerated) > 0 {
		result["notEnumerated"] = notEnumerated
	}
	if propagation == metav1.DeletePropagationOrphan {
		result["propagationEffect"] = "Owner-reference garbage collection will orphan dependents."
		if len(notEnumerated) > 0 {
			result["propagationEffect"] = "Owner-reference garbage collection will orphan dependents; cleanup listed in notEnumerated may still delete resources."
		}
	}
	incomplete := maps.Clone(result)
	// A shared topology build may outlive the deadline; its worker owns separate counts.
	incomplete["dependentCounts"] = &deleteDependentCounts{ByKind: map[string]int{}, Basis: "observed (not exhaustive)"}
	preview, err := waitStep(ctx, func() (map[string]any, error) {
		return enumerateDeleteCascade(ctx, obj, result), ctx.Err()
	})
	if err != nil {
		incomplete["incomplete"] = true
		incomplete["reason"] = "Cascade preview incomplete: enumeration did not finish within its budget; dependents are unknown."
		return incomplete
	}
	return preview
}

func enumerateDeleteCascade(ctx context.Context, obj *unstructured.Unstructured, result map[string]any) map[string]any {
	cache := k8s.GetResourceCache()
	if cache == nil {
		result["reason"] = "Topology cache is unavailable; no dependents were enumerated."
		return result
	}
	dp := k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery())
	opts := topology.DefaultBuildOptions()
	opts.IncludeReplicaSets = true
	opts.IncludeSecrets = true
	topo, err := summaryCtxTopoMemo.GetScoped(cache.Generation(), opts, func() (*topology.Topology, error) {
		return topology.NewBuilder(k8s.NewTopologyResourceProvider(cache)).WithDynamic(dp).Build(opts)
	})
	if err != nil {
		result["reason"] = "Topology could not be built; no dependents were enumerated."
		return result
	}
	if ctx.Err() != nil {
		return result
	}
	if topo.RequiresNamespaceFilter {
		result["reason"] = "Cluster is too large for bounded cascade enumeration; no dependents were enumerated."
		return result
	}
	preview := topology.GetCascadeDeletePreview(topology.ResourceRef{Kind: obj.GetKind(), Group: resourceid.GroupFromAPIVersion(obj.GetAPIVersion()), Namespace: obj.GetNamespace(), Name: obj.GetName()}, topo, dp)
	result["rootResolved"] = preview.RootResolved
	if !preview.RootResolved {
		result["reason"] = "The deletion target is not represented in cached topology; dependents were not enumerated."
		return result
	}
	result["coverage"] = "partial"
	result["reason"] = "Observed caller-readable related resources from cached topology; counts are not exhaustive and do not predict garbage collection or controller pruning."
	dependents := result["dependents"].([]topology.ResourceRef)
	counts := result["dependentCounts"].(*deleteDependentCounts)
	for _, ref := range preview.Dependents {
		if ctx.Err() != nil {
			break
		}
		gvr, _, err := resolveMutationGVR(ref.Kind, ref.Group)
		if err != nil || !canReadInNamespace(ctx, gvr.Group, gvr.Resource, ref.Namespace, "get") {
			continue
		}
		counts.ByKind[ref.Kind]++
		counts.Total++
		if len(dependents) < 100 {
			dependents = append(dependents, ref)
		} else {
			result["truncated"] = true
		}
	}
	result["dependents"] = dependents
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
