package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
)

type deleteResourceInput struct {
	Kind        string `json:"kind" jsonschema:"resource kind or plural"`
	Group       string `json:"group,omitempty" jsonschema:"API group to disambiguate colliding kinds"`
	Namespace   string `json:"namespace,omitempty" jsonschema:"required for namespaced resources; omit for cluster-scoped resources"`
	Name        string `json:"name" jsonschema:"one resource name; batch deletion is not supported"`
	Propagation string `json:"propagation,omitempty" jsonschema:"background (default), foreground, or orphan; namespace contents and CRD instances are deleted even with orphan"`
	DryRun      *bool  `json:"dry_run,omitempty" jsonschema:"default true: validate deletion on the API server and return a mandatory preview; false requires confirm from that preview"`
	Confirm     string `json:"confirm,omitempty" jsonschema:"token returned by a reviewed preview; valid for 5 minutes and bound to object UID/resourceVersion, caller, context, and propagation"`
}

func handleDeleteResource(ctx context.Context, req *mcp.CallToolRequest, input deleteResourceInput) (*mcp.CallToolResult, any, error) {
	kind, group := strings.TrimSpace(input.Kind), strings.TrimSpace(input.Group)
	namespace, name := strings.TrimSpace(input.Namespace), strings.TrimSpace(input.Name)
	if kind == "" || name == "" {
		return nil, nil, fmt.Errorf("kind and name are required")
	}
	gvr, namespaced, err := resolveMutationGVR(kind, group)
	if err != nil {
		return nil, nil, err
	}
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
	obj, err := client.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read deletion target: %w", err)
	}
	uid, rv := obj.GetUID(), obj.GetResourceVersion()
	if uid == "" || rv == "" {
		return nil, nil, fmt.Errorf("deletion target is missing UID or resourceVersion; no deletion attempted")
	}
	targetBytes, err := json.Marshal(struct {
		Resource        string
		Namespace       string
		Name            string
		UID             string
		ResourceVersion string
		Propagation     metav1.DeletionPropagation
	}{gvr.String(), namespace, name, string(uid), rv, propagation})
	if err != nil {
		return nil, nil, err
	}
	target := string(targetBytes)
	if !dryRun {
		if err := verifyMutationConfirmation(ctx, "delete_resource", target, input.Confirm); err != nil {
			return nil, nil, err
		}
	}
	opts := metav1.DeleteOptions{PropagationPolicy: &propagation, Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &rv}}
	if dryRun {
		opts.DryRun = []string{metav1.DryRunAll}
	}
	auditRequest := (&http.Request{Method: http.MethodDelete, URL: &url.URL{Path: "/mcp"}}).WithContext(ctx)
	outcome := "accepted"
	if dryRun {
		outcome = "preview"
	}
	err = client.Delete(ctx, name, opts)
	if err != nil {
		outcome = "failed"
	}
	auth.AuditLogAction(auditRequest, auth.AuditActionDetails{Action: "delete_resource", Namespace: namespace, Name: name, Source: "mcp", Outcome: outcome})
	if err != nil {
		if apierrors.IsConflict(err) {
			return nil, nil, fmt.Errorf("failed to delete resource: %w; the object changed, run dry_run=true again", err)
		}
		return nil, nil, fmt.Errorf("failed to delete resource: %w", err)
	}
	result := map[string]any{"status": "ok", "dry_run": dryRun, "kind": obj.GetKind(), "group": gvr.Group, "namespace": namespace, "name": name, "uid": uid, "resourceVersion": rv, "propagation": propagation}
	if dryRun {
		token, err := issueMutationConfirmation(ctx, "delete_resource", target)
		if err != nil {
			return nil, nil, err
		}
		result["confirm"] = token
		result["finalizers"] = obj.GetFinalizers()
		result["cascade"] = deleteCascadePreview(ctx, obj, propagation)
		result["message"] = "Deletion preview validated by the API server; no deletion was persisted. Review the cascade limits before confirming."
		if len(obj.GetFinalizers()) > 0 {
			result["finalizerGuidance"] = "Finalizers may keep this object deleting while controllers finish cleanup. This tool never removes finalizers. Inspect cleanup before using patch_resource as a separate, explicit step; removing finalizers can orphan resources."
		}
		if gvr.Group == "" && gvr.Resource == "namespaces" {
			finalizers, _, _ := unstructured.NestedStringSlice(obj.Object, "spec", "finalizers")
			result["namespaceFinalizers"] = finalizers
			if len(finalizers) > 0 {
				result["namespaceFinalizerGuidance"] = "Namespace lifecycle finalizers may keep this Namespace deleting until its contents are cleaned up. Inspect status.conditions and remaining contents; this tool does not clear namespace lifecycle finalizers."
			}
		}
	} else {
		result["message"] = "Deletion accepted; completion is asynchronous."
		result["observation"] = k8score.ObserveResourceDeletion(ctx, client, name, uid)
	}
	return toJSONResult(result)
}

