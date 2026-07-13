package server

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/health"
	"github.com/skyhook-io/radar/pkg/k8score"
)

// WorkloadPodContainerInfo contains compact per-container runtime status for the UI.
type WorkloadPodContainerInfo struct {
	Name         string `json:"name"`
	Init         bool   `json:"init,omitempty"`
	Ready        bool   `json:"ready"`
	RestartCount int32  `json:"restartCount"`
}

// WorkloadPodInfo contains compact runtime status about a pod for workload views.
type WorkloadPodInfo struct {
	Name                  string                     `json:"name"`
	Containers            []string                   `json:"containers"`
	Ready                 bool                       `json:"ready"`
	Phase                 string                     `json:"phase,omitempty"`
	HealthLevel           string                     `json:"healthLevel,omitempty"`
	Reason                string                     `json:"reason,omitempty"`
	Message               string                     `json:"message,omitempty"`
	RestartCount          int32                      `json:"restartCount,omitempty"`
	LastTerminationReason string                     `json:"lastTerminationReason,omitempty"`
	CreatedAt             string                     `json:"createdAt,omitempty"`
	ContainerStatuses     []WorkloadPodContainerInfo `json:"containerStatuses,omitempty"`
	StepID                string                     `json:"stepID,omitempty"`
	StepName              string                     `json:"stepName,omitempty"`
	StepPhase             string                     `json:"stepPhase,omitempty"`
}

// workloadLogEntry is an internal structure for log lines from pods
type workloadLogEntry struct {
	Pod       string `json:"pod"`
	Container string `json:"container"`
	Timestamp string `json:"timestamp"`
	Content   string `json:"content"`
}

type workloadLogMetadata struct {
	EmptyReason  string `json:"emptyReason,omitempty"`
	EmptyMessage string `json:"emptyMessage,omitempty"`
	Command      string `json:"command,omitempty"`
}

type WorkloadRun struct {
	Kind        string `json:"kind"`
	Namespace   string `json:"namespace"`
	Name        string `json:"name"`
	Phase       string `json:"phase"`
	Active      bool   `json:"active"`
	StartedAt   string `json:"startedAt,omitempty"`
	FinishedAt  string `json:"finishedAt,omitempty"`
	ScheduledAt string `json:"scheduledAt,omitempty"`
	Trigger     string `json:"trigger,omitempty"`
	Message     string `json:"message,omitempty"`

	Succeeded   int32                   `json:"succeeded,omitempty"`
	Failed      int32                   `json:"failed,omitempty"`
	Running     int32                   `json:"running,omitempty"`
	Desired     int32                   `json:"desired,omitempty"`
	Parallelism int32                   `json:"parallelism,omitempty"`
	Progress    string                  `json:"progress,omitempty"`
	Template    string                  `json:"template,omitempty"`
	Launcher    *WorkloadRunResourceRef `json:"launcher,omitempty"`

	PodTotal     int `json:"podTotal,omitempty"`
	PodSucceeded int `json:"podSucceeded,omitempty"`
	PodFailed    int `json:"podFailed,omitempty"`
	PodRunning   int `json:"podRunning,omitempty"`
	PodPending   int `json:"podPending,omitempty"`
}

type WorkloadRunResourceRef struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Group     string `json:"group,omitempty"`
}

// validWorkloadKinds defines which resource types support workload logs.
// Accepts both singular and plural forms so the frontend can send K8s canonical
// Kind names ("Deployment") without additional pluralization.
var validWorkloadKinds = map[string]bool{
	"deployment":   true,
	"deployments":  true,
	"statefulset":  true,
	"statefulsets": true,
	"daemonset":    true,
	"daemonsets":   true,
	"job":          true,
	"jobs":         true,
	"workflow":     true,
	"workflows":    true,
}

// handleWorkloadPods returns the list of pods for a workload
func (s *Server) handleWorkloadPods(w http.ResponseWriter, r *http.Request) {
	kind := strings.ToLower(chi.URLParam(r, "kind"))
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	if noNamespaceAccess(s.getUserNamespaces(r, []string{namespace})) {
		s.writeError(w, http.StatusForbidden, "no access to namespace "+namespace)
		return
	}

	pods, err := s.getWorkloadPods(kind, namespace, name)
	if err != nil {
		s.writeWorkloadError(w, err)
		return
	}

	s.writeJSON(w, map[string]any{
		"pods": buildPodInfos(pods),
	})
}

