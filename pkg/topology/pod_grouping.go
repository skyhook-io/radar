package topology

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// PodGroup represents a collection of pods grouped by app label or owner
type PodGroup struct {
	Key        string          // Unique key: namespace/groupKind/groupName
	GroupKind  string          // "app", "ReplicaSet", "DaemonSet", etc.
	GroupName  string          // App name or owner name
	Namespace  string          // Namespace of the pods
	Pods       []*corev1.Pod   // Pods in this group
	ServiceIDs map[string]bool // Service IDs that route to this group (for traffic view)
	Healthy    int             // Count of healthy pods
	Degraded   int             // Count of degraded pods
	Unhealthy  int             // Count of unhealthy pods
}

// PodGroupingResult contains the result of grouping pods
type PodGroupingResult struct {
	Groups map[string]*PodGroup // Grouped pods by key
}

// PodGroupingOptions configures pod grouping behavior
type PodGroupingOptions struct {
	Namespaces      []string                              // Filter to specific namespaces (empty = all)
	ServiceMatching bool                                  // Whether to match pods to services (for traffic view)
	ServicesByNS    map[string]map[string]*corev1.Service // Namespace -> svcKey -> service
	ServiceIDs      map[string]string                     // svcKey -> serviceID
}

// matchesNamespaceFilter returns true if the given namespace matches the filter.
func (opts PodGroupingOptions) matchesNamespaceFilter(ns string) bool {
	return MatchesNamespace(opts.Namespaces, ns)
}

// GroupPods groups pods by run identity, app label, or owner reference
func GroupPods(pods []*corev1.Pod, opts PodGroupingOptions) *PodGroupingResult {
	result := &PodGroupingResult{
		Groups: make(map[string]*PodGroup),
	}

	for _, pod := range pods {
		if !opts.matchesNamespaceFilter(pod.Namespace) {
			continue
		}

		// For traffic view, find matching services first
		var matchingServiceIDs []string
		if opts.ServiceMatching && opts.ServicesByNS != nil {
			for svcKey, svc := range opts.ServicesByNS[pod.Namespace] {
				if matchesSelector(pod.Labels, svc.Spec.Selector) {
					if svcID, ok := opts.ServiceIDs[svcKey]; ok {
						matchingServiceIDs = append(matchingServiceIDs, svcID)
					}
				}
			}
			// Skip pods with no service connections in traffic view
			if len(matchingServiceIDs) == 0 {
				continue
			}
		}

		// Determine group key
		groupKey, groupKind, groupName := determineGroupKey(pod)

		if _, exists := result.Groups[groupKey]; !exists {
			result.Groups[groupKey] = &PodGroup{
				Key:        groupKey,
				GroupKind:  groupKind,
				GroupName:  groupName,
				Namespace:  pod.Namespace,
				Pods:       make([]*corev1.Pod, 0),
				ServiceIDs: make(map[string]bool),
			}
		}

		group := result.Groups[groupKey]
		group.Pods = append(group.Pods, pod)

		// Track services (for traffic view)
		for _, svcID := range matchingServiceIDs {
			group.ServiceIDs[svcID] = true
		}

		// Track health
		status := podSummaryStatus(pod)
		switch status {
		case StatusHealthy:
			group.Healthy++
		case StatusDegraded:
			group.Degraded++
		default:
			group.Unhealthy++
		}
	}

	return result
}

// determineGroupKey determines the group key, kind, and name for a pod
func determineGroupKey(pod *corev1.Pod) (key, kind, name string) {
	if workflowName := pod.Labels["workflows.argoproj.io/workflow"]; workflowName != "" {
		return fmt.Sprintf("%s/Workflow/%s", pod.Namespace, workflowName), "Workflow", workflowName
	}
	if jobName := pod.Labels["batch.kubernetes.io/job-name"]; jobName != "" {
		return fmt.Sprintf("%s/Job/%s", pod.Namespace, jobName), "Job", jobName
	}
	if jobName := pod.Labels["job-name"]; jobName != "" {
		return fmt.Sprintf("%s/Job/%s", pod.Namespace, jobName), "Job", jobName
	}

	// First try app labels (groups all pods of the same app together)
	if appName := pod.Labels["app.kubernetes.io/name"]; appName != "" {
		return fmt.Sprintf("%s/app/%s", pod.Namespace, appName), "app", appName
	}
	if appName := pod.Labels["app"]; appName != "" {
		return fmt.Sprintf("%s/app/%s", pod.Namespace, appName), "app", appName
	}

	// Fall back to owner reference
	ownerKind := "standalone"
	ownerName := pod.Name
	for _, ref := range pod.OwnerReferences {
		if ref.Controller != nil && *ref.Controller {
			ownerKind = ref.Kind
			ownerName = ref.Name
			break
		}
	}
	return fmt.Sprintf("%s/%s/%s", pod.Namespace, ownerKind, ownerName), ownerKind, ownerName
}

// ComputeGroupStatus determines the overall health status of a pod group
func ComputeGroupStatus(group *PodGroup) HealthStatus {
	if group.Unhealthy > 0 {
		return StatusUnhealthy
	}
	if group.Degraded > 0 {
		return StatusDegraded
	}
	return StatusHealthy
}

