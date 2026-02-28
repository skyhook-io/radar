package server

import (
	"bufio"
	"context"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/skyhook-io/radar/internal/k8s"
)

// WorkloadPodInfo contains basic info about a pod for the UI
type WorkloadPodInfo struct {
	Name       string   `json:"name"`
	Containers []string `json:"containers"`
	Ready      bool     `json:"ready"`
}

// workloadLogEntry is an internal structure for log lines from pods
type workloadLogEntry struct {
	Pod       string `json:"pod"`
	Container string `json:"container"`
	Timestamp string `json:"timestamp"`
	Content   string `json:"content"`
}

// validWorkloadKinds defines which resource types support workload logs
var validWorkloadKinds = map[string]bool{
	"deployments":  true,
	"statefulsets": true,
	"daemonsets":   true,
}

// handleWorkloadPods returns the list of pods for a workload
func (s *Server) handleWorkloadPods(w http.ResponseWriter, r *http.Request) {
	kind := strings.ToLower(chi.URLParam(r, "kind"))
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	pods, err := s.getWorkloadPods(kind, namespace, name)
	if err != nil {
		s.writeWorkloadError(w, err)
		return
	}

	s.writeJSON(w, map[string]any{
		"pods": buildPodInfos(pods),
	})
}

// handleWorkloadLogs fetches and merges logs from all pods (non-streaming)
func (s *Server) handleWorkloadLogs(w http.ResponseWriter, r *http.Request) {
	kind := strings.ToLower(chi.URLParam(r, "kind"))
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	container := r.URL.Query().Get("container")
	tailLines := parseTailLines(r.URL.Query().Get("tailLines"), 100)
	sinceSeconds := parseSinceSeconds(r.URL.Query().Get("sinceSeconds"))

	pods, err := s.getWorkloadPods(kind, namespace, name)
	if err != nil {
		s.writeWorkloadError(w, err)
		return
	}

	if len(pods) == 0 {
		s.writeJSON(w, map[string]any{
			"pods": []WorkloadPodInfo{},
			"logs": []workloadLogEntry{},
		})
		return
	}

	client := k8s.GetClient()
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "kubernetes client not available")
		return
	}

	// Collect logs from all pods concurrently
	allLogs := collectLogsFromPods(r.Context(), client, namespace, pods, container, tailLines, sinceSeconds)

	// Sort by timestamp (string comparison works for RFC3339 format)
	sortLogsByTimestamp(allLogs)

	s.writeJSON(w, map[string]any{
		"pods": buildPodInfos(pods),
		"logs": allLogs,
	})
}

