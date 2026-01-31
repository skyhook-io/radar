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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/internal/k8s"
)

// ArgoCD Application GVR - all handlers operate on Applications
var argoApplicationGVR = schema.GroupVersionResource{
	Group:    "argoproj.io",
	Version:  "v1alpha1",
	Resource: "applications",
}

// handleArgoSync triggers a sync operation on an ArgoCD Application
// This sets the operation field to initiate a sync
func (s *Server) handleArgoSync(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	client := k8s.GetDynamicClient()
	if client == nil {
		log.Printf("[argo] Dynamic client unavailable for sync Application %s/%s", namespace, name)
		s.writeError(w, http.StatusServiceUnavailable, "dynamic client not available")
		return
	}

	// First, get the current application to check its state
	app, err := client.Resource(argoApplicationGVR).Namespace(namespace).Get(
		r.Context(),
		name,
		metav1.GetOptions{},
	)
	if err != nil {
		if apierrors.IsNotFound(err) {
			s.writeError(w, http.StatusNotFound, err.Error())
			return
		}
		log.Printf("[argo] Failed to get application %s/%s: %v", namespace, name, err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Check if there's already an operation in progress
	phase, found, _ := unstructured.NestedString(app.Object, "status", "operationState", "phase")
	if found {
		if phase == "Running" {
			s.writeError(w, http.StatusConflict, "sync operation already in progress")
			return
		}
	}

	// ArgoCD sync is triggered by setting the operation field
	// The argocd-application-controller watches for this and performs the sync
	timestamp := time.Now().Format(time.RFC3339Nano)

	// We use a simpler approach: set the refresh annotation to trigger a sync
	// This is similar to running `argocd app sync`
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
				"revision": "", // Empty means use the target revision from spec
				"prune":    true,
			},
		},
	}

	patchBytes, err := json.Marshal(patch)
	if err != nil {
		log.Printf("[argo] Failed to marshal sync patch for %s/%s: %v", namespace, name, err)
		s.writeError(w, http.StatusInternalServerError, "failed to create patch")
		return
	}

	_, err = client.Resource(argoApplicationGVR).Namespace(namespace).Patch(
		r.Context(),
		name,
		types.MergePatchType,
		patchBytes,
		metav1.PatchOptions{},
	)
	if err != nil {
		log.Printf("[argo] Failed to sync application %s/%s: %v", namespace, name, err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, GitOpsOperationResponse{
		Message:     "Sync operation initiated",
		Operation:   "sync",
		Tool:        "argocd",
		Resource:    GitOpsResourceRef{Kind: "Application", Name: name, Namespace: namespace},
		RequestedAt: timestamp,
	})
}

