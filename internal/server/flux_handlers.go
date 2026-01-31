package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/internal/k8s"
)

// FluxCD API groups and versions
var (
	fluxGitRepoGVR = schema.GroupVersionResource{
		Group:    "source.toolkit.fluxcd.io",
		Version:  "v1",
		Resource: "gitrepositories",
	}
	fluxOCIRepoGVR = schema.GroupVersionResource{
		Group:    "source.toolkit.fluxcd.io",
		Version:  "v1",
		Resource: "ocirepositories",
	}
	fluxHelmRepoGVR = schema.GroupVersionResource{
		Group:    "source.toolkit.fluxcd.io",
		Version:  "v1",
		Resource: "helmrepositories",
	}
	fluxKustomizeGVR = schema.GroupVersionResource{
		Group:    "kustomize.toolkit.fluxcd.io",
		Version:  "v1",
		Resource: "kustomizations",
	}
	fluxHelmReleaseGVR = schema.GroupVersionResource{
		Group:    "helm.toolkit.fluxcd.io",
		Version:  "v2",
		Resource: "helmreleases",
	}
	fluxAlertGVR = schema.GroupVersionResource{
		Group:    "notification.toolkit.fluxcd.io",
		Version:  "v1beta3",
		Resource: "alerts",
	}
)

// getFluxGVR returns the appropriate GVR for a Flux resource kind
func getFluxGVR(kind string) (schema.GroupVersionResource, error) {
	switch strings.ToLower(kind) {
	case "gitrepository", "gitrepositories":
		return fluxGitRepoGVR, nil
	case "ocirepository", "ocirepositories":
		return fluxOCIRepoGVR, nil
	case "helmrepository", "helmrepositories":
		return fluxHelmRepoGVR, nil
	case "kustomization", "kustomizations":
		return fluxKustomizeGVR, nil
	case "helmrelease", "helmreleases":
		return fluxHelmReleaseGVR, nil
	case "alert", "alerts":
		return fluxAlertGVR, nil
	default:
		return schema.GroupVersionResource{}, fmt.Errorf("unknown Flux resource kind: %s", kind)
	}
}

// handleFluxReconcile triggers a reconciliation by setting the reconcile annotation
func (s *Server) handleFluxReconcile(w http.ResponseWriter, r *http.Request) {
	kind := chi.URLParam(r, "kind")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	gvr, err := getFluxGVR(kind)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Patch the reconcile annotation to trigger reconciliation
	// This is the standard FluxCD pattern for triggering a sync
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
		log.Printf("[flux] Failed to marshal reconcile patch for %s %s/%s: %v", kind, namespace, name, err)
		s.writeError(w, http.StatusInternalServerError, "failed to create patch")
		return
	}

	client := k8s.GetDynamicClient()
	if client == nil {
		log.Printf("[flux] Dynamic client unavailable for %s %s/%s", kind, namespace, name)
		s.writeError(w, http.StatusServiceUnavailable, "dynamic client not available")
		return
	}

	_, err = client.Resource(gvr).Namespace(namespace).Patch(
		r.Context(),
		name,
		types.MergePatchType,
		patchBytes,
		metav1.PatchOptions{},
	)
	if err != nil {
		if apierrors.IsNotFound(err) {
			s.writeError(w, http.StatusNotFound, err.Error())
			return
		}
		log.Printf("[flux] Failed to reconcile %s %s/%s: %v", kind, namespace, name, err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, GitOpsOperationResponse{
		Message:     "Reconciliation triggered",
		Operation:   "reconcile",
		Tool:        "fluxcd",
		Resource:    GitOpsResourceRef{Kind: formatFluxKind(kind), Name: name, Namespace: namespace},
		RequestedAt: timestamp,
	})
}

// handleFluxSuspend suspends a Flux resource by setting spec.suspend=true
func (s *Server) handleFluxSuspend(w http.ResponseWriter, r *http.Request) {
	s.setFluxSuspend(w, r, true)
}

// handleFluxResume resumes a suspended Flux resource by setting spec.suspend=false
func (s *Server) handleFluxResume(w http.ResponseWriter, r *http.Request) {
	s.setFluxSuspend(w, r, false)
}

