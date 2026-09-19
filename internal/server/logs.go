package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"k8s.io/client-go/kubernetes"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

// LogsResponse is the response for non-streaming logs
type LogsResponse struct {
	PodName    string            `json:"podName"`
	Namespace  string            `json:"namespace"`
	Containers []string          `json:"containers"`
	Logs       map[string]string `json:"logs"` // container -> logs
	// Set when a container's logs could not be read. Without it an unreadable
	// container is indistinguishable from one that printed nothing.
}

// handlePodLogs fetches logs from a pod (non-streaming)
func (s *Server) handlePodLogs(w http.ResponseWriter, r *http.Request) {
	namespace := chi.URLParam(r, "namespace")
	podName := chi.URLParam(r, "name")
	container := r.URL.Query().Get("container")
	previous := r.URL.Query().Get("previous") == "true"
	tailLinesStr := r.URL.Query().Get("tailLines")
	sinceSecondsStr := r.URL.Query().Get("sinceSeconds")

	// Whether a container has logs changes from one minute to the next, and
	// several of the answers here are cacheable by default: 404 for a
	// container that has not restarted yet, and 410 in earlier versions of
	// this handler. A browser that caches one keeps showing it long after the
	// container has restarted and the logs exist, with no way for the reader
	// to tell. Observed: a cached 410 served for minutes while the live
	// endpoint returned the logs.
	w.Header().Set("Cache-Control", "no-store")

	// Check namespace access for authenticated users
	if allowed := s.getUserNamespaces(r, []string{namespace}); noNamespaceAccess(allowed) {
		s.writeError(w, http.StatusForbidden, "no access to namespace "+namespace)
		return
	}

	tailLines := parseTailLines(tailLinesStr, 500)
	sinceSeconds := parseSinceSeconds(sinceSecondsStr)

	client := s.getClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
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
		logContent, err := s.fetchContainerLogs(r.Context(), client, namespace, podName, container, tailLines, previous, sinceSeconds)
		if errors.Is(err, ErrLogsUnavailable) {
			// Not 404: absence is not established. The same answer comes back
			// when the container runtime is briefly unreachable, so this says
			// retryable rather than gone.
			s.writeErrorCode(w, http.StatusServiceUnavailable, "logs_unavailable", logsUnavailableMessage(previous))
			return
		}
		// A container that has not restarted has no earlier run to show. That
		// is an ordinary answer about the pod, not a fault in Radar.
		if previous && isNoPreviousContainer(err) {
			s.writeErrorCode(w, http.StatusNotFound, "no_previous_run", "This container has not restarted, so there is no previous run. Deselect Previous run to show the current run.")
			return
		}
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("Radar could not load logs: %v", err))
			return
		}
		logs[container] = logContent
	} else {
		// Fetch logs for all containers
		for _, c := range containers {
			logContent, err := s.fetchContainerLogs(r.Context(), client, namespace, podName, c, tailLines, previous, sinceSeconds)
			if errors.Is(err, ErrLogsUnavailable) {
				logs[c] = ""
			} else if err != nil {
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

	// Check namespace access for authenticated users
	if allowed := s.getUserNamespaces(r, []string{namespace}); noNamespaceAccess(allowed) {
		s.writeError(w, http.StatusForbidden, "no access to namespace "+namespace)
		return
	}

	sinceStr := r.URL.Query().Get("sinceSeconds")

	tailLines := parseTailLines(tailLinesStr, 100)
	sinceSeconds := parseSinceSeconds(sinceStr)

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

	client := s.getClientForRequest(r)
	if client == nil {
		sendSSEError(w, flusher, "cluster client not available — check cluster connection")
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

	// Flush the 200 + SSE headers and a connection event to the client before
	// opening the follow stream. GetContainerLogs with Follow issues a live,
	// blocking apiserver call that can stall (and a buffering OIDC/ALB proxy in
	// front of Radar holds back unflushed bytes); flushing first means the
	// browser EventSource opens immediately instead of pending with 0 bytes.
	sendSSEEvent(w, flusher, "connected", map[string]any{
		"pod":       podName,
		"namespace": namespace,
		"container": container,
	})

	stream, err := k8score.GetContainerLogs(r.Context(), client, namespace, podName, container, k8score.LogOptions{
		TailLines:    &tailLines,
		SinceSeconds: sinceSeconds,
		Previous:     previous,
		Timestamps:   true,
		Follow:       true,
	})
	if err != nil {
		// The connection is already open, so surface the failure as an SSE
		// error event rather than an HTTP error the client would never see.
		sendSSEError(w, flusher, fmt.Sprintf("Radar could not start live updates: %v", err))
		return
	}
	defer stream.Close()

	// Stream logs line by line
	reader := bufio.NewReader(stream)
	for {
		select {
		case <-r.Context().Done():
			return
		default:
			line, err := reader.ReadString('\n')
			if isLogsUnavailableNotice(line) {
				// Not a log line. The node is telling us it has nothing, and
				// the reader must not see it styled as workload output. Sent
				// as an end rather than an error so it reads the same as the
				// snapshot path: the cluster has nothing to give, which is not
				// a fault worth painting red.
				sendSSEEvent(w, flusher, "end", map[string]string{"reason": logsUnavailableMessage(previous)})
				return
			}
			if err != nil {
				if err == io.EOF {
					// Stream ended (pod terminated or container finished)
					sendSSEEvent(w, flusher, "end", map[string]string{"reason": ""})
					return
				}
				// Check if context was cancelled
				if r.Context().Err() != nil {
					return
				}
				sendSSEError(w, flusher, fmt.Sprintf("Radar stopped receiving logs: %v", err))
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

// fetchContainerLogs fetches logs for a specific container. Callers pass
// the impersonated client so log reads are subject to the user's K8s RBAC.
func (s *Server) fetchContainerLogs(ctx context.Context, client kubernetes.Interface, namespace, podName, container string, tailLines int64, previous bool, sinceSeconds *int64) (string, error) {
	if client == nil {
		return "", fmt.Errorf("cluster client not available")
	}

	stream, err := k8score.GetContainerLogs(ctx, client, namespace, podName, container, k8score.LogOptions{
		TailLines:    &tailLines,
		SinceSeconds: sinceSeconds,
		Previous:     previous,
		Timestamps:   true,
	})
	if err != nil {
		return "", err
	}
	defer stream.Close()

	content, err := io.ReadAll(stream)
	if err != nil {
		return "", err
	}

	if body := string(content); isLogsUnavailableNotice(body) {
		return "", ErrLogsUnavailable
	}

	return string(content), nil
}

// ErrLogsUnavailable means Kubernetes did not hand back a container's
// output. Usually the kubelet has already collected the log file, but the same
// answer comes back when the container runtime is briefly unreachable, so this
// deliberately does not claim the lines are gone for good.
var ErrLogsUnavailable = errors.New("Kubernetes did not return this container's logs")

// logsUnavailableMessage says what was asked for and what came back, and where
// the reader can still go. The same answer comes back for a run whose log file
// the node has dropped and for a runtime that is briefly unreachable, so it
// asserts neither. It says "Kubernetes" rather than "the node" because the
// reader has a relationship with the first and none with the second.
func logsUnavailableMessage(previous bool) string {
	if previous {
		return "Kubernetes did not return logs from the previous run. Deselect Previous run to show the current run."
	}
	return "Kubernetes did not return this container's logs. Select Refresh to ask again."
}

// isLogsUnavailableNotice recognises the kubelet's own apology for a log file
// it has already collected. The apiserver returns it with a 200 and it is the
// entire body, so without this it reaches the reader styled as a line their
// workload printed. Matched on the stable prefix rather than the whole string,
// which carries a container id.
//
// Ref: kubelet returns "unable to retrieve container logs for <containerID>".
// isNoPreviousContainer matches the apiserver's answer when --previous is asked
// and Kubernetes holds no earlier container for the name. It arrives as a 400,
// which would otherwise surface as a Radar failure. It does not distinguish a
// container that never restarted from one whose earlier record has been
// dropped; the pod's restart count is what separates those.

func isNoPreviousContainer(err error) bool {
	return err != nil && strings.Contains(err.Error(), "previous terminated container")
}

func isLogsUnavailableNotice(body string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(body)), "unable to retrieve container logs for")
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
