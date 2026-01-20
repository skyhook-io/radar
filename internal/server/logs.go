package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"

	"github.com/skyhook-io/skyhook-explorer/internal/k8s"
)

// LogsResponse is the response for non-streaming logs
type LogsResponse struct {
	PodName    string            `json:"podName"`
	Namespace  string            `json:"namespace"`
	Containers []string          `json:"containers"`
	Logs       map[string]string `json:"logs"` // container -> logs
}

// handlePodLogs fetches logs from a pod (non-streaming)
func (s *Server) handlePodLogs(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	podName := chi.URLParam(r, "name")
	container := r.URL.Query().Get("container")
	previous := r.URL.Query().Get("previous") == "true"
	tailLinesStr := r.URL.Query().Get("tailLines")

	tailLines := int64(500) // default
	if tailLinesStr != "" {
		if t, err := strconv.ParseInt(tailLinesStr, 10, 64); err == nil && t > 0 {
			tailLines = t
		}
	}

	client := k8s.GetClient()
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Kubernetes client not available")
		return
	}

	// Get pod to find containers
	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}

	pod, err := cache.Pods().Pods(namespace).Get(podName)
	if err != nil {
		s.writeError(w, http.StatusNotFound, fmt.Sprintf("Pod not found: %v", err))
		return
	}

	// Get container names
	var containers []string
	for _, c := range pod.Spec.Containers {
		containers = append(containers, c.Name)
	}
	for _, c := range pod.Spec.InitContainers {
		containers = append(containers, c.Name)
	}

	// Fetch logs
	logs := make(map[string]string)

	if container != "" {
		// Fetch logs for specific container
		logContent, err := s.fetchContainerLogs(r.Context(), namespace, podName, container, tailLines, previous)
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to fetch logs: %v", err))
			return
		}
		logs[container] = logContent
	} else {
		// Fetch logs for all containers
		for _, c := range containers {
			logContent, err := s.fetchContainerLogs(r.Context(), namespace, podName, c, tailLines, previous)
			if err != nil {
				logs[c] = fmt.Sprintf("Error fetching logs: %v", err)
			} else {
				logs[c] = logContent
			}
		}
	}

	response := LogsResponse{
		PodName:    podName,
		Namespace:  namespace,
		Containers: containers,
		Logs:       logs,
	}

	s.writeJSON(w, response)
}

// handlePodLogsStream streams logs from a pod using SSE
func (s *Server) handlePodLogsStream(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	podName := chi.URLParam(r, "name")
	container := r.URL.Query().Get("container")
	previous := r.URL.Query().Get("previous") == "true"
	tailLinesStr := r.URL.Query().Get("tailLines")

	tailLines := int64(100) // default for streaming
	if tailLinesStr != "" {
		if t, err := strconv.ParseInt(tailLinesStr, 10, 64); err == nil && t > 0 {
			tailLines = t
		}
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	client := k8s.GetClient()
	if client == nil {
		sendSSEError(w, flusher, "Kubernetes client not available")
		return
	}

	// If no container specified, get the first one
	if container == "" {
		cache := k8s.GetResourceCache()
		if cache != nil {
			pod, err := cache.Pods().Pods(namespace).Get(podName)
			if err == nil && len(pod.Spec.Containers) > 0 {
				container = pod.Spec.Containers[0].Name
			}
		}
	}

	// Build log options
	opts := &corev1.PodLogOptions{
		Container:  container,
		Follow:     true,
		TailLines:  &tailLines,
		Previous:   previous,
		Timestamps: true,
	}

	// Get log stream
	req := client.CoreV1().Pods(namespace).GetLogs(podName, opts)
	stream, err := req.Stream(r.Context())
	if err != nil {
		sendSSEError(w, flusher, fmt.Sprintf("Failed to open log stream: %v", err))
		return
	}
	defer stream.Close()

	// Send initial connection event
	sendSSEEvent(w, flusher, "connected", map[string]any{
		"pod":       podName,
		"namespace": namespace,
		"container": container,
	})

	// Stream logs line by line
	reader := bufio.NewReader(stream)
	for {
		select {
		case <-r.Context().Done():
			return
		default:
			line, err := reader.ReadString('\n')
			if err != nil {
				if err == io.EOF {
					// Stream ended (pod terminated or container finished)
					sendSSEEvent(w, flusher, "end", map[string]string{"reason": "stream ended"})
					return
				}
				// Check if context was cancelled
				if r.Context().Err() != nil {
					return
				}
				sendSSEError(w, flusher, fmt.Sprintf("Read error: %v", err))
				return
			}

			line = strings.TrimSuffix(line, "\n")
			if line == "" {
				continue
			}

			// Parse timestamp and content
			timestamp, content := parseLogLine(line)

			sendSSEEvent(w, flusher, "log", map[string]string{
				"timestamp": timestamp,
				"content":   content,
				"container": container,
			})
		}
	}
}

// fetchContainerLogs fetches logs for a specific container
func (s *Server) fetchContainerLogs(ctx context.Context, namespace, podName, container string, tailLines int64, previous bool) (string, error) {
	client := k8s.GetClient()
	if client == nil {
		return "", fmt.Errorf("kubernetes client not available")
	}

	opts := &corev1.PodLogOptions{
		Container:  container,
		TailLines:  &tailLines,
		Previous:   previous,
		Timestamps: true,
	}

	req := client.CoreV1().Pods(namespace).GetLogs(podName, opts)
	stream, err := req.Stream(ctx)
	if err != nil {
		return "", err
	}
	defer stream.Close()

	content, err := io.ReadAll(stream)
	if err != nil {
		return "", err
	}

	return string(content), nil
}

// parseLogLine extracts timestamp from a log line (format: 2024-01-20T10:30:00.123456789Z content)
func parseLogLine(line string) (timestamp, content string) {
	// K8s timestamps are in RFC3339Nano format at the start of the line
	if len(line) > 30 && line[4] == '-' && line[7] == '-' && line[10] == 'T' {
		// Find the space after timestamp
		spaceIdx := strings.Index(line, " ")
		if spaceIdx > 20 && spaceIdx < 40 {
			return line[:spaceIdx], line[spaceIdx+1:]
		}
	}
	return "", line
}

// sendSSEEvent sends an SSE event
func sendSSEEvent(w http.ResponseWriter, flusher http.Flusher, event string, data any) {
	jsonData, _ := json.Marshal(data)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, jsonData)
	flusher.Flush()
}

// sendSSEError sends an error event
func sendSSEError(w http.ResponseWriter, flusher http.Flusher, message string) {
	sendSSEEvent(w, flusher, "error", map[string]string{"error": message})
}