// ComputePodRestarts calculates total restarts for a pod
func ComputePodRestarts(pod *corev1.Pod) int32 {
	restarts := int32(0)
	for _, cs := range pod.Status.ContainerStatuses {
		restarts += cs.RestartCount
	}
	return restarts
}

// CreatePodNode creates a Node for a single pod
func CreatePodNode(pod *corev1.Pod, provider ResourceProvider, includeNodeName bool) Node {
	podID := fmt.Sprintf("pod/%s/%s", pod.Namespace, pod.Name)
	restarts := ComputePodRestarts(pod)

	// Get status issue from provider
	statusIssue := ""
	if provider != nil {
		if resourceStatus := provider.GetResourceStatus("Pod", pod.Namespace, pod.Name); resourceStatus != nil {
			statusIssue = resourceStatus.Issue
		}
	}

	data := map[string]any{
		"namespace":   pod.Namespace,
		"phase":       string(pod.Status.Phase),
		"restarts":    restarts,
		"containers":  len(pod.Spec.Containers),
		"labels":      pod.Labels,
		"statusIssue": statusIssue,
	}

	// Only include nodeName for resources view (not traffic view)
	if includeNodeName {
		data["nodeName"] = pod.Spec.NodeName
	}

	return Node{
		ID:     podID,
		Kind:   KindPod,
		Name:   pod.Name,
		Status: getPodStatus(pod),
		Data:   data,
	}
}

// PodDetail represents pod details for PodGroup expansion
type PodDetail struct {
	Name        string `json:"name"`
	Namespace   string `json:"namespace"`
	Phase       string `json:"phase"`
	Restarts    int32  `json:"restarts"`
	Containers  int    `json:"containers"`
	StatusIssue string `json:"statusIssue"`
}

// CreatePodGroupNode creates a Node for a group of pods
func CreatePodGroupNode(group *PodGroup, provider ResourceProvider) Node {
	podGroupID := fmt.Sprintf("podgroup-%s", strings.ReplaceAll(group.Key, "/", "-"))

	// Determine display name
	groupName := group.GroupName
	if groupName == "" {
		groupName = "pods"
	}

	// Build pod details and collect issues
	podDetails := make([]map[string]any, 0, len(group.Pods))
	totalRestarts := int32(0)
	groupStatusIssue := ""

	for _, pod := range group.Pods {
		restarts := ComputePodRestarts(pod)
		totalRestarts += restarts

		// Get pod issue
		podIssue := ""
		if provider != nil {
			if resourceStatus := provider.GetResourceStatus("Pod", pod.Namespace, pod.Name); resourceStatus != nil {
				podIssue = resourceStatus.Issue
				// Use first issue found as group issue
				if groupStatusIssue == "" && podIssue != "" {
					groupStatusIssue = podIssue
				}
			}
		}

		podDetails = append(podDetails, map[string]any{
			"name":        pod.Name,
			"namespace":   pod.Namespace,
			"phase":       string(pod.Status.Phase),
			"restarts":    restarts,
			"containers":  len(pod.Spec.Containers),
			"statusIssue": podIssue,
		})
	}

	return Node{
		ID:     podGroupID,
		Kind:   KindPodGroup,
		Name:   groupName,
		Status: ComputeGroupStatus(group),
		Data: map[string]any{
			"namespace":     group.Namespace,
			"ownerKind":     group.GroupKind,
			"podCount":      len(group.Pods),
			"healthy":       group.Healthy,
			"degraded":      group.Degraded,
			"unhealthy":     group.Unhealthy,
			"totalRestarts": totalRestarts,
			"pods":          podDetails,
			"statusIssue":   groupStatusIssue,
		},
	}
}

// CreateOrphanPodSummaryNode creates a single summary-only node for pods that
// couldn't be attributed to a workload in summary mode (standalone pods, bare
// ReplicaSets, or controllers whose node wasn't created — e.g. RBAC-denied).
// It carries counts/status/restarts only — NO per-pod "pods" array and no
// expand affordance — so a large orphan set can't re-introduce the pod-tier
// payload/render cost that summary mode exists to avoid.
func CreateOrphanPodSummaryNode(namespace string, summary PodSummary, totalRestarts int32) Node {
	status := StatusHealthy
	if summary.Unhealthy > 0 {
		status = StatusUnhealthy
	} else if summary.Degraded > 0 {
		status = StatusDegraded
	}
	return Node{
		ID:     "podgroup-orphans-" + namespace,
		Kind:   KindPodGroup,
		Name:   "unattributed pods",
		Status: status,
		Data: map[string]any{
			"namespace":     namespace,
			"podCount":      summary.Total,
			"healthy":       summary.Healthy,
			"degraded":      summary.Degraded,
			"unhealthy":     summary.Unhealthy,
			"totalRestarts": totalRestarts,
			"summaryOnly":   true,
		},
	}
}

// GetPodGroupID returns the node ID for a pod group
func GetPodGroupID(group *PodGroup) string {
	return fmt.Sprintf("podgroup-%s", strings.ReplaceAll(group.Key, "/", "-"))
}

// GetPodID returns the node ID for a pod
func GetPodID(pod *corev1.Pod) string {
	return fmt.Sprintf("pod/%s/%s", pod.Namespace, pod.Name)
}