// handleWorkloadLogsStream streams logs from all pods using SSE
func (s *Server) handleWorkloadLogsStream(w http.ResponseWriter, r *http.Request) {
	kind := strings.ToLower(chi.URLParam(r, "kind"))
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	container := r.URL.Query().Get("container")
	tailLines := parseTailLines(r.URL.Query().Get("tailLines"), 50)
	sinceSeconds := parseSinceSeconds(r.URL.Query().Get("sinceSeconds"))

	if !validWorkloadKinds[kind] {
		s.writeError(w, http.StatusBadRequest, "only deployments, statefulsets, and daemonsets are supported")
		return
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	cache := k8s.GetResourceCache()
	if cache == nil {
		sendSSEError(w, flusher, "resource cache not available")
		return
	}

	client := k8s.GetClient()
	if client == nil {
		sendSSEError(w, flusher, "kubernetes client not available")
		return
	}

	selector, err := k8s.GetWorkloadSelector(cache, kind, namespace, name)
	if err != nil {
		sendSSEError(w, flusher, err.Error())
		return
	}

	// Get initial pods
	pods := cache.GetPodsForWorkload(namespace, selector)
	podInfos := buildPodInfos(pods)

	// Send connected event with pod list
	sendSSEEvent(w, flusher, "connected", map[string]any{
		"workload":  name,
		"namespace": namespace,
		"kind":      kind,
		"pods":      podInfos,
	})

	if len(pods) == 0 {
		sendSSEEvent(w, flusher, "end", map[string]string{"reason": "no pods found"})
		return
	}

	// Context for managing goroutines
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Channel for aggregated log lines
	logCh := make(chan workloadLogEntry, 1000)

	// Track active streams
	var activeStreams sync.Map // podName+containerName -> cancel func
	var streamWg sync.WaitGroup

	// Start streaming from each pod/container
	startPodStreams := func(pods []*corev1.Pod) {
		for _, pod := range pods {
			containers := k8s.GetContainersForPod(pod, container, true)
			for _, c := range containers {
				key := pod.Name + "/" + c
				if _, exists := activeStreams.Load(key); exists {
					continue // Already streaming
				}

				streamCtx, streamCancel := context.WithCancel(ctx)
				activeStreams.Store(key, streamCancel)

				streamWg.Add(1)
				go func(podName, containerName string, streamCtx context.Context) {
					defer streamWg.Done()
					defer activeStreams.Delete(podName + "/" + containerName)

					streamPodLogs(streamCtx, client, namespace, podName, containerName, tailLines, sinceSeconds, logCh)
				}(pod.Name, c, streamCtx)
			}
		}
	}

	// Start initial streams
	startPodStreams(pods)

	// Pod discovery ticker (every 5 seconds)
	discoveryTicker := time.NewTicker(5 * time.Second)
	defer discoveryTicker.Stop()

	// Track known pods for detecting changes
	knownPods := make(map[string]bool)
	for _, p := range pods {
		knownPods[p.Name] = true
	}

	// Main loop: forward logs and handle pod discovery
	for {
		select {
		case <-ctx.Done():
			sendSSEEvent(w, flusher, "end", map[string]string{"reason": "client disconnected"})
			return

		case entry := <-logCh:
			sendSSEEvent(w, flusher, "log", entry)

		case <-discoveryTicker.C:
			// Re-discover pods
			currentPods := cache.GetPodsForWorkload(namespace, selector)
			currentPodNames := make(map[string]bool)
			for _, p := range currentPods {
				currentPodNames[p.Name] = true
			}

			// Check for new pods
			var newPods []*corev1.Pod
			for _, p := range currentPods {
				if !knownPods[p.Name] {
					newPods = append(newPods, p)
					knownPods[p.Name] = true
					// Notify frontend about new pod
					sendSSEEvent(w, flusher, "pod_added", map[string]any{
						"pods": []WorkloadPodInfo{buildPodInfo(p)},
					})
				}
			}

			// Check for removed pods
			for podName := range knownPods {
				if !currentPodNames[podName] {
					delete(knownPods, podName)
					// Cancel streams for this pod
					activeStreams.Range(func(key, value any) bool {
						if strings.HasPrefix(key.(string), podName+"/") {
							if cancelFn, ok := value.(context.CancelFunc); ok {
								cancelFn()
							}
							activeStreams.Delete(key)
						}
						return true
					})
					// Notify frontend
					sendSSEEvent(w, flusher, "pod_removed", map[string]string{
						"pod":    podName,
						"reason": "terminated",
					})
				}
			}

			// Start streams for new pods
			if len(newPods) > 0 {
				startPodStreams(newPods)
			}
		}
	}
}

// streamPodLogs streams logs from a single pod/container to the log channel
func streamPodLogs(ctx context.Context, client *kubernetes.Clientset, namespace, podName, containerName string, tailLines int64, sinceSeconds *int64, logCh chan<- workloadLogEntry) {
	opts := &corev1.PodLogOptions{
		Container:  containerName,
		Follow:     true,
		TailLines:  &tailLines,
		Timestamps: true,
	}
	if sinceSeconds != nil {
		opts.SinceSeconds = sinceSeconds
	}

	req := client.CoreV1().Pods(namespace).GetLogs(podName, opts)
	stream, err := req.Stream(ctx)
	if err != nil {
		log.Printf("[workload-logs] Failed to stream logs for %s/%s: %v", podName, containerName, err)
		return
	}
	defer stream.Close()

	reader := bufio.NewReader(stream)
	for {
		select {
		case <-ctx.Done():
			return
		default:
			line, err := reader.ReadString('\n')
			if err != nil {
				if err == io.EOF || ctx.Err() != nil {
					return
				}
				log.Printf("[workload-logs] Read error for %s/%s: %v", podName, containerName, err)
				return
			}

			line = strings.TrimSuffix(line, "\n")
			if line == "" {
				continue
			}

			ts, content := parseLogLine(line)
			select {
			case logCh <- workloadLogEntry{
				Pod:       podName,
				Container: containerName,
				Timestamp: ts,
				Content:   content,
			}:
			case <-ctx.Done():
				return
			}
		}
	}
}

// isPodReady checks if all containers in a pod are ready
func isPodReady(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if !cs.Ready {
			return false
		}
	}
	return true
}

// buildPodInfos converts pods to WorkloadPodInfo slice
func buildPodInfos(pods []*corev1.Pod) []WorkloadPodInfo {
	infos := make([]WorkloadPodInfo, 0, len(pods))
	for _, pod := range pods {
		infos = append(infos, buildPodInfo(pod))
	}
	return infos
}

