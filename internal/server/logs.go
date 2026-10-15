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
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/podlogs"
	"github.com/skyhook-io/radar/pkg/k8score"
)

type podLogStreamHandle struct {
	uid    types.UID
	cancel context.CancelFunc
}

type podLogSourceFailure struct {
	key string
	uid types.UID
}

func stopPodLogStreams(active *sync.Map, cursors map[string]*podlogs.StreamCursor, podName string) {
	active.Range(func(key, value any) bool {
		if strings.HasPrefix(key.(string), podName+"/") {
			value.(*podLogStreamHandle).cancel()
			active.CompareAndDelete(key, value)
		}
		return true
	})
	for key := range cursors {
		if strings.HasPrefix(key, podName+"/") {
			delete(cursors, key)
		}
	}
}

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
	sinceSecondsStr := r.URL.Query().Get("sinceSeconds")

	// Check namespace access for authenticated users
	if allowed := s.getUserNamespaces(r, []string{namespace}); noNamespaceAccess(allowed) {
		s.writeError(w, http.StatusForbidden, "no access to namespace "+namespace)
		return
	}
	if !s.authorizePodLogRead(w, r, namespace) {
		return
	}

	tailLines := podlogs.ParseTailLines(tailLinesStr, 500)
	sinceSeconds := podlogs.ParseSinceSeconds(sinceSecondsStr)

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
		// These are answers about the pod, not failures in Radar. The code
		// lets the viewer show them as such.
		if errors.Is(err, k8score.ErrLogsUnavailable) {
			s.writeErrorCode(w, http.StatusNotFound, "logs_unavailable", k8score.ErrLogsUnavailable.Error())
			return
		}
		if previous && podlogs.IsNoPreviousRun(err) {
			// The apiserver gives the same answer when the node has dropped
			// the earlier run's record, so the pod's status tells them apart.
			if hadEarlierRun, _ := podlogs.EarlierRun(pod, container); hadEarlierRun {
				s.writeErrorCode(w, http.StatusNotFound, "logs_unavailable", "Kubernetes did not return the logs of this container's earlier run.")
				return
			}
			s.writeErrorCode(w, http.StatusNotFound, "no_previous_run", "This container has not restarted, so there is no earlier run to show.")
			return
		}
		if podlogs.IsContainerNotStarted(err) {
			s.writeErrorCode(w, http.StatusNotFound, "container_not_started", podlogs.NotStartedMessage(pod, container, err))
			return
		}
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, fmt.Sprintf("Failed to fetch logs: %v", err))
			return
		}
		logs[container] = logContent
	} else {
		// Fetch logs for all containers
		for _, c := range containers {
			logContent, err := s.fetchContainerLogs(r.Context(), client, namespace, podName, c, tailLines, previous, sinceSeconds)
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

	// Check namespace access for authenticated users
	if allowed := s.getUserNamespaces(r, []string{namespace}); noNamespaceAccess(allowed) {
		s.writeError(w, http.StatusForbidden, "no access to namespace "+namespace)
		return
	}
	if !s.authorizePodLogRead(w, r, namespace) {
		return
	}

	sinceStr := r.URL.Query().Get("sinceSeconds")

	tailLines := podlogs.ParseTailLines(tailLinesStr, 100)
	sinceSeconds := podlogs.ParseSinceSeconds(sinceStr)

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
		if pod := cachedPod(namespace, podName); pod != nil && len(pod.Spec.Containers) > 0 {
			container = pod.Spec.Containers[0].Name
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

	// A container that has not started has no logs yet, but often will soon
	// (ContainerCreating, an image still pulling). The stream waits for it and
	// says why it is waiting, instead of ending on an answer that goes stale.
	// While Radar's pod cache still shows the container waiting, it doesn't ask
	// the apiserver again, so a waiting viewer costs no cluster requests.
	var waitingNotice string
	var stream io.ReadCloser
	lastWrite := time.Now()
	for {
		pod := cachedPod(namespace, podName)
		if waitingNotice != "" && podlogs.ContainerNotStartedYet(pod, container) {
			// Still waiting: the cache says why, without asking the apiserver.
			if notice := podlogs.NotStartedMessage(pod, container, nil) + " Logs will show here once it starts."; notice != waitingNotice {
				waitingNotice = notice
				sendSSEEvent(w, flusher, "notice", map[string]string{"notice": notice, "notice_code": "container_not_started"})
				lastWrite = time.Now()
			}
		} else {
			var err error
			stream, err = k8score.GetContainerLogs(r.Context(), client, namespace, podName, container, k8score.LogOptions{
				TailLines:    &tailLines,
				SinceSeconds: sinceSeconds,
				Previous:     previous,
				Timestamps:   true,
				Follow:       true,
			})
			if err == nil {
				break
			}
			if r.Context().Err() != nil {
				return
			}
			if !podlogs.IsContainerNotStarted(err) {
				// The connection is already open, so surface the failure as an SSE
				// error event rather than an HTTP error the client would never see.
				sendSSEError(w, flusher, fmt.Sprintf("Failed to open log stream: %v", err))
				return
			}
			if notice := podlogs.NotStartedMessage(pod, container, err) + " Logs will show here once it starts."; notice != waitingNotice {
				waitingNotice = notice
				sendSSEEvent(w, flusher, "notice", map[string]string{"notice": notice, "notice_code": "container_not_started"})
				lastWrite = time.Now()
			}
		}
		if time.Since(lastWrite) >= waitingKeepaliveInterval {
			// An unchanged notice sends nothing, and a proxy in front of Radar
			// may close a connection that stays idle through a long image pull.
			// An SSE comment keeps it open without reaching the viewer.
			_, _ = fmt.Fprint(w, ": waiting\n\n")
			flusher.Flush()
			lastWrite = time.Now()
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(containerStartPollInterval):
		}
	}
	if waitingNotice != "" {
		sendSSEEvent(w, flusher, "notice", map[string]string{"notice": ""})
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
			// Checked before the EOF branch: the notice is the whole body with
			// no trailing newline, so it arrives together with EOF.
			if k8score.IsLogsUnavailableNotice(line) {
				// Coded like the snapshot answer, so the viewer shows it as an
				// answer about the pod rather than a Radar failure.
				sendSSEEvent(w, flusher, "error", map[string]string{"error": k8score.ErrLogsUnavailable.Error(), "error_code": "logs_unavailable"})
				return
			}

			line = strings.TrimSuffix(line, "\n")

			if line != "" && (err == nil || err == io.EOF) {
				// Parse timestamp and content
				timestamp, content := podlogs.ParseLine(line)

				sendSSEEvent(w, flusher, "log", map[string]string{
					"timestamp": timestamp,
					"content":   content,
					"container": container,
				})
			}
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
		}
	}
}

// containerStartPollInterval is how often a waiting pod log stream checks
// whether its container has started.
var containerStartPollInterval = 2 * time.Second

// waitingKeepaliveInterval bounds how long a waiting pod log stream stays
// silent, well under common proxy idle timeouts.
var waitingKeepaliveInterval = 15 * time.Second

func cachedPod(namespace, name string) *corev1.Pod {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil
	}
	pod, err := cache.Pods().Pods(namespace).Get(name)
	if err != nil {
		return nil
	}
	return pod
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

	if body := string(content); k8score.IsLogsUnavailableNotice(body) {
		return "", k8score.ErrLogsUnavailable
	}

	return string(content), nil
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
