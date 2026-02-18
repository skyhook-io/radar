package ai

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
)

const maxContextLen = 8000 // Max characters for K8s context in system prompt

// BuildSystemPrompt constructs the system prompt with K8s context
func BuildSystemPrompt(ctx context.Context, resourceCtx *ResourceContext) string {
	var sb strings.Builder

	sb.WriteString("You are Radar AI, an expert Kubernetes assistant integrated into the Radar dashboard. ")
	sb.WriteString("You help users understand their cluster state, troubleshoot issues, and manage resources.\n\n")
	sb.WriteString("Guidelines:\n")
	sb.WriteString("- Be concise and actionable\n")
	sb.WriteString("- Reference specific resources by name when possible\n")
	sb.WriteString("- Suggest kubectl commands when relevant\n")
	sb.WriteString("- Format code blocks with ```\n")
	sb.WriteString("- If you don't have enough info, ask the user to provide more context\n\n")

	// Add cluster context
	clusterCtx := gatherClusterContext(ctx)
	if clusterCtx != "" {
		sb.WriteString("## Current Cluster State\n\n")
		sb.WriteString(clusterCtx)
		sb.WriteString("\n")
	}

	// Add resource-specific context
	if resourceCtx != nil && resourceCtx.Kind != "" && resourceCtx.Name != "" {
		resCtx := gatherResourceContext(ctx, resourceCtx)
		if resCtx != "" {
			sb.WriteString("\n## Focused Resource\n\n")
			sb.WriteString(resCtx)
			sb.WriteString("\n")
		}
	}

	return sb.String()
}

// gatherClusterContext collects general cluster information
func gatherClusterContext(ctx context.Context) string {
	var parts []string

	// Cluster info
	info, err := k8s.GetClusterInfo(ctx)
	if err == nil && info != nil {
		parts = append(parts, fmt.Sprintf("Cluster: %s (platform: %s, version: %s)",
			info.Cluster, info.Platform, info.KubernetesVersion))
	}

	// Node summary
	cache := k8s.GetResourceCache()
	if cache != nil {
		if nodeLister := cache.Nodes(); nodeLister != nil {
			nodes, _ := nodeLister.List(labels.Everything())
			ready := 0
			for _, n := range nodes {
				for _, cond := range n.Status.Conditions {
					if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
						ready++
						break
					}
				}
			}
			parts = append(parts, fmt.Sprintf("Nodes: %d total, %d ready", len(nodes), ready))
		}

		// Pod summary
		if podLister := cache.Pods(); podLister != nil {
			pods, _ := podLister.List(labels.Everything())
			running, pending, failed := 0, 0, 0
			var crashLooping []string
			for _, pod := range pods {
				switch pod.Status.Phase {
				case corev1.PodRunning:
					running++
				case corev1.PodPending:
					pending++
				case corev1.PodFailed:
					failed++
				}
				// Detect CrashLoopBackOff
				for _, cs := range pod.Status.ContainerStatuses {
					if cs.State.Waiting != nil && cs.State.Waiting.Reason == "CrashLoopBackOff" {
						crashLooping = append(crashLooping, fmt.Sprintf("%s/%s", pod.Namespace, pod.Name))
						break
					}
				}
			}
			parts = append(parts, fmt.Sprintf("Pods: %d running, %d pending, %d failed", running, pending, failed))
			if len(crashLooping) > 0 {
				limit := 5
				if len(crashLooping) < limit {
					limit = len(crashLooping)
				}
				parts = append(parts, fmt.Sprintf("CrashLoopBackOff pods: %s", strings.Join(crashLooping[:limit], ", ")))
			}
		}
	}

	// Recent warning events
	if store := timeline.GetStore(); store != nil {
		opts := timeline.QueryOptions{
			Since:            time.Now().Add(-30 * time.Minute),
			Limit:            10,
			IncludeK8sEvents: true,
			FilterPreset:     "warnings-only",
		}
		events, err := store.Query(ctx, opts)
		if err == nil && len(events) > 0 {
			parts = append(parts, "\nRecent warnings:")
			for _, e := range events {
				msg := e.Message
				if len(msg) > 120 {
					msg = msg[:117] + "..."
				}
				parts = append(parts, fmt.Sprintf("- [%s] %s/%s: %s", e.Kind, e.Namespace, e.Name, msg))
			}
		}
	}

	result := strings.Join(parts, "\n")
	if len(result) > maxContextLen {
		result = result[:maxContextLen-3] + "..."
	}
	return result
}

