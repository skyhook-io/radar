package images

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// Handlers provides HTTP handlers for image inspection
type Handlers struct {
	inspector *Inspector
}

// NewHandlers creates a new Handlers instance
func NewHandlers() *Handlers {
	return &Handlers{
		inspector: NewInspector(),
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

	namespace := r.URL.Query().Get("namespace")
	podName := r.URL.Query().Get("pod")
	pullSecrets := r.URL.Query().Get("pullSecrets")

	var secretNames []string
	if pullSecrets != "" {
		secretNames = strings.Split(pullSecrets, ",")
	}

	// If pod name is provided, auto-discover pull secrets from pod spec
	if podName != "" && namespace != "" && len(secretNames) == 0 {
		secretNames = GetPullSecretsFromPod(namespace, podName)
	}

	req := InspectRequest{
		Image:           image,
		Namespace:       namespace,
		PodName:         podName,
		PullSecretNames: secretNames,
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
		writeError(w, http.StatusInternalServerError, err.Error())
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

	namespace := r.URL.Query().Get("namespace")
	podName := r.URL.Query().Get("pod")
	pullSecrets := r.URL.Query().Get("pullSecrets")

	var secretNames []string
	if pullSecrets != "" {
		secretNames = strings.Split(pullSecrets, ",")
	}

	// If pod name is provided, auto-discover pull secrets from pod spec
	if podName != "" && namespace != "" && len(secretNames) == 0 {
		secretNames = GetPullSecretsFromPod(namespace, podName)
	}

	req := InspectRequest{
		Image:           image,
		Namespace:       namespace,
		PodName:         podName,
		PullSecretNames: secretNames,
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
		writeError(w, http.StatusInternalServerError, err.Error())
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

	namespace := r.URL.Query().Get("namespace")
	podName := r.URL.Query().Get("pod")
	pullSecrets := r.URL.Query().Get("pullSecrets")

	var secretNames []string
	if pullSecrets != "" {
		secretNames = strings.Split(pullSecrets, ",")
	}

	// If pod name is provided, auto-discover pull secrets from pod spec
	if podName != "" && namespace != "" && len(secretNames) == 0 {
		secretNames = GetPullSecretsFromPod(namespace, podName)
	}

	req := InspectRequest{
		Image:           image,
		Namespace:       namespace,
		PodName:         podName,
		PullSecretNames: secretNames,
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
		writeError(w, http.StatusInternalServerError, err.Error())
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
