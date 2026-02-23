package mcp

import (
	"context"
	"fmt"
	"io"
	"log"
	"sort"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	aicontext "github.com/skyhook-io/radar/internal/ai/context"
	"github.com/skyhook-io/radar/internal/k8s"
)

// Workload tool input types

type manageWorkloadInput struct {
	Action    string `json:"action" jsonschema:"action to perform: restart, scale, or rollback"`
	Kind      string `json:"kind" jsonschema:"workload kind: deployment, statefulset, or daemonset"`
	Namespace string `json:"namespace" jsonschema:"workload namespace"`
	Name      string `json:"name" jsonschema:"workload name"`
	Replicas  *int32 `json:"replicas,omitempty" jsonschema:"target replica count (required for scale)"`
	Revision  *int64 `json:"revision,omitempty" jsonschema:"target revision number (required for rollback)"`
}

type manageCronJobInput struct {
	Action    string `json:"action" jsonschema:"action to perform: trigger, suspend, or resume"`
	Namespace string `json:"namespace" jsonschema:"cronjob namespace"`
	Name      string `json:"name" jsonschema:"cronjob name"`
}

type getWorkloadLogsInput struct {
	Kind      string `json:"kind" jsonschema:"workload kind: deployment, statefulset, or daemonset"`
	Namespace string `json:"namespace" jsonschema:"workload namespace"`
	Name      string `json:"name" jsonschema:"workload name"`
	Container string `json:"container,omitempty" jsonschema:"specific container name, defaults to all containers"`
	TailLines int    `json:"tail_lines,omitempty" jsonschema:"lines per pod (default 100)"`
}

// Workload tool handlers

func handleManageWorkload(ctx context.Context, req *mcp.CallToolRequest, input manageWorkloadInput) (*mcp.CallToolResult, any, error) {
	kind := normalizeWorkloadKind(input.Kind)
	if kind == "" {
		return nil, nil, fmt.Errorf("invalid kind %q: must be deployment, statefulset, or daemonset", input.Kind)
	}

	switch strings.ToLower(input.Action) {
	case "restart":
		if err := k8s.RestartWorkload(ctx, kind, input.Namespace, input.Name); err != nil {
			return nil, nil, fmt.Errorf("restart failed: %w", err)
		}
		return toJSONResult(map[string]string{
			"status":  "ok",
			"message": fmt.Sprintf("Rolling restart initiated for %s %s/%s", kind, input.Namespace, input.Name),
		})

	case "scale":
		if input.Replicas == nil {
			return nil, nil, fmt.Errorf("replicas is required for scale action")
		}
		if kind == "daemonsets" {
			return nil, nil, fmt.Errorf("scaling is not supported for DaemonSets (only Deployments and StatefulSets)")
		}
		if err := k8s.ScaleWorkload(ctx, kind, input.Namespace, input.Name, *input.Replicas); err != nil {
			return nil, nil, fmt.Errorf("scale failed: %w", err)
		}
		return toJSONResult(map[string]any{
			"status":   "ok",
			"message":  fmt.Sprintf("Scaled %s %s/%s to %d replicas", kind, input.Namespace, input.Name, *input.Replicas),
			"replicas": *input.Replicas,
		})

	case "rollback":
		if input.Revision == nil {
			return nil, nil, fmt.Errorf("revision is required for rollback action")
		}
		if err := k8s.RollbackWorkload(ctx, kind, input.Namespace, input.Name, *input.Revision); err != nil {
			return nil, nil, fmt.Errorf("rollback failed: %w", err)
		}
		return toJSONResult(map[string]any{
			"status":   "ok",
			"message":  fmt.Sprintf("Rolled back %s %s/%s to revision %d", kind, input.Namespace, input.Name, *input.Revision),
			"revision": *input.Revision,
		})

	default:
		return nil, nil, fmt.Errorf("unknown action %q: must be restart, scale, or rollback", input.Action)
	}
}