// buildPodInfo converts a single pod to WorkloadPodInfo
func buildPodInfo(pod *corev1.Pod) WorkloadPodInfo {
	containers := make([]string, 0, len(pod.Spec.Containers)+len(pod.Spec.InitContainers))
	for _, c := range pod.Spec.InitContainers {
		containers = append(containers, c.Name)
	}
	for _, c := range pod.Spec.Containers {
		containers = append(containers, c.Name)
	}
	return WorkloadPodInfo{
		Name:       pod.Name,
		Containers: containers,
		Ready:      isPodReady(pod),
	}
}

// sortLogsByTimestamp sorts log entries by timestamp using efficient sort
func sortLogsByTimestamp(logs []workloadLogEntry) {
	sort.Slice(logs, func(i, j int) bool {
		return logs[i].Timestamp < logs[j].Timestamp
	})
}

// workloadError represents a typed error for workload operations
type workloadError struct {
	statusCode int
	message    string
}

func (e *workloadError) Error() string { return e.message }

// getWorkloadPods validates the kind, retrieves cache, and returns pods for a workload
func (s *Server) getWorkloadPods(kind, namespace, name string) ([]*corev1.Pod, *workloadError) {
	if !validWorkloadKinds[kind] {
		return nil, &workloadError{http.StatusBadRequest, "only deployments, statefulsets, and daemonsets are supported"}
	}

	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil, &workloadError{http.StatusServiceUnavailable, "resource cache not available"}
	}

	selector, err := k8s.GetWorkloadSelector(cache, kind, namespace, name)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, &workloadError{http.StatusNotFound, err.Error()}
		}
		if strings.Contains(err.Error(), "insufficient permissions") {
			return nil, &workloadError{http.StatusForbidden, err.Error()}
		}
		return nil, &workloadError{http.StatusInternalServerError, err.Error()}
	}

	return cache.GetPodsForWorkload(namespace, selector), nil
}

// writeWorkloadError writes an error response based on workloadError
func (s *Server) writeWorkloadError(w http.ResponseWriter, err *workloadError) {
	s.writeError(w, err.statusCode, err.message)
}

// parseSinceSeconds parses sinceSeconds query parameter, returning nil if not set
func parseSinceSeconds(str string) *int64 {
	if str == "" {
		return nil
	}
	if s, err := strconv.ParseInt(str, 10, 64); err == nil && s > 0 {
		return &s
	}
	return nil
}

// parseTailLines parses tailLines query parameter with a default value
func parseTailLines(str string, defaultVal int64) int64 {
	if str == "" {
		return defaultVal
	}
	if t, err := strconv.ParseInt(str, 10, 64); err == nil && t > 0 {
		return t
	}
	return defaultVal
}

// collectLogsFromPods fetches logs from all pods concurrently
func collectLogsFromPods(ctx context.Context, client *kubernetes.Clientset, namespace string, pods []*corev1.Pod, container string, tailLines int64, sinceSeconds *int64) []workloadLogEntry {
	var allLogs []workloadLogEntry
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, pod := range pods {
		containers := k8s.GetContainersForPod(pod, container, true)
		for _, c := range containers {
			wg.Add(1)
			go func(podName, containerName string) {
				defer wg.Done()

				entries := fetchPodContainerLogs(ctx, client, namespace, podName, containerName, tailLines, sinceSeconds)
				if len(entries) > 0 {
					mu.Lock()
					allLogs = append(allLogs, entries...)
					mu.Unlock()
				}
			}(pod.Name, c)
		}
	}

	wg.Wait()
	return allLogs
}

// fetchPodContainerLogs fetches logs for a single pod/container
func fetchPodContainerLogs(ctx context.Context, client *kubernetes.Clientset, namespace, podName, containerName string, tailLines int64, sinceSeconds *int64) []workloadLogEntry {
	opts := &corev1.PodLogOptions{
		Container:  containerName,
		TailLines:  &tailLines,
		Timestamps: true,
	}
	if sinceSeconds != nil {
		opts.SinceSeconds = sinceSeconds
	}

	req := client.CoreV1().Pods(namespace).GetLogs(podName, opts)
	stream, err := req.Stream(ctx)
	if err != nil {
		log.Printf("[workload-logs] Failed to get logs for %s/%s: %v", podName, containerName, err)
		return nil
	}
	defer stream.Close()

	content, err := io.ReadAll(stream)
	if err != nil {
		log.Printf("[workload-logs] Failed to read logs for %s/%s: %v", podName, containerName, err)
		return nil
	}

	lines := strings.Split(string(content), "\n")
	entries := make([]workloadLogEntry, 0, len(lines))
	for _, line := range lines {
		if line == "" {
			continue
		}
		ts, text := parseLogLine(line)
		entries = append(entries, workloadLogEntry{
			Pod:       podName,
			Container: containerName,
			Timestamp: ts,
			Content:   text,
		})
	}
	return entries
}