// handleWorkloadRuns returns retained child runs for scheduled workload kinds.
func (s *Server) handleWorkloadRuns(w http.ResponseWriter, r *http.Request) {
	kind := strings.ToLower(chi.URLParam(r, "kind"))
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")
	clusterScoped := r.URL.Query().Get("clusterScoped") == "true"

	runNamespaces := []string{namespace}
	if clusterScoped {
		runNamespaces = s.parseNamespacesForUser(r)
		if noNamespaceAccess(runNamespaces) {
			s.writeError(w, http.StatusForbidden, "no namespace access")
			return
		}
	} else {
		if allowed := s.getUserNamespaces(r, []string{namespace}); noNamespaceAccess(allowed) {
			s.writeError(w, http.StatusForbidden, "no access to namespace "+namespace)
			return
		}
	}
	switch kind {
	case "job", "jobs":
		if !s.canRead(r, "batch", "jobs", namespace, "get") {
			s.writeError(w, http.StatusForbidden, "no access to jobs in namespace "+namespace)
			return
		}
	case "workflow", "workflows":
		if !s.canRead(r, "argoproj.io", "workflows", namespace, "get") {
			s.writeError(w, http.StatusForbidden, "no access to workflows in namespace "+namespace)
			return
		}
	case "cronjob", "cronjobs":
		if !s.canRead(r, "batch", "cronjobs", namespace, "get") {
			s.writeError(w, http.StatusForbidden, "no access to cronjobs in namespace "+namespace)
			return
		}
		if !s.canRead(r, "batch", "jobs", namespace, "list") {
			s.writeError(w, http.StatusForbidden, "no access to jobs in namespace "+namespace)
			return
		}
	case "cronworkflow", "cronworkflows":
		if !s.canRead(r, "argoproj.io", "cronworkflows", namespace, "get") {
			s.writeError(w, http.StatusForbidden, "no access to cronworkflows in namespace "+namespace)
			return
		}
		if !s.canRead(r, "argoproj.io", "workflows", namespace, "list") {
			s.writeError(w, http.StatusForbidden, "no access to workflows in namespace "+namespace)
			return
		}
	case "workflowtemplate", "workflowtemplates":
		if !s.canRead(r, "argoproj.io", "workflowtemplates", namespace, "get") {
			s.writeError(w, http.StatusForbidden, "no access to workflowtemplates in namespace "+namespace)
			return
		}
		if !s.canRead(r, "argoproj.io", "workflows", namespace, "list") {
			s.writeError(w, http.StatusForbidden, "no access to workflows in namespace "+namespace)
			return
		}
	case "clusterworkflowtemplate", "clusterworkflowtemplates":
		if !s.canRead(r, "argoproj.io", "clusterworkflowtemplates", "", "get") {
			s.writeError(w, http.StatusForbidden, "no access to clusterworkflowtemplates")
			return
		}
		var ok bool
		runNamespaces, ok = s.readableRunNamespaces(r, "argoproj.io", "workflows", runNamespaces)
		if !ok {
			s.writeError(w, http.StatusForbidden, "no access to workflows")
			return
		}
	case "scaledjob", "scaledjobs":
		if !s.canRead(r, "keda.sh", "scaledjobs", namespace, "get") {
			s.writeError(w, http.StatusForbidden, "no access to scaledjobs in namespace "+namespace)
			return
		}
		if !s.canRead(r, "batch", "jobs", namespace, "list") {
			s.writeError(w, http.StatusForbidden, "no access to jobs in namespace "+namespace)
			return
		}
	}

	runs, err := s.getWorkloadRuns(r.Context(), kind, namespace, name, runNamespaces)
	if err != nil {
		s.writeWorkloadError(w, err)
		return
	}

	s.writeJSON(w, map[string]any{
		"runs": runs,
	})
}

func (s *Server) readableRunNamespaces(r *http.Request, group, resource string, namespaces []string) ([]string, bool) {
	if noNamespaceAccess(namespaces) {
		return nil, false
	}
	if namespaces == nil {
		if s.canRead(r, group, resource, "", "list") {
			return nil, true
		}
		allowed := s.filterNamespacesByCanRead(r, group, resource, "list", s.allNamespaceNames())
		return allowed, len(allowed) > 0
	}
	allowed := s.filterNamespacesByCanRead(r, group, resource, "list", namespaces)
	return allowed, len(allowed) > 0
}