func deleteCascadePreview(ctx context.Context, obj *unstructured.Unstructured, propagation metav1.DeletionPropagation) map[string]any {
	dependents := []topology.ResourceRef{}
	result := map[string]any{"approximation": true, "dependents": dependents, "coverage": "unknown", "notEnumerated": []string{"namespace contents", "CRD instances", "controller-finalizer cleanup"}}
	switch {
	case obj.GetKind() == "Namespace" && obj.GetAPIVersion() == "v1":
		result["scopeWarning"] = "Deleting this Namespace deletes ALL namespace contents, including resources not shown here. Contents are not enumerated; their count is unknown. Orphan propagation does not prevent namespace cleanup."
	case obj.GetKind() == "CustomResourceDefinition" && obj.GetAPIVersion() == "apiextensions.k8s.io/v1":
		result["scopeWarning"] = "Deleting this CRD deletes ALL instances of the CRD across the cluster. Instances are not enumerated; their count is unknown. Orphan propagation does not preserve CRD instances."
	}
	if propagation == metav1.DeletePropagationOrphan {
		result["propagationEffect"] = "Owner-reference garbage collection will orphan dependents. Namespace, CRD, and controller-finalizer cleanup can still delete resources."
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		result["reason"] = "Topology cache is unavailable; no dependents were enumerated."
		return result
	}
	dp := k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery())
	opts := topology.DefaultBuildOptions()
	opts.IncludeReplicaSets = true
	opts.ForRelationshipCache = true
	topo, err := topology.NewBuilder(k8s.NewTopologyResourceProvider(cache)).WithDynamic(dp).Build(opts)
	if err != nil {
		result["reason"] = "Topology could not be built; no dependents were enumerated."
		return result
	}
	preview := topology.GetCascadeDeletePreview(topology.ResourceRef{Kind: obj.GetKind(), Group: resourceid.GroupFromAPIVersion(obj.GetAPIVersion()), Namespace: obj.GetNamespace(), Name: obj.GetName()}, topo, dp)
	result["rootResolved"] = preview.RootResolved
	if !preview.RootResolved {
		result["reason"] = "The deletion target is not represented in cached topology; dependents were not enumerated."
		return result
	}
	result["coverage"] = "partial"
	result["reason"] = "Cached topology management edges approximate possible dependents; absence from this list does not prove no cascade."
	withheld := 0
	for _, ref := range preview.Dependents {
		gvr, _, err := resolveMutationGVR(ref.Kind, ref.Group)
		if err != nil || !canReadInNamespace(ctx, gvr.Group, gvr.Resource, ref.Namespace, "get") {
			withheld++
			continue
		}
		if len(dependents) < 100 {
			dependents = append(dependents, ref)
		} else {
			result["truncated"] = true
		}
	}
	result["dependents"] = dependents
	if withheld > 0 {
		result["withheld"] = withheld
	}
	return result
}
