package images

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/go-containerregistry/pkg/name"
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
	authorize       func(*http.Request, string) (allowed, authoritative bool)
	authorizeExec   func(*http.Request, string) (allowed, authoritative bool)
	authorizeSecret func(*http.Request, string, string) (allowed, authoritative bool)
}

// NewHandlers creates a new Handlers instance
func NewHandlers(authorize, authorizeExec func(*http.Request, string) (allowed, authoritative bool), authorizeSecret func(*http.Request, string, string) (allowed, authoritative bool)) *Handlers {
	return &Handlers{
		inspector:       NewInspector(),
		authorize:       authorize,
		authorizeExec:   authorizeExec,
		authorizeSecret: authorizeSecret,
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
		if isRefused(err) {
			writeBlockedError(w, err)
			return
		}
		if writeFetchError(w, req, err) {
			return
		}
		errLower := strings.ToLower(err.Error())
		if strings.Contains(errLower, "unauthorized") || strings.Contains(errLower, "denied") {
			writeAuthError(w, image)
			return
		}
		if strings.Contains(errLower, "not found") || strings.Contains(errLower, "manifest unknown") {
			writeError(w, http.StatusNotFound, "Image not found: "+image)
			return
		}
		log.Printf("[images] Failed to get image metadata %s/%s: %v", k8s.SanitizeForLog(req.Namespace), k8s.SanitizeForLog(req.Image), k8s.SanitizeForLog(err.Error()))
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
		if isRefused(err) {
			writeBlockedError(w, err)
			return
		}
		if writeFetchError(w, req, err) {
			return
		}
		errLower := strings.ToLower(err.Error())
		if strings.Contains(errLower, "unauthorized") || strings.Contains(errLower, "denied") {
			writeAuthError(w, image)
			return
		}
		if strings.Contains(errLower, "not found") || strings.Contains(errLower, "manifest unknown") {
			writeError(w, http.StatusNotFound, "Image not found: "+image)
			return
		}
		log.Printf("[images] Failed to inspect image %s/%s: %v", k8s.SanitizeForLog(req.Namespace), k8s.SanitizeForLog(req.Image), k8s.SanitizeForLog(err.Error()))
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
		if isRefused(err) {
			writeBlockedError(w, err)
			return
		}
		if writeFetchError(w, req, err) {
			return
		}
		errLower := strings.ToLower(err.Error())
		if strings.Contains(errLower, "unauthorized") || strings.Contains(errLower, "denied") {
			writeAuthError(w, image)
			return
		}
		if strings.Contains(errLower, "not found") {
			writeError(w, http.StatusNotFound, "File not found: "+filePath)
			return
		}
		log.Printf("[images] Failed to read image file %s/%s: %v", k8s.SanitizeForLog(req.Namespace), k8s.SanitizeForLog(req.Image), k8s.SanitizeForLog(err.Error()))
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

// inspectionRequest limits authenticated inspection to an image a readable Pod
// runs, with only the credentials that caller may use.
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
		if pod := cachedPod(req.Namespace, req.PodName); pod != nil {
			if len(req.PullSecretNames) == 0 {
				req.PullSecretNames = podPullSecrets(pod)
			}
			if c, ok := findContainerImage(pod, image); ok {
				if c.pinned != "" {
					req.Image = c.pinned
				}
				req.nodeOnly = c.nodeOnly
			}
		}
		return req, true
	}

	// Without a Pod the caller could name any host and have Radar connect to
	// it from inside the cluster network. Binding every authenticated request
	// to a Pod they can read limits the registry host to that Pod's image.
	if req.PodName == "" || req.Namespace == "" {
		writeError(w, http.StatusBadRequest, "pod and namespace are required when authentication is enabled")
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
	cache, readiness := k8s.ReadableCacheForKind("pods")
	if cache == nil {
		writeError(w, http.StatusServiceUnavailable, "Pod cache is unavailable")
		return req, false
	}
	switch readiness {
	case k8s.KindPending:
		writeError(w, http.StatusServiceUnavailable, "Pod cache is still syncing")
		return req, false
	case k8s.KindFailed:
		writeError(w, http.StatusServiceUnavailable, "Pod cache sync is overdue; retry later")
		return req, false
	case k8s.KindUnavailable:
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
	c, ok := findContainerImage(pod, image)
	if !ok {
		writeError(w, http.StatusForbidden, "image is not used by pod "+req.Namespace+"/"+req.PodName)
		return req, false
	}
	req.nodeOnly = c.nodeOnly
	// Exec holders may also use Radar's own registry credentials, but only for
	// the digest the kubelet already pulled for this container: a tag can move,
	// and a Pod that never pulled its image proves nothing.
	useAmbient := false
	if c.pinned != "" && h.authorizeExec != nil {
		if allowed, authoritative := h.authorizeExec(r, req.Namespace); allowed && authoritative {
			req.Image, useAmbient = c.pinned, true
		}
	}
	req.guarded = true
	req.keychain = getPodKeychain(req.Namespace, pod, useAmbient, func(secretName string) bool {
		if h.authorizeSecret == nil {
			return false
		}
		allowed, authoritative := h.authorizeSecret(r, req.Namespace, secretName)
		return allowed && authoritative
	})
	return req, true
}

// containerImage is what one of a Pod's containers runs.
type containerImage struct {
	// pinned is the reference the kubelet recorded as pulled for this image,
	// or "" when there is none Radar can trust to name it.
	pinned string
	// nodeOnly is set when the node holds the image without having pulled it
	// from a registry, as after kind load or ctr images import.
	nodeOnly bool
}

// findContainerImage finds the containers running image, named by their spec
// image or the reference the kubelet recorded.
func findContainerImage(pod *corev1.Pod, image string) (containerImage, bool) {
	specImages := map[string]string{}
	for _, containers := range [][]corev1.Container{pod.Spec.InitContainers, pod.Spec.Containers} {
		for _, container := range containers {
			specImages[container.Name] = container.Image
		}
	}
	for _, container := range pod.Spec.EphemeralContainers {
		specImages[container.Name] = container.Image
	}
	var matches []containerImage
	for _, statuses := range [][]corev1.ContainerStatus{pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses, pod.Status.EphemeralContainerStatuses} {
		for _, status := range statuses {
			spec, ok := specImages[status.Name]
			if !ok || status.ImageID == "" {
				continue
			}
			delete(specImages, status.Name)
			imageID := status.ImageID
			if _, ref, ok := strings.Cut(imageID, "://"); ok {
				imageID = ref
			}
			recorded := strings.Contains(imageID, "@sha256:")
			if spec != image && !(recorded && imageID == image) {
				continue
			}
			match := containerImage{nodeOnly: strings.HasPrefix(imageID, "sha256:") && nodeRecordsPulledDigests(pod.Spec.NodeName)}
			if recorded && sameRepository(spec, imageID) {
				match.pinned = imageID
			}
			matches = append(matches, match)
		}
	}
	if len(matches) > 0 {
		// Containers that pulled the same tag at different times can hold
		// different digests, and the request doesn't say which container it
		// means.
		for _, m := range matches[1:] {
			if m.pinned != matches[0].pinned {
				matches[0].pinned = ""
			}
			matches[0].nodeOnly = matches[0].nodeOnly && m.nodeOnly
		}
		return matches[0], true
	}
	// Containers that haven't pulled their image yet say nothing about it.
	for _, spec := range specImages {
		if spec == image {
			return containerImage{}, true
		}
	}
	return containerImage{}, false
}

// sameRepository reports whether the kubelet's recorded reference is in the
// repository the Pod spec names. A runtime holding one image under several
// names reports the first, which can be another registry's, and its digest
// need not exist in the spec's repository.
func sameRepository(spec, recorded string) bool {
	specRef, err := name.ParseReference(spec)
	if err != nil {
		return false
	}
	recordedRef, err := name.ParseReference(recorded)
	if err != nil {
		return false
	}
	return specRef.Context().Name() == recordedRef.Context().Name()
}

// nodeRecordsPulledDigests reports a node whose runtime always records a
// repository digest for an image it pulls, so an image without one was put
// on the node some other way. CRI-O reports a bare image ID even for pulled
// images, so it doesn't qualify.
func nodeRecordsPulledDigests(nodeName string) bool {
	cache := k8s.GetResourceCache()
	if cache == nil || nodeName == "" {
		return false
	}
	nodes := cache.Nodes()
	if nodes == nil {
		return false
	}
	node, err := nodes.Get(nodeName)
	if err != nil {
		return false
	}
	runtime := node.Status.NodeInfo.ContainerRuntimeVersion
	return strings.HasPrefix(runtime, "containerd://") || strings.HasPrefix(runtime, "docker://")
}

// writeFetchError answers a failure to download the image that Radar can
// explain, and reports whether it did.
func writeFetchError(w http.ResponseWriter, req InspectRequest, err error) bool {
	if isRefused(err) {
		return false
	}
	registry := registryHost(req.Image)
	if isConnectFailure(err) {
		writeJSONStatus(w, http.StatusBadGateway, map[string]string{
			"error":    "Radar couldn't connect while fetching this image from " + registry + ".",
			"reason":   "registry_unreachable",
			"registry": registry,
		})
		return true
	}
	var fetchErr *registryFetchError
	if req.nodeOnly && errors.As(err, &fetchErr) {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{
			"error":    "The node didn't pull this image from a registry, and Radar couldn't get it from " + registry + ".",
			"reason":   "node_only",
			"registry": registry,
		})
		return true
	}
	return false
}

// isConnectFailure reports a connection that was never established: a host
// name that doesn't resolve, or a dial that was refused or timed out.
func isConnectFailure(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "dial"
}

// registryHost names the image's registry as people write it.
func registryHost(image string) string {
	ref, err := name.ParseReference(image)
	if err != nil {
		return image
	}
	if host := ref.Context().RegistryStr(); host != name.DefaultRegistry {
		return host
	}
	return "docker.io"
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

func writeBlockedError(w http.ResponseWriter, err error) {
	if errors.Is(err, errCredentialsOverHTTP) {
		writeError(w, http.StatusForbidden, "This image's registry uses plain HTTP, and Radar only sends registry credentials over HTTPS, so it can't open this private image.")
		return
	}
	writeError(w, http.StatusForbidden, "Radar doesn't connect to this registry address (loopback, link-local, or an internal IP the registry pointed it to), so it can't open this image.")
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSONStatus(w, status, map[string]string{"error": message})
}

func writeJSONStatus(w http.ResponseWriter, status int, body map[string]string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("[images] Failed to encode error response: %v", err)
	}
}