// handleWorkloadLogs fetches and merges logs from all pods (non-streaming)
func (s *Server) handleWorkloadLogs(w http.ResponseWriter, r *http.Request) {
	kind := strings.ToLower(chi.URLParam(r, "kind"))
	namespace := chi.URLParam(r, "namespace")
	name := chi.URLParam(r, "name")

	// Check namespace access for authenticated users
	if allowed := s.getUserNamespaces(r, []string{namespace}); noNamespaceAccess(allowed) {
		s.writeError(w, http.StatusForbidden, "no access to namespace "+namespace)
		return
	}

	container := r.URL.Query().Get("container")
	tailLines := parseTailLines(r.URL.Query().Get("tailLines"), 100)
	sinceSeconds := parseSinceSeconds(r.URL.Query().Get("sinceSeconds"))

	pods, err := s.getWorkloadPods(kind, namespace, name)
	if err != nil {
		s.writeWorkloadError(w, err)
		return
	}

	if len(pods) == 0 {
		metadata := s.describeWorkloadLogEmpty(r.Context(), kind, namespace, name)
		response := map[string]any{
			"pods": []WorkloadPodInfo{},
			"logs": []workloadLogEntry{},
		}
		addWorkloadLogMetadata(response, metadata)
		s.writeJSON(w, response)
		return
	}

	client := s.getClientForRequest(r)
	if client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "cluster client not available — check cluster connection")
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

	// Check namespace access for authenticated users
	if allowed := s.getUserNamespaces(r, []string{namespace}); noNamespaceAccess(allowed) {
		s.writeError(w, http.StatusForbidden, "no access to namespace "+namespace)
		return
	}

	container := r.URL.Query().Get("container")
	tailLines := parseTailLines(r.URL.Query().Get("tailLines"), 50)
	sinceSeconds := parseSinceSeconds(r.URL.Query().Get("sinceSeconds"))

	if !validWorkloadKinds[kind] {
		s.writeError(w, http.StatusBadRequest, "only deployments, statefulsets, daemonsets, jobs, and workflows are supported")
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

	client := s.getClientForRequest(r)
	if client == nil {
		sendSSEError(w, flusher, "cluster client not available — check cluster connection")
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
	connected := map[string]any{
		"workload":  name,
		"namespace": namespace,
		"kind":      kind,
		"pods":      podInfos,
	}
	var emptyMetadata workloadLogMetadata
	if len(pods) == 0 {
		emptyMetadata = s.describeWorkloadLogEmpty(r.Context(), kind, namespace, name)
		addWorkloadLogMetadata(connected, emptyMetadata)
	}
	sendSSEEvent(w, flusher, "connected", connected)

	if len(pods) == 0 && !shouldWaitForPodsInLogStream(kind, emptyMetadata) {
		sendSSEEvent(w, flusher, "end", workloadLogEndPayload(emptyMetadata))
		return
	}

	// Context for managing goroutines
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Channel for aggregated log lines
	logCh := make(chan workloadLogEntry, 1000)

	// Track active streams
	var activeStreams sync.Map // podName/containerName -> cancel func
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
				go func(podName, containerName, key string, streamCtx context.Context) {
					defer streamWg.Done()
					defer activeStreams.Delete(key)

					streamPodLogs(streamCtx, client, namespace, podName, containerName, tailLines, sinceSeconds, logCh)
				}(pod.Name, c, key, streamCtx)
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
			if len(currentPods) == 0 {
				metadata := s.describeWorkloadLogEmpty(ctx, kind, namespace, name)
				if !shouldWaitForPodsInLogStream(kind, metadata) {
					sendSSEEvent(w, flusher, "end", workloadLogEndPayload(metadata))
					return
				}
			}
			currentPodNames := make(map[string]bool)
			for _, p := range currentPods {
				currentPodNames[p.Name] = true
			}

			// Check for new pods
			for _, p := range currentPods {
				if !knownPods[p.Name] {
					knownPods[p.Name] = true
					// Notify frontend about new pod
					sendSSEEvent(w, flusher, "pod_added", map[string]any{
						"pods": []WorkloadPodInfo{buildPodInfo(p, time.Now())},
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

			// Start streams for all current pods — startPodStreams skips pods
			// that already have active streams, so this safely retries pods
			// whose initial stream failed (e.g., container wasn't ready yet)
			startPodStreams(currentPods)
		}
	}
}

func workloadLogEndPayload(metadata workloadLogMetadata) map[string]string {
	reason := metadata.EmptyReason
	if reason == "" {
		reason = "no pods found"
	}
	payload := map[string]string{"reason": reason}
	if metadata.EmptyReason != "" {
		payload["emptyReason"] = metadata.EmptyReason
	}
	if metadata.EmptyMessage != "" {
		payload["emptyMessage"] = metadata.EmptyMessage
	}
	if metadata.Command != "" {
		payload["command"] = metadata.Command
	}
	return payload
}

func shouldWaitForPodsInLogStream(kind string, metadata workloadLogMetadata) bool {
	if metadata.EmptyReason != "no-pods" {
		return false
	}
	return kind == "job" || kind == "jobs" || kind == "workflow" || kind == "workflows"
}

// streamPodLogs streams logs from a single pod/container to the log channel
func streamPodLogs(ctx context.Context, client kubernetes.Interface, namespace, podName, containerName string, tailLines int64, sinceSeconds *int64, logCh chan<- workloadLogEntry) {
	stream, err := k8score.GetContainerLogs(ctx, client, namespace, podName, containerName, k8score.LogOptions{
		TailLines:    &tailLines,
		SinceSeconds: sinceSeconds,
		Timestamps:   true,
		Follow:       true,
	})
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
	now := time.Now()
	for _, pod := range pods {
		infos = append(infos, buildPodInfo(pod, now))
	}
	return infos
}

// buildPodInfo converts a single pod to WorkloadPodInfo
func buildPodInfo(pod *corev1.Pod, now time.Time) WorkloadPodInfo {
	containers := make([]string, 0, len(pod.Spec.Containers)+len(pod.Spec.InitContainers))
	containerStatuses := make([]WorkloadPodContainerInfo, 0, len(pod.Status.InitContainerStatuses)+len(pod.Status.ContainerStatuses))
	for _, c := range pod.Spec.InitContainers {
		containers = append(containers, c.Name)
	}
	for _, c := range pod.Spec.Containers {
		containers = append(containers, c.Name)
	}
	for _, cs := range pod.Status.InitContainerStatuses {
		containerStatuses = append(containerStatuses, WorkloadPodContainerInfo{
			Name:         cs.Name,
			Init:         true,
			Ready:        cs.Ready,
			RestartCount: cs.RestartCount,
		})
	}
	for _, cs := range pod.Status.ContainerStatuses {
		containerStatuses = append(containerStatuses, WorkloadPodContainerInfo{
			Name:         cs.Name,
			Ready:        cs.Ready,
			RestartCount: cs.RestartCount,
		})
	}
	verdict := health.Pod(pod, now)
	displayLevel := health.PodDisplayLevel(pod, now)
	if displayLevel != verdict.Level {
		verdict.Level = displayLevel
		if verdict.Reason == "" {
			verdict.Reason = health.PodProblemReason(pod, now)
		}
		if verdict.Message == "" {
			verdict.Message = health.PodProblemMessage(pod)
		}
	}
	restartCount, lastTerminationReason := health.PodRestartContext(pod)
	createdAt := ""
	if !pod.CreationTimestamp.IsZero() {
		createdAt = pod.CreationTimestamp.Time.Format(time.RFC3339)
	}
	annotations := pod.GetAnnotations()
	labels := pod.GetLabels()
	return WorkloadPodInfo{
		Name:                  pod.Name,
		Containers:            containers,
		Ready:                 isPodReady(pod),
		Phase:                 string(pod.Status.Phase),
		HealthLevel:           string(verdict.Level),
		Reason:                verdict.Reason,
		Message:               verdict.Message,
		RestartCount:          restartCount,
		LastTerminationReason: lastTerminationReason,
		CreatedAt:             createdAt,
		ContainerStatuses:     containerStatuses,
		StepID:                annotations["workflows.argoproj.io/node-id"],
		StepName:              annotations["workflows.argoproj.io/node-name"],
		StepPhase:             labels["workflows.argoproj.io/phase"],
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
		return nil, &workloadError{http.StatusBadRequest, "only deployments, statefulsets, daemonsets, jobs, and workflows are supported"}
	}

	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil, &workloadError{http.StatusServiceUnavailable, "resource cache not available"}
	}

	selector, err := k8s.GetWorkloadSelector(cache, kind, namespace, name)
	if err != nil {
		return nil, workloadSelectorGetError(err)
	}

	return cache.GetPodsForWorkload(namespace, selector), nil
}

func workloadSelectorGetError(err error) *workloadError {
	if errors.Is(err, k8s.ErrWorkloadAccessDenied) || apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
		return &workloadError{http.StatusForbidden, err.Error()}
	}
	if apierrors.IsNotFound(err) || errors.Is(err, k8score.ErrResourceNotFound) {
		return &workloadError{http.StatusNotFound, err.Error()}
	}
	return &workloadError{http.StatusInternalServerError, err.Error()}
}

func (s *Server) getWorkloadRuns(ctx context.Context, kind, namespace, name string, runNamespaces []string) ([]WorkloadRun, *workloadError) {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil, &workloadError{http.StatusServiceUnavailable, "resource cache not available"}
	}

	var runs []WorkloadRun
	switch kind {
	case "job", "jobs":
		if cache.Jobs() == nil {
			return nil, &workloadError{http.StatusForbidden, "insufficient permissions to get jobs"}
		}
		job, err := cache.Jobs().Jobs(namespace).Get(name)
		if err != nil {
			return nil, workloadParentGetError("job", namespace, name, err)
		}
		runs = append(runs, jobRunInfo(job))
	case "workflow", "workflows":
		workflow, err := cache.GetDynamicWithGroup(ctx, "Workflow", namespace, name, "argoproj.io")
		if err != nil {
			return nil, workloadParentGetError("workflow", namespace, name, err)
		}
		runs = append(runs, workflowRunInfo(workflow))
	case "cronjob", "cronjobs":
		if cache.CronJobs() == nil {
			return nil, &workloadError{http.StatusForbidden, "insufficient permissions to list cronjobs"}
		}
		if _, err := cache.CronJobs().CronJobs(namespace).Get(name); err != nil {
			return nil, workloadParentGetError("cronjob", namespace, name, err)
		}
		if cache.Jobs() == nil {
			return nil, &workloadError{http.StatusForbidden, "insufficient permissions to list jobs"}
		}
		jobs, err := listJobRuns(cache, []string{namespace})
		if err != nil {
			return nil, &workloadError{http.StatusInternalServerError, err.Error()}
		}
		for _, job := range jobs {
			if controllerOwnerName(job.OwnerReferences, "CronJob") == name {
				runs = append(runs, jobRunInfo(job))
			}
		}
	case "cronworkflow", "cronworkflows":
		if _, err := cache.GetDynamicWithGroup(ctx, "CronWorkflow", namespace, name, "argoproj.io"); err != nil {
			return nil, workloadParentGetError("cronworkflow", namespace, name, err)
		}
		workflows, err := listWorkflowRuns(ctx, cache, []string{namespace})
		if err != nil {
			return nil, &workloadError{http.StatusInternalServerError, err.Error()}
		}
		for _, workflow := range workflows {
			if cronWorkflowOwnerName(workflow) == name {
				runs = append(runs, workflowRunInfo(workflow))
			}
		}
	case "workflowtemplate", "workflowtemplates":
		if _, err := cache.GetDynamicWithGroup(ctx, "WorkflowTemplate", namespace, name, "argoproj.io"); err != nil {
			return nil, workloadParentGetError("workflowtemplate", namespace, name, err)
		}
		workflows, err := listWorkflowRuns(ctx, cache, []string{namespace})
		if err != nil {
			return nil, &workloadError{http.StatusInternalServerError, err.Error()}
		}
		for _, workflow := range workflows {
			if workflowReferencesTemplate(workflow, namespace, name) {
				runs = append(runs, workflowRunInfo(workflow))
			}
		}
	case "clusterworkflowtemplate", "clusterworkflowtemplates":
		if _, err := cache.GetDynamicWithGroup(ctx, "ClusterWorkflowTemplate", "", name, "argoproj.io"); err != nil {
			return nil, workloadParentGetError("clusterworkflowtemplate", "", name, err)
		}
		workflows, err := listWorkflowRuns(ctx, cache, runNamespaces)
		if err != nil {
			return nil, &workloadError{http.StatusInternalServerError, err.Error()}
		}
		for _, workflow := range workflows {
			if workflowReferencesClusterTemplate(workflow, name) {
				runs = append(runs, workflowRunInfo(workflow))
			}
		}
	case "scaledjob", "scaledjobs":
		if _, err := cache.GetDynamicWithGroup(ctx, "ScaledJob", namespace, name, "keda.sh"); err != nil {
			return nil, workloadParentGetError("scaledjob", namespace, name, err)
		}
		if cache.Jobs() == nil {
			return nil, &workloadError{http.StatusForbidden, "insufficient permissions to list jobs"}
		}
		jobs, err := listJobRuns(cache, []string{namespace})
		if err != nil {
			return nil, &workloadError{http.StatusInternalServerError, err.Error()}
		}
		for _, job := range jobs {
			if controllerOwnerName(job.OwnerReferences, "ScaledJob") == name {
				runs = append(runs, jobRunInfo(job))
			}
		}
	default:
		return nil, &workloadError{http.StatusBadRequest, "only jobs, workflows, cronjobs, cronworkflows, workflowtemplates, clusterworkflowtemplates, and scaledjobs have runs"}
	}

	sortRuns(runs)
	return runs, nil
}