func handleManageCronJob(ctx context.Context, req *mcp.CallToolRequest, input manageCronJobInput) (*mcp.CallToolResult, any, error) {
	switch strings.ToLower(input.Action) {
	case "trigger":
		job, err := k8s.TriggerCronJob(ctx, input.Namespace, input.Name)
		if err != nil {
			return nil, nil, fmt.Errorf("trigger failed: %w", err)
		}
		return toJSONResult(map[string]string{
			"status":  "ok",
			"message": fmt.Sprintf("Triggered manual job from CronJob %s/%s", input.Namespace, input.Name),
			"jobName": job.GetName(),
		})

	case "suspend":
		if err := k8s.SetCronJobSuspend(ctx, input.Namespace, input.Name, true); err != nil {
			return nil, nil, fmt.Errorf("suspend failed: %w", err)
		}
		return toJSONResult(map[string]string{
			"status":  "ok",
			"message": fmt.Sprintf("Suspended CronJob %s/%s", input.Namespace, input.Name),
		})

	case "resume":
		if err := k8s.SetCronJobSuspend(ctx, input.Namespace, input.Name, false); err != nil {
			return nil, nil, fmt.Errorf("resume failed: %w", err)
		}
		return toJSONResult(map[string]string{
			"status":  "ok",
			"message": fmt.Sprintf("Resumed CronJob %s/%s", input.Namespace, input.Name),
		})

	default:
		return nil, nil, fmt.Errorf("unknown action %q: must be trigger, suspend, or resume", input.Action)
	}
}