// handleArgoRefresh triggers a refresh (re-read from git) on an ArgoCD Application
// This is a lighter operation than sync - it just refreshes the app status
func (s *Server) handleArgoRefresh(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	// Get refresh type from query param (default: normal, can be "hard")
	refreshType := r.URL.Query().Get("type")
	if refreshType == "" {
		refreshType = "normal"
	} else if refreshType != "normal" && refreshType != "hard" {
		s.writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid refresh type %q: must be 'normal' or 'hard'", refreshType))
		return
	}

	client := k8s.GetDynamicClient()
	if client == nil {
		log.Printf("[argo] Dynamic client unavailable for refresh Application %s/%s", namespace, name)
		s.writeError(w, http.StatusServiceUnavailable, "dynamic client not available")
		return
	}

	timestamp := time.Now().Format(time.RFC3339Nano)

	// ArgoCD refresh is triggered by setting the refresh annotation
	patch := map[string]any{
		"metadata": map[string]any{
			"annotations": map[string]string{
				"argocd.argoproj.io/refresh": refreshType,
			},
		},
	}

	patchBytes, err := json.Marshal(patch)
	if err != nil {
		log.Printf("[argo] Failed to marshal refresh patch for %s/%s: %v", namespace, name, err)
		s.writeError(w, http.StatusInternalServerError, "failed to create patch")
		return
	}

	_, err = client.Resource(argoApplicationGVR).Namespace(namespace).Patch(
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
		log.Printf("[argo] Failed to refresh application %s/%s: %v", namespace, name, err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, GitOpsOperationResponse{
		Message:     fmt.Sprintf("Refresh (%s) triggered", refreshType),
		Operation:   "refresh",
		Tool:        "argocd",
		Resource:    GitOpsResourceRef{Kind: "Application", Name: name, Namespace: namespace},
		RequestedAt: timestamp,
	})
}

// handleArgoTerminate terminates an ongoing sync operation
func (s *Server) handleArgoTerminate(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	client := k8s.GetDynamicClient()
	if client == nil {
		log.Printf("[argo] Dynamic client unavailable for terminate Application %s/%s", namespace, name)
		s.writeError(w, http.StatusServiceUnavailable, "dynamic client not available")
		return
	}

	// First, check if there's an operation in progress
	app, err := client.Resource(argoApplicationGVR).Namespace(namespace).Get(
		r.Context(),
		name,
		metav1.GetOptions{},
	)
	if err != nil {
		if apierrors.IsNotFound(err) {
			s.writeError(w, http.StatusNotFound, err.Error())
			return
		}
		log.Printf("[argo] Failed to get application %s/%s for terminate: %v", namespace, name, err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Check the operation state
	phase, found, _ := unstructured.NestedString(app.Object, "status", "operationState", "phase")
	if !found || phase != "Running" {
		s.writeError(w, http.StatusBadRequest, "no sync operation in progress")
		return
	}

	// Terminate by removing the operation field - ArgoCD will cancel it
	// Actually, ArgoCD termination is done by setting operation to nil
	// We use a JSON patch to remove the operation field
	patchBytes := []byte(`[{"op": "remove", "path": "/operation"}]`)

	_, err = client.Resource(argoApplicationGVR).Namespace(namespace).Patch(
		r.Context(),
		name,
		types.JSONPatchType,
		patchBytes,
		metav1.PatchOptions{},
	)
	if err != nil {
		// If the operation field doesn't exist, the operation may have already completed
		if strings.Contains(err.Error(), "nonexistent") {
			log.Printf("[argo] Terminate: operation field already removed for %s/%s (may have completed)", namespace, name)
			// Return informative response - the operation wasn't terminated by us
			s.writeJSON(w, GitOpsOperationResponse{
				Message:   "No operation to terminate (may have already completed)",
				Operation: "terminate",
				Tool:      "argocd",
				Resource:  GitOpsResourceRef{Kind: "Application", Name: name, Namespace: namespace},
			})
			return
		}
		log.Printf("[argo] Failed to terminate operation for %s/%s: %v", namespace, name, err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.writeJSON(w, GitOpsOperationResponse{
		Message:   "Sync operation terminated",
		Operation: "terminate",
		Tool:      "argocd",
		Resource:  GitOpsResourceRef{Kind: "Application", Name: name, Namespace: namespace},
	})
}

// handleArgoSuspend disables automated sync on an ArgoCD Application
// ArgoCD doesn't have a direct suspend like Flux, but we can disable automated sync
func (s *Server) handleArgoSuspend(w http.ResponseWriter, r *http.Request) {
	s.setArgoAutomatedSync(w, r, false)
}

// handleArgoResume re-enables automated sync on an ArgoCD Application
func (s *Server) handleArgoResume(w http.ResponseWriter, r *http.Request) {
	s.setArgoAutomatedSync(w, r, true)
}

// setArgoAutomatedSync enables or disables automated sync policy
func (s *Server) setArgoAutomatedSync(w http.ResponseWriter, r *http.Request, enable bool) {
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	action := "suspend"
	if enable {
		action = "resume"
	}

	client := k8s.GetDynamicClient()
	if client == nil {
		log.Printf("[argo] Dynamic client unavailable for %s Application %s/%s", action, namespace, name)
		s.writeError(w, http.StatusServiceUnavailable, "dynamic client not available")
		return
	}

	// Get current application to check existing sync policy
	app, err := client.Resource(argoApplicationGVR).Namespace(namespace).Get(
		r.Context(),
		name,
		metav1.GetOptions{},
	)
	if err != nil {
		if apierrors.IsNotFound(err) {
			s.writeError(w, http.StatusNotFound, err.Error())
			return
		}
		log.Printf("[argo] Failed to get application %s/%s for %s: %v", namespace, name, action, err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var patch map[string]any

	if enable {
		// Re-enable automated sync
		// Get the existing prune and selfHeal settings if they exist
		prune := true
		selfHeal := true

		// Try to get existing settings from annotations (we store them when suspending)
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
					"radar.skyhook.io/suspended-prune":    nil, // Remove
					"radar.skyhook.io/suspended-selfheal": nil, // Remove
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
		// Disable automated sync (suspend)
		// First, save current automated settings to annotations for later restore
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
					"automated": nil, // Remove automated sync
				},
			},
		}
	}

	patchBytes, err := json.Marshal(patch)
	if err != nil {
		log.Printf("[argo] Failed to marshal %s patch for %s/%s: %v", action, namespace, name, err)
		s.writeError(w, http.StatusInternalServerError, "failed to create patch")
		return
	}

	_, err = client.Resource(argoApplicationGVR).Namespace(namespace).Patch(
		r.Context(),
		name,
		types.MergePatchType,
		patchBytes,
		metav1.PatchOptions{},
	)
	if err != nil {
		log.Printf("[argo] Failed to %s application %s/%s: %v", action, namespace, name, err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	actionPast := "suspended"
	operation := "suspend"
	if enable {
		actionPast = "resumed"
		operation = "resume"
	}

	s.writeJSON(w, GitOpsOperationResponse{
		Message:   fmt.Sprintf("Application %s (automated sync %s)", actionPast, actionPast),
		Operation: operation,
		Tool:      "argocd",
		Resource:  GitOpsResourceRef{Kind: "Application", Name: name, Namespace: namespace},
	})
}