// gatherResourceContext collects context about a specific resource
func gatherResourceContext(ctx context.Context, rc *ResourceContext) string {
	var parts []string

	cache := k8s.GetResourceCache()
	if cache == nil {
		return ""
	}

	parts = append(parts, fmt.Sprintf("Kind: %s, Name: %s, Namespace: %s", rc.Kind, rc.Name, rc.Namespace))

	kind := strings.ToLower(rc.Kind)

	// Get pod details for pod-related queries
	if kind == "pod" || kind == "pods" {
		if podLister := cache.Pods(); podLister != nil {
			pod, err := podLister.Pods(rc.Namespace).Get(rc.Name)
			if err == nil {
				parts = append(parts, fmt.Sprintf("Phase: %s", pod.Status.Phase))

				// Container statuses
				for _, cs := range pod.Status.ContainerStatuses {
					status := "Running"
					if cs.State.Waiting != nil {
						status = fmt.Sprintf("Waiting (%s)", cs.State.Waiting.Reason)
						if cs.State.Waiting.Message != "" {
							status += ": " + truncate(cs.State.Waiting.Message, 150)
						}
					} else if cs.State.Terminated != nil {
						status = fmt.Sprintf("Terminated (%s, exit: %d)", cs.State.Terminated.Reason, cs.State.Terminated.ExitCode)
					}
					parts = append(parts, fmt.Sprintf("Container %s: %s, restarts: %d", cs.Name, status, cs.RestartCount))
				}

				// Pod events
				if eventLister := cache.Events(); eventLister != nil {
					events, _ := eventLister.Events(rc.Namespace).List(labels.Everything())
					var podEvents []string
					for _, e := range events {
						if e.InvolvedObject.Name == rc.Name && e.InvolvedObject.Kind == "Pod" {
							ts := e.LastTimestamp.Time
							if ts.IsZero() {
								ts = e.CreationTimestamp.Time
							}
							podEvents = append(podEvents, fmt.Sprintf("  [%s] %s: %s (%s)",
								e.Type, e.Reason, truncate(e.Message, 100), formatAge(time.Since(ts))))
						}
					}
					if len(podEvents) > 0 {
						limit := 10
						if len(podEvents) < limit {
							limit = len(podEvents)
						}
						parts = append(parts, "\nRecent events:")
						parts = append(parts, podEvents[:limit]...)
					}
				}

				// Get last few log lines
				logs := getPodLogs(ctx, rc.Namespace, rc.Name, pod)
				if logs != "" {
					parts = append(parts, "\nRecent logs:")
					parts = append(parts, logs)
				}
			}
		}
	}

	result := strings.Join(parts, "\n")
	if len(result) > maxContextLen {
		result = result[:maxContextLen-3] + "..."
	}
	return result
}

// getPodLogs fetches the last N lines of logs from a pod
func getPodLogs(ctx context.Context, namespace, name string, pod *corev1.Pod) string {
	clientset := k8s.GetClient()
	if clientset == nil {
		return ""
	}

	// Get logs from the first non-init container
	container := ""
	for _, cs := range pod.Status.ContainerStatuses {
		container = cs.Name
		break
	}
	if container == "" && len(pod.Spec.Containers) > 0 {
		container = pod.Spec.Containers[0].Name
	}
	if container == "" {
		return ""
	}

	tailLines := int64(30)
	logOpts := &corev1.PodLogOptions{
		Container: container,
		TailLines: &tailLines,
	}

	result := clientset.CoreV1().Pods(namespace).GetLogs(name, logOpts)
	stream, err := result.Stream(ctx)
	if err != nil {
		log.Printf("[ai-context] Failed to get logs for %s/%s: %v", namespace, name, err)
		return ""
	}
	defer stream.Close()

	buf := make([]byte, 4096)
	n, _ := stream.Read(buf)
	if n == 0 {
		return ""
	}

	logs := string(buf[:n])
	if len(logs) > 2000 {
		logs = logs[len(logs)-2000:]
	}
	return "```\n" + logs + "\n```"
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

func formatAge(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