func listJobRuns(cache *k8s.ResourceCache, namespaces []string) ([]*batchv1.Job, error) {
	lister := cache.Jobs()
	var out []*batchv1.Job
	if len(namespaces) == 0 {
		return lister.List(labels.Everything())
	}
	for _, ns := range namespaces {
		items, err := lister.Jobs(ns).List(labels.Everything())
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	return out, nil
}

func listWorkflowRuns(ctx context.Context, cache *k8s.ResourceCache, namespaces []string) ([]*unstructured.Unstructured, error) {
	if len(namespaces) == 0 {
		return cache.ListDynamicWithGroup(ctx, "Workflow", "", "argoproj.io")
	}
	var out []*unstructured.Unstructured
	for _, ns := range namespaces {
		items, err := cache.ListDynamicWithGroup(ctx, "Workflow", ns, "argoproj.io")
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	return out, nil
}

func workloadParentGetError(kind, namespace, name string, err error) *workloadError {
	displayName := name
	if namespace != "" {
		displayName = namespace + "/" + name
	}
	if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
		return &workloadError{http.StatusForbidden, "insufficient permissions to get " + kind + " " + displayName}
	}
	if apierrors.IsNotFound(err) || errors.Is(err, k8score.ErrResourceNotFound) {
		return &workloadError{http.StatusNotFound, kind + " " + displayName + " not found"}
	}
	return &workloadError{http.StatusInternalServerError, "failed to get " + kind + " " + displayName + ": " + err.Error()}
}

func workflowReferencesTemplate(workflow *unstructured.Unstructured, namespace, name string) bool {
	refName, _, _ := unstructured.NestedString(workflow.Object, "spec", "workflowTemplateRef", "name")
	clusterScope, _, _ := unstructured.NestedBool(workflow.Object, "spec", "workflowTemplateRef", "clusterScope")
	if refName != "" {
		return !clusterScope && workflow.GetNamespace() == namespace && refName == name
	}
	return workflow.GetNamespace() == namespace && workflow.GetLabels()["workflows.argoproj.io/workflow-template"] == name
}

func workflowReferencesClusterTemplate(workflow *unstructured.Unstructured, name string) bool {
	refName, _, _ := unstructured.NestedString(workflow.Object, "spec", "workflowTemplateRef", "name")
	clusterScope, _, _ := unstructured.NestedBool(workflow.Object, "spec", "workflowTemplateRef", "clusterScope")
	return clusterScope && refName == name
}

func jobRunInfo(job *batchv1.Job) WorkloadRun {
	completeCondition, complete := jobCondition(job, batchv1.JobComplete)
	failedCondition, failed := jobCondition(job, batchv1.JobFailed)

	phase := "Pending"
	switch {
	case jobIsSuspended(job):
		phase = "Suspended"
	case job.Status.Active > 0:
		phase = "Running"
	case complete:
		phase = "Succeeded"
	case failed:
		phase = "Failed"
	}

	annotations := job.Annotations
	scheduledAt := ""
	trigger := ""
	if annotations["cronjob.kubernetes.io/instantiate"] == "manual" {
		trigger = "manual"
	} else if v := annotations["batch.kubernetes.io/cronjob-scheduled-timestamp"]; v != "" {
		scheduledAt = v
		trigger = "schedule"
	}

	launcher := jobLauncher(job)
	if trigger == "" && launcher != nil && launcher.Kind == "ScaledJob" {
		trigger = "event"
	}
	run := WorkloadRun{
		Kind:         "jobs",
		Namespace:    job.Namespace,
		Name:         job.Name,
		Phase:        phase,
		Active:       phase == "Running" || phase == "Pending",
		StartedAt:    formatMetaTime(job.Status.StartTime),
		ScheduledAt:  scheduledAt,
		Trigger:      trigger,
		Succeeded:    job.Status.Succeeded,
		Failed:       job.Status.Failed,
		Running:      job.Status.Active,
		Desired:      jobDesiredCount(job),
		Parallelism:  jobParallelismCount(job),
		Launcher:     launcher,
		PodSucceeded: int(job.Status.Succeeded),
		PodFailed:    int(job.Status.Failed),
		PodRunning:   int(job.Status.Active),
	}
	run.PodTotal = run.PodSucceeded + run.PodFailed + run.PodRunning
	if run.Desired > 0 {
		run.Progress = fmt.Sprintf("%d/%d", run.Succeeded, run.Desired)
	}
	if complete {
		applyJobCondition(&run, completeCondition)
	} else if failed {
		applyJobCondition(&run, failedCondition)
	}
	return run
}

func jobIsSuspended(job *batchv1.Job) bool {
	if job.Spec.Suspend != nil && *job.Spec.Suspend {
		return true
	}
	for _, condition := range job.Status.Conditions {
		if string(condition.Type) == "Suspended" && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func jobLauncher(job *batchv1.Job) *WorkloadRunResourceRef {
	for _, owner := range job.OwnerReferences {
		if owner.Controller == nil || !*owner.Controller {
			continue
		}
		switch owner.Kind {
		case "CronJob":
			return &WorkloadRunResourceRef{Kind: owner.Kind, Namespace: job.Namespace, Name: owner.Name, Group: "batch"}
		case "ScaledJob":
			return &WorkloadRunResourceRef{Kind: owner.Kind, Namespace: job.Namespace, Name: owner.Name, Group: "keda.sh"}
		}
	}
	return nil
}

func jobCondition(job *batchv1.Job, conditionType batchv1.JobConditionType) (batchv1.JobCondition, bool) {
	for _, condition := range job.Status.Conditions {
		if condition.Type == conditionType && condition.Status == corev1.ConditionTrue {
			return condition, true
		}
	}
	return batchv1.JobCondition{}, false
}

func applyJobCondition(run *WorkloadRun, condition batchv1.JobCondition) {
	run.FinishedAt = condition.LastTransitionTime.Format(time.RFC3339)
	if condition.Message != "" {
		run.Message = condition.Message
	}
}

func jobDesiredCount(job *batchv1.Job) int32 {
	if job.Spec.Completions != nil {
		return *job.Spec.Completions
	}
	return 1
}

func jobParallelismCount(job *batchv1.Job) int32 {
	if job.Spec.Parallelism != nil {
		return *job.Spec.Parallelism
	}
	return 1
}

func workflowRunInfo(workflow *unstructured.Unstructured) WorkloadRun {
	phase, _, _ := unstructured.NestedString(workflow.Object, "status", "phase")
	startedAt, _, _ := unstructured.NestedString(workflow.Object, "status", "startedAt")
	finishedAt, _, _ := unstructured.NestedString(workflow.Object, "status", "finishedAt")
	message, _, _ := unstructured.NestedString(workflow.Object, "status", "message")
	progress, _, _ := unstructured.NestedString(workflow.Object, "status", "progress")
	template, _, _ := unstructured.NestedString(workflow.Object, "spec", "workflowTemplateRef", "name")
	if phase == "" {
		phase = "Pending"
	}
	scheduledAt := workflow.GetAnnotations()["workflows.argoproj.io/scheduled-time"]
	trigger := ""
	if scheduledAt != "" {
		trigger = "schedule"
	}
	run := WorkloadRun{
		Kind:        "workflows",
		Namespace:   workflow.GetNamespace(),
		Name:        workflow.GetName(),
		Phase:       phase,
		Active:      phase == "Running" || phase == "Pending",
		StartedAt:   startedAt,
		FinishedAt:  finishedAt,
		ScheduledAt: scheduledAt,
		Trigger:     trigger,
		Message:     message,
		Progress:    progress,
		Template:    template,
		Launcher:    workflowLauncher(workflow),
	}
	applyWorkflowPodCounts(&run, workflow)
	return run
}

func workflowLauncher(workflow *unstructured.Unstructured) *WorkloadRunResourceRef {
	if name := cronWorkflowOwnerName(workflow); name != "" {
		return &WorkloadRunResourceRef{Kind: "CronWorkflow", Namespace: workflow.GetNamespace(), Name: name, Group: "argoproj.io"}
	}
	return nil
}

func applyWorkflowPodCounts(run *WorkloadRun, workflow *unstructured.Unstructured) {
	nodes, found, _ := unstructured.NestedMap(workflow.Object, "status", "nodes")
	if !found {
		return
	}
	for _, raw := range nodes {
		node, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		nodeType, _ := node["type"].(string)
		nodePhase, _ := node["phase"].(string)
		if nodeType == "Pod" {
			run.PodTotal++
			switch nodePhase {
			case "Succeeded":
				run.PodSucceeded++
			case "Failed", "Error":
				run.PodFailed++
			case "Running":
				run.PodRunning++
			case "Pending":
				run.PodPending++
			}
		}
	}
}

func sortRuns(runs []WorkloadRun) {
	sort.SliceStable(runs, func(i, j int) bool {
		return runComesBefore(runs[i], runs[j])
	})
}

func runComesBefore(a, b WorkloadRun) bool {
	if a.Active != b.Active {
		return a.Active
	}
	if !runSortTime(a).Equal(runSortTime(b)) {
		return runSortTime(a).After(runSortTime(b))
	}
	if runPhaseRank(a.Phase) != runPhaseRank(b.Phase) {
		return runPhaseRank(a.Phase) < runPhaseRank(b.Phase)
	}
	return a.Name < b.Name
}

func runPhaseRank(phase string) int {
	switch phase {
	case "Failed", "Error":
		return 0
	case "Running", "Pending":
		return 1
	case "Succeeded":
		return 2
	default:
		return 3
	}
}

func runSortTime(run WorkloadRun) time.Time {
	var out time.Time
	for _, value := range []string{run.StartedAt, run.ScheduledAt, run.FinishedAt} {
		if value == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, value); err == nil && t.After(out) {
			out = t
		}
	}
	return out
}

func formatMetaTime(t *metav1.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.RFC3339)
}

func addWorkloadLogMetadata(response map[string]any, metadata workloadLogMetadata) {
	if metadata.EmptyReason != "" {
		response["emptyReason"] = metadata.EmptyReason
	}
	if metadata.EmptyMessage != "" {
		response["emptyMessage"] = metadata.EmptyMessage
	}
	if metadata.Command != "" {
		response["command"] = metadata.Command
	}
}

func (s *Server) describeWorkloadLogEmpty(ctx context.Context, kind, namespace, name string) workloadLogMetadata {
	switch kind {
	case "job", "jobs":
		return s.describeJobLogEmpty(namespace, name)
	case "workflow", "workflows":
		return s.describeWorkflowLogEmpty(ctx, namespace, name)
	default:
		return workloadLogMetadata{
			EmptyReason:  "no-pods",
			EmptyMessage: "No pods found for this workload.",
		}
	}
}

func (s *Server) describeJobLogEmpty(namespace, name string) workloadLogMetadata {
	metadata := workloadLogMetadata{
		EmptyReason:  "no-pods",
		EmptyMessage: "No pods found for this Job yet. Check the Timeline tab for scheduling or admission events.",
		Command:      "kubectl logs job/" + name + " -n " + namespace,
	}
	cache := k8s.GetResourceCache()
	if cache == nil || cache.Jobs() == nil {
		return metadata
	}
	job, err := cache.Jobs().Jobs(namespace).Get(name)
	if err != nil {
		return metadata
	}
	applyTerminalJobEmptyState(&metadata, job, namespace, name)
	return metadata
}

func applyTerminalJobEmptyState(metadata *workloadLogMetadata, job *batchv1.Job, namespace, name string) {
	if !k8s.IsJobTerminal(job) {
		return
	}
	metadata.EmptyReason = "pods-gone"
	metadata.EmptyMessage = "This Job has finished, but its pods are no longer present in Kubernetes. If logs were retained externally, use your logging system; otherwise inspect the Job conditions and events."
	metadata.Command = "kubectl describe job/" + name + " -n " + namespace
}

func (s *Server) describeWorkflowLogEmpty(ctx context.Context, namespace, name string) workloadLogMetadata {
	metadata := workloadLogMetadata{
		EmptyReason:  "no-pods",
		EmptyMessage: "No Workflow pods found yet. Check the Timeline tab for scheduling or admission events.",
		Command:      "argo logs " + name + " -n " + namespace,
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		return metadata
	}
	workflow, err := cache.GetDynamicWithGroup(ctx, "Workflow", namespace, name, "argoproj.io")
	if err != nil {
		return metadata
	}
	applyTerminalWorkflowEmptyState(&metadata, workflow.Object, namespace, name)
	return metadata
}

func applyTerminalWorkflowEmptyState(metadata *workloadLogMetadata, workflow map[string]any, namespace, name string) {
	if !k8s.IsWorkflowTerminal(workflow) {
		return
	}
	metadata.EmptyReason = "pods-gone"
	if k8s.WorkflowArchiveLogsConfigured(workflow) {
		metadata.EmptyMessage = "This Workflow has finished and its pods are no longer present. Archived logs appear to be enabled; use the configured Argo or logging UI, or try argo logs " + name + " -n " + namespace + "."
	} else {
		metadata.EmptyMessage = "This Workflow has finished and its pods are no longer present. Argo may have garbage-collected them; Kubernetes pod logs are no longer available here."
	}
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

// collectLogsFromPods fetches logs from all pods concurrently. Non-nil even
// when nothing is retrievable (e.g. every pod is crashlooping) — a nil slice
// marshals as JSON null and consumers expect an array.
func collectLogsFromPods(ctx context.Context, client kubernetes.Interface, namespace string, pods []*corev1.Pod, container string, tailLines int64, sinceSeconds *int64) []workloadLogEntry {
	allLogs := []workloadLogEntry{}
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
func fetchPodContainerLogs(ctx context.Context, client kubernetes.Interface, namespace, podName, containerName string, tailLines int64, sinceSeconds *int64) []workloadLogEntry {
	stream, err := k8score.GetContainerLogs(ctx, client, namespace, podName, containerName, k8score.LogOptions{
		TailLines:    &tailLines,
		SinceSeconds: sinceSeconds,
		Timestamps:   true,
	})
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