func handleGetWorkloadLogs(ctx context.Context, req *mcp.CallToolRequest, input getWorkloadLogsInput) (*mcp.CallToolResult, any, error) {
	kind := normalizeWorkloadKind(input.Kind)
	if kind == "" {
		return nil, nil, fmt.Errorf("invalid kind %q: must be deployment, statefulset, or daemonset", input.Kind)
	}

	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil, nil, fmt.Errorf("not connected to cluster")
	}

	client := k8s.GetClient()
	if client == nil {
		return nil, nil, fmt.Errorf("not connected to cluster")
	}

	// Get the workload's label selector
	selector, err := getWorkloadSelectorFromCache(cache, kind, input.Namespace, input.Name)
	if err != nil {
		return nil, nil, err
	}

	// Get pods matching the workload
	pods := cache.GetPodsForWorkload(input.Namespace, selector)
	if len(pods) == 0 {
		return toJSONResult(map[string]any{
			"workload": fmt.Sprintf("%s/%s/%s", kind, input.Namespace, input.Name),
			"pods":     0,
			"logs":     "no pods found for this workload",
		})
	}

	tailLines := int64(100)
	if input.TailLines > 0 {
		tailLines = int64(input.TailLines)
	}

	// Validate container name if specified
	if input.Container != "" {
		found := false
		for _, pod := range pods {
			for _, c := range pod.Spec.Containers {
				if c.Name == input.Container {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			return nil, nil, fmt.Errorf("container %q not found in any pod of %s %s/%s", input.Container, kind, input.Namespace, input.Name)
		}
	}

	// Collect logs from all pods concurrently
	type logEntry struct {
		Pod       string                 `json:"pod"`
		Container string                 `json:"container"`
		Logs      aicontext.FilteredLogs `json:"logs,omitempty"`
		Error     string                 `json:"error,omitempty"`
	}

	var allLogs []logEntry
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, pod := range pods {
		containers := getContainersForPod(pod, input.Container)
		for _, c := range containers {
			wg.Add(1)
			go func(podName, containerName string) {
				defer wg.Done()

				opts := &corev1.PodLogOptions{
					Container:  containerName,
					TailLines:  &tailLines,
					Timestamps: true,
				}

				entry := logEntry{
					Pod:       podName,
					Container: containerName,
				}

				stream, err := client.CoreV1().Pods(input.Namespace).GetLogs(podName, opts).Stream(ctx)
				if err != nil {
					log.Printf("[mcp] Failed to get logs for %s/%s: %v", podName, containerName, err)
					entry.Error = fmt.Sprintf("failed to get logs: %v", err)
					mu.Lock()
					allLogs = append(allLogs, entry)
					mu.Unlock()
					return
				}
				defer stream.Close()

				data, err := io.ReadAll(stream)
				if err != nil {
					log.Printf("[mcp] Failed to read logs for %s/%s: %v", podName, containerName, err)
					entry.Error = fmt.Sprintf("failed to read logs: %v", err)
					mu.Lock()
					allLogs = append(allLogs, entry)
					mu.Unlock()
					return
				}

				// Apply AI-optimized log filtering
				entry.Logs = aicontext.FilterLogs(string(data))

				mu.Lock()
				allLogs = append(allLogs, entry)
				mu.Unlock()
			}(pod.Name, c)
		}
	}

	wg.Wait()

	// Sort by pod name for deterministic output
	sort.Slice(allLogs, func(i, j int) bool {
		if allLogs[i].Pod != allLogs[j].Pod {
			return allLogs[i].Pod < allLogs[j].Pod
		}
		return allLogs[i].Container < allLogs[j].Container
	})

	return toJSONResult(map[string]any{
		"workload": fmt.Sprintf("%s/%s/%s", kind, input.Namespace, input.Name),
		"pods":     len(pods),
		"logs":     allLogs,
	})
}

// getWorkloadSelectorFromCache returns the label selector for a workload from cache.
func getWorkloadSelectorFromCache(cache *k8s.ResourceCache, kind, namespace, name string) (*metav1.LabelSelector, error) {
	switch kind {
	case "deployments":
		lister := cache.Deployments()
		if lister == nil {
			return nil, fmt.Errorf("insufficient permissions to list deployments")
		}
		dep, err := lister.Deployments(namespace).Get(name)
		if err != nil {
			return nil, fmt.Errorf("deployment %s/%s not found: %w", namespace, name, err)
		}
		return dep.Spec.Selector, nil

	case "statefulsets":
		lister := cache.StatefulSets()
		if lister == nil {
			return nil, fmt.Errorf("insufficient permissions to list statefulsets")
		}
		sts, err := lister.StatefulSets(namespace).Get(name)
		if err != nil {
			return nil, fmt.Errorf("statefulset %s/%s not found: %w", namespace, name, err)
		}
		return sts.Spec.Selector, nil

	case "daemonsets":
		lister := cache.DaemonSets()
		if lister == nil {
			return nil, fmt.Errorf("insufficient permissions to list daemonsets")
		}
		ds, err := lister.DaemonSets(namespace).Get(name)
		if err != nil {
			return nil, fmt.Errorf("daemonset %s/%s not found: %w", namespace, name, err)
		}
		return ds.Spec.Selector, nil

	default:
		return nil, fmt.Errorf("unsupported workload kind: %s", kind)
	}
}

// getContainersForPod returns containers to fetch logs from.
func getContainersForPod(pod *corev1.Pod, selectedContainer string) []string {
	if selectedContainer != "" {
		for _, c := range pod.Spec.Containers {
			if c.Name == selectedContainer {
				return []string{selectedContainer}
			}
		}
		return nil
	}
	containers := make([]string, 0, len(pod.Spec.Containers))
	for _, c := range pod.Spec.Containers {
		containers = append(containers, c.Name)
	}
	return containers
}

// normalizeWorkloadKind converts various kind formats to the plural lowercase form.
func normalizeWorkloadKind(kind string) string {
	switch strings.ToLower(kind) {
	case "deployment", "deployments":
		return "deployments"
	case "statefulset", "statefulsets":
		return "statefulsets"
	case "daemonset", "daemonsets":
		return "daemonsets"
	default:
		return ""
	}
}