// setFluxSuspend is a helper that sets the spec.suspend field
func (s *Server) setFluxSuspend(w http.ResponseWriter, r *http.Request, suspend bool) {
	kind := chi.URLParam(r, "kind")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	gvr, err := getFluxGVR(kind)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	patch := map[string]any{
		"spec": map[string]any{
			"suspend": suspend,
		},
	}

	action := "suspend"
	if !suspend {
		action = "resume"
	}

	patchBytes, err := json.Marshal(patch)
	if err != nil {
		log.Printf("[flux] Failed to marshal %s patch for %s %s/%s: %v", action, kind, namespace, name, err)
		s.writeError(w, http.StatusInternalServerError, "failed to create patch")
		return
	}

	client := k8s.GetDynamicClient()
	if client == nil {
		log.Printf("[flux] Dynamic client unavailable for %s %s/%s", kind, namespace, name)
		s.writeError(w, http.StatusServiceUnavailable, "dynamic client not available")
		return
	}

	_, err = client.Resource(gvr).Namespace(namespace).Patch(
		r.Context(),
		name,
		types.MergePatchType,
		patchBytes,
		metav1.PatchOptions{},
	)
	if err != nil {
		if apierrors.IsNotFound(err) {
			s.writeError(w, http.StatusNotFound, err.Error())
			return
		}
		log.Printf("[flux] Failed to %s %s %s/%s: %v", action, kind, namespace, name, err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	actionPast := "suspended"
	operation := "suspend"
	if !suspend {
		actionPast = "resumed"
		operation = "resume"
	}

	s.writeJSON(w, GitOpsOperationResponse{
		Message:   fmt.Sprintf("%s %s", formatFluxKind(kind), actionPast),
		Operation: operation,
		Tool:      "fluxcd",
		Resource:  GitOpsResourceRef{Kind: formatFluxKind(kind), Name: name, Namespace: namespace},
	})
}

// formatFluxKind returns a human-readable name for a Flux kind
func formatFluxKind(kind string) string {
	switch strings.ToLower(kind) {
	case "gitrepository", "gitrepositories":
		return "GitRepository"
	case "ocirepository", "ocirepositories":
		return "OCIRepository"
	case "helmrepository", "helmrepositories":
		return "HelmRepository"
	case "kustomization", "kustomizations":
		return "Kustomization"
	case "helmrelease", "helmreleases":
		return "HelmRelease"
	case "alert", "alerts":
		return "Alert"
	default:
		return kind
	}
}

// handleFluxSyncWithSource reconciles the source first, then the resource
// This is useful for Kustomizations and HelmReleases that depend on a source
func (s *Server) handleFluxSyncWithSource(w http.ResponseWriter, r *http.Request) {
	kind := chi.URLParam(r, "kind")
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	gvr, err := getFluxGVR(kind)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	client := k8s.GetDynamicClient()
	if client == nil {
		log.Printf("[flux] Dynamic client unavailable for %s %s/%s", kind, namespace, name)
		s.writeError(w, http.StatusServiceUnavailable, "dynamic client not available")
		return
	}

	// Get the resource to extract sourceRef
	resource, err := client.Resource(gvr).Namespace(namespace).Get(r.Context(), name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			s.writeError(w, http.StatusNotFound, err.Error())
			return
		}
		log.Printf("[flux] Failed to get %s %s/%s for sync-with-source: %v", kind, namespace, name, err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Extract sourceRef based on kind
	var sourceKind, sourceName, sourceNamespace string
	spec, ok := resource.Object["spec"].(map[string]any)
	if !ok {
		log.Printf("[flux] Invalid spec for %s %s/%s: expected map[string]any", kind, namespace, name)
		s.writeError(w, http.StatusInternalServerError, "invalid resource spec")
		return
	}

	switch strings.ToLower(kind) {
	case "kustomization", "kustomizations":
		// Kustomization: spec.sourceRef
		if sourceRef, ok := spec["sourceRef"].(map[string]any); ok {
			sourceKind, _ = sourceRef["kind"].(string)
			sourceName, _ = sourceRef["name"].(string)
			sourceNamespace, _ = sourceRef["namespace"].(string)
		}
	case "helmrelease", "helmreleases":
		// HelmRelease: spec.chart.spec.sourceRef
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
		s.writeError(w, http.StatusBadRequest, "sync-with-source only supported for Kustomization and HelmRelease")
		return
	}

	if sourceName == "" {
		s.writeError(w, http.StatusBadRequest, "no source reference found in resource")
		return
	}

	// Default sourceNamespace to resource namespace
	if sourceNamespace == "" {
		sourceNamespace = namespace
	}

	// Get the GVR for the source
	sourceGVR, err := getFluxGVR(sourceKind)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown source kind: %s", sourceKind))
		return
	}

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
		log.Printf("[flux] Failed to marshal sync-with-source patch for %s %s/%s: %v", kind, namespace, name, err)
		s.writeError(w, http.StatusInternalServerError, "failed to create patch")
		return
	}

	// First, reconcile the source
	_, err = client.Resource(sourceGVR).Namespace(sourceNamespace).Patch(
		r.Context(),
		sourceName,
		types.MergePatchType,
		patchBytes,
		metav1.PatchOptions{},
	)
	if err != nil {
		log.Printf("[flux] Failed to reconcile source %s %s/%s: %v", sourceKind, sourceNamespace, sourceName, err)
		s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to reconcile source: %v", err))
		return
	}

	// Then, reconcile the resource itself
	_, err = client.Resource(gvr).Namespace(namespace).Patch(
		r.Context(),
		name,
		types.MergePatchType,
		patchBytes,
		metav1.PatchOptions{},
	)
	if err != nil {
		log.Printf("[flux] Partial sync-with-source: source %s/%s reconciled, but %s %s/%s failed: %v",
			sourceNamespace, sourceName, kind, namespace, name, err)
		s.writeError(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to reconcile resource (note: source %s/%s was reconciled): %v", sourceName, sourceNamespace, err))
		return
	}

	s.writeJSON(w, GitOpsOperationResponse{
		Message:     "Sync with source triggered",
		Operation:   "reconcile",
		Tool:        "fluxcd",
		Resource:    GitOpsResourceRef{Kind: formatFluxKind(kind), Name: name, Namespace: namespace},
		RequestedAt: timestamp,
		Source:      &GitOpsResourceRef{Kind: formatFluxKind(sourceKind), Name: sourceName, Namespace: sourceNamespace},
	})
}
