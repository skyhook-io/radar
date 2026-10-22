package images

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// Handlers provides HTTP handlers for image inspection
type Handlers struct {
	inspector interface {
		GetMetadata(context.Context, InspectRequest) (*ImageMetadata, error)
		Inspect(context.Context, InspectRequest) (*ImageFilesystem, error)
		GetFileContent(context.Context, InspectRequest, string) ([]byte, string, error)
	}
	authorize func(*http.Request, string) (allowed, authoritative bool)
}

// NewHandlers creates a new Handlers instance
func NewHandlers(authorize func(*http.Request, string) (allowed, authoritative bool)) *Handlers {
	return &Handlers{
		inspector: NewInspector(),
		authorize: authorize,
	}
}

// RegisterRoutes registers image inspection routes
func (h *Handlers) RegisterRoutes(r chi.Router) {
	r.Route("/images", func(r chi.Router) {
		r.Get("/metadata", h.handleMetadata)
		r.Get("/inspect", h.handleInspect)
		r.Get("/file", h.handleGetFile)
	})
}

// handleMetadata returns lightweight metadata about an image
// If the image is already cached, returns the full filesystem
func (h *Handlers) handleMetadata(w http.ResponseWriter, r *http.Request) {
	image := r.URL.Query().Get("image")
	if image == "" {
		writeError(w, http.StatusBadRequest, "image parameter is required")
		return
	}

	req, ok := h.inspectionRequest(w, r, image)
	if !ok {
		return
	}

	result, err := h.inspector.GetMetadata(r.Context(), req)
	if err != nil {
		errLower := strings.ToLower(err.Error())
		if strings.Contains(errLower, "unauthorized") || strings.Contains(errLower, "denied") {
			writeAuthError(w, image)
			return
		}
		if strings.Contains(errLower, "not found") || strings.Contains(errLower, "manifest unknown") {
			writeError(w, http.StatusNotFound, "Image not found: "+image)
			return
		}
		writeError(w, http.StatusInternalServerError, "Failed to inspect image")
		return
	}

	writeJSON(w, result)
}

// handleInspect inspects an image and returns its filesystem tree
func (h *Handlers) handleInspect(w http.ResponseWriter, r *http.Request) {
	image := r.URL.Query().Get("image")
	if image == "" {
		writeError(w, http.StatusBadRequest, "image parameter is required")
		return
	}

	req, ok := h.inspectionRequest(w, r, image)
	if !ok {
		return
	}

	result, err := h.inspector.Inspect(r.Context(), req)
	if err != nil {
		// Check for common errors
		errLower := strings.ToLower(err.Error())
		if strings.Contains(errLower, "unauthorized") || strings.Contains(errLower, "denied") {
			writeAuthError(w, image)
			return
		}
		if strings.Contains(errLower, "not found") || strings.Contains(errLower, "manifest unknown") {
			writeError(w, http.StatusNotFound, "Image not found: "+image)
			return
		}
		writeError(w, http.StatusInternalServerError, "Failed to inspect image")
		return
	}

	writeJSON(w, result)
}

// handleGetFile returns the content of a specific file from an image
func (h *Handlers) handleGetFile(w http.ResponseWriter, r *http.Request) {
	image := r.URL.Query().Get("image")
	if image == "" {
		writeError(w, http.StatusBadRequest, "image parameter is required")
		return
	}

	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		writeError(w, http.StatusBadRequest, "path parameter is required")
		return
	}

	req, ok := h.inspectionRequest(w, r, image)
	if !ok {
		return
	}

	content, filename, err := h.inspector.GetFileContent(r.Context(), req, filePath)
	if err != nil {
		errLower := strings.ToLower(err.Error())
		if strings.Contains(errLower, "unauthorized") || strings.Contains(errLower, "denied") {
			writeAuthError(w, image)
			return
		}
		if strings.Contains(errLower, "not found") {
			writeError(w, http.StatusNotFound, "File not found: "+filePath)
			return
		}
		writeError(w, http.StatusInternalServerError, "Failed to read image file")
		return
	}

	// Set headers for file download
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
	if _, err := w.Write(content); err != nil {
		log.Printf("[images] Failed to write file content: %v", err)
	}
}

// inspectionRequest binds authenticated inspection to a readable Pod before any
// registry credentials or shared image-cache contents can be accessed.
func (h *Handlers) inspectionRequest(w http.ResponseWriter, r *http.Request, image string) (InspectRequest, bool) {
	req := InspectRequest{
		Image:     image,
		Namespace: r.URL.Query().Get("namespace"),
		PodName:   r.URL.Query().Get("pod"),
	}
	if auth.UserFromContext(r.Context()) == nil {
		if pullSecrets := r.URL.Query().Get("pullSecrets"); pullSecrets != "" {
			req.PullSecretNames = strings.Split(pullSecrets, ",")
		}
		if req.PodName != "" && req.Namespace != "" && len(req.PullSecretNames) == 0 {
			req.PullSecretNames = GetPullSecretsFromPod(req.Namespace, req.PodName)
		}
		return req, true
	}

	if req.Namespace == "" || req.PodName == "" {
		writeError(w, http.StatusBadRequest, "namespace and pod parameters are required for authenticated image inspection")
		return req, false
	}
	if h.authorize == nil {
		writeError(w, http.StatusServiceUnavailable, "Pod authorization is unavailable")
		return req, false
	}
	allowed, authoritative := h.authorize(r, req.Namespace)
	if !authoritative {
		writeError(w, http.StatusServiceUnavailable, "Could not authorize get pods in namespace "+req.Namespace)
		return req, false
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "rbac_denied: requires get pods in namespace "+req.Namespace)
		return req, false
	}
	cache := k8s.GetResourceCache()
	if cache == nil || cache.Pods() == nil {
		writeError(w, http.StatusServiceUnavailable, "Pod cache is unavailable")
		return req, false
	}
	pod, err := cache.Pods().Pods(req.Namespace).Get(req.PodName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			writeError(w, http.StatusNotFound, "Pod not found: "+req.Namespace+"/"+req.PodName)
		} else {
			writeError(w, http.StatusServiceUnavailable, "Could not read Pod")
		}
		return req, false
	}
	if !podUsesImage(pod, image) {
		writeError(w, http.StatusForbidden, "image is not used by pod "+req.Namespace+"/"+req.PodName)
		return req, false
	}
	req.keychain = getPodKeychain(req.Namespace, pod)
	return req, true
}

func podUsesImage(pod *corev1.Pod, image string) bool {
	for _, containers := range [][]corev1.Container{pod.Spec.InitContainers, pod.Spec.Containers} {
		for _, container := range containers {
			if container.Image == image {
				return true
			}
		}
	}
	for _, container := range pod.Spec.EphemeralContainers {
		if container.Image == image {
			return true
		}
	}
	for _, statuses := range [][]corev1.ContainerStatus{pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses, pod.Status.EphemeralContainerStatuses} {
		for _, status := range statuses {
			imageID := status.ImageID
			if _, ref, ok := strings.Cut(imageID, "://"); ok {
				imageID = ref
			}
			if strings.Contains(imageID, "@sha256:") && imageID == image {
				return true
			}
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("[images] Failed to encode JSON response: %v", err)
	}
}

func writeAuthError(w http.ResponseWriter, image string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	if err := json.NewEncoder(w).Encode(map[string]string{
		"error":        "Authentication required for this image",
		"registryType": string(DetectRegistryType(image)),
	}); err != nil {
		log.Printf("[images] Failed to encode auth error response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]string{"error": message}); err != nil {
		log.Printf("[images] Failed to encode error response: %v", err)
	}
}
