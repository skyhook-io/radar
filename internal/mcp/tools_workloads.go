package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/skyhook-io/radar/internal/k8s"
	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
	"github.com/skyhook-io/radar/pkg/k8score"
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
	Kind      string `json:"kind,omitempty" jsonschema:"workload kind: deployment, statefulset, daemonset, job, or workflow. Defaults to deployment when omitted."`
	Namespace string `json:"namespace" jsonschema:"workload namespace"`
	Name      string `json:"name" jsonschema:"workload name"`
	Container string `json:"container,omitempty" jsonschema:"specific container name, defaults to all containers"`
	TailLines int    `json:"tail_lines,omitempty" jsonschema:"lines per pod (default 100)"`
	Grep      string `json:"grep,omitempty" jsonschema:"optional regular expression to keep matching log lines before diagnostic filtering, like kubectl logs | grep PATTERN"`
	Since     string `json:"since,omitempty" jsonschema:"only return logs newer than this duration (e.g. 30s, 10m, 1h), like kubectl logs --since"`
	Previous  bool   `json:"previous,omitempty" jsonschema:"return logs from the previous terminated container instance (e.g. for CrashLoopBackOff diagnosis), like kubectl logs -p"`
}

// parseLogsSince converts a relative duration string like "30s"/"10m"/"1h"
// into seconds for corev1.PodLogOptions.SinceSeconds. Empty input returns
// (nil, nil) so the caller can leave SinceSeconds unset. Negative or zero
// durations are rejected — kubectl's behavior on these is implementation-
// dependent and not useful for diagnosis.
func parseLogsSince(s string) (*int64, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return nil, fmt.Errorf("invalid since duration %q: %w (expected e.g. 30s, 10m, 1h)", s, err)
	}
	if d <= 0 {
		return nil, fmt.Errorf("invalid since duration %q: must be positive", s)
	}
	secs := int64(d.Seconds())
	if secs < 1 {
		secs = 1
	}
	return &secs, nil
}

// Workload tool handlers

func handleManageWorkload(ctx context.Context, req *mcp.CallToolRequest, input manageWorkloadInput) (*mcp.CallToolResult, any, error) {
	kind := normalizeWorkloadKind(input.Kind)
	if kind == "" {
		return nil, nil, fmt.Errorf("invalid kind %q: must be deployment, statefulset, or daemonset", input.Kind)
	}

	dynClient := k8s.DynamicClientFromContext(ctx)
	if dynClient == nil {
		return nil, nil, fmt.Errorf("not connected to cluster")
	}

	switch strings.ToLower(input.Action) {
	case "restart":
		warnings := schedulingBlockerWarnings(kind, input.Namespace, input.Name)
		if err := k8s.RestartWorkloadWithClient(ctx, kind, input.Namespace, input.Name, dynClient); err != nil {
			return nil, nil, fmt.Errorf("restart failed: %w", err)
		}
		resp := map[string]any{
			"status":  "ok",
			"message": fmt.Sprintf("Rolling restart initiated for %s %s/%s", kind, input.Namespace, input.Name),
		}
		if len(warnings) > 0 {
			resp["warnings"] = warnings
		}
		return toJSONResult(resp)

	case "scale":
		if input.Replicas == nil {
			return nil, nil, fmt.Errorf("replicas is required for scale action")
		}
		if kind == "daemonsets" {
			return nil, nil, fmt.Errorf("scaling is not supported for DaemonSets (only Deployments and StatefulSets)")
		}
		if err := k8s.ScaleWorkloadWithClient(ctx, kind, input.Namespace, input.Name, *input.Replicas, dynClient); err != nil {
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
		if err := k8s.RollbackWorkloadWithClient(ctx, kind, input.Namespace, input.Name, *input.Revision, dynClient); err != nil {
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
	dynClient := k8s.DynamicClientFromContext(ctx)
	if dynClient == nil {
		return nil, nil, fmt.Errorf("not connected to cluster")
	}

	switch strings.ToLower(input.Action) {
	case "trigger":
		job, err := k8s.TriggerCronJobWithClient(ctx, input.Namespace, input.Name, dynClient)
		if err != nil {
			return nil, nil, fmt.Errorf("trigger failed: %w", err)
		}
		return toJSONResult(map[string]string{
			"status":  "ok",
			"message": fmt.Sprintf("Triggered manual job from CronJob %s/%s", input.Namespace, input.Name),
			"jobName": job.GetName(),
		})

	case "suspend":
		if err := k8s.SetCronJobSuspendWithClient(ctx, input.Namespace, input.Name, true, dynClient); err != nil {
			return nil, nil, fmt.Errorf("suspend failed: %w", err)
		}
		return toJSONResult(map[string]string{
			"status":  "ok",
			"message": fmt.Sprintf("Suspended CronJob %s/%s", input.Namespace, input.Name),
		})

	case "resume":
		if err := k8s.SetCronJobSuspendWithClient(ctx, input.Namespace, input.Name, false, dynClient); err != nil {
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
	kind := normalizeWorkloadLogsKind(input.Kind)
	if kind == "" {
		return nil, nil, fmt.Errorf("invalid kind %q: must be deployment, statefulset, daemonset, job, or workflow", input.Kind)
	}

	if !checkNamespaceAccess(ctx, input.Namespace) {
		return nil, nil, fmt.Errorf("forbidden: no access to namespace %q", input.Namespace)
	}

	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil, nil, fmt.Errorf("not connected to cluster")
	}

	client := k8s.ClientFromContext(ctx)
	if client == nil {
		return nil, nil, fmt.Errorf("not connected to cluster")
	}

	// Get the workload's label selector
	selector, err := k8s.GetWorkloadSelector(cache, kind, input.Namespace, input.Name)
	if err != nil {
		return nil, nil, workloadSelectorMCPError(ctx, err, kind, input.Namespace, input.Name)
	}

	// Get pods matching the workload
	pods := cache.GetPodsForWorkload(input.Namespace, selector)
	if len(pods) == 0 {
		empty := describeMCPWorkloadLogEmpty(ctx, kind, input.Namespace, input.Name)
		response := map[string]any{
			"workload": fmt.Sprintf("%s/%s/%s", kind, input.Namespace, input.Name),
			"pods":     0,
			"logs":     empty.Message,
		}
		addMCPWorkloadLogEmptyMetadata(response, empty)
		return toJSONResult(response)
	}

	tailLines := int64(100)
	if input.TailLines > 0 {
		tailLines = int64(input.TailLines)
	}
	if strings.TrimSpace(input.Grep) != "" {
		if _, err := regexp.Compile(input.Grep); err != nil {
			return nil, nil, fmt.Errorf("invalid grep regex: %w", err)
		}
	}
	sinceSeconds, err := parseLogsSince(input.Since)
	if err != nil {
		return nil, nil, err
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

	// Mirror diagnose's logsError contract: surface a missing kube client
	// distinctly from an empty pod set, so agents don't read "no log lines"
	// as truth when we couldn't even try to fetch.
	if k8s.ClientFromContext(ctx) == nil {
		return toJSONResult(map[string]any{
			"workload":  fmt.Sprintf("%s/%s/%s", kind, input.Namespace, input.Name),
			"pods":      len(pods),
			"logsError": "no kube client in request context",
		})
	}

	allLogs := fetchPodLogs(ctx, pods, input.Namespace, input.Container, input.Grep, tailLines, sinceSeconds, input.Previous)

	resp := map[string]any{
		"workload": fmt.Sprintf("%s/%s/%s", kind, input.Namespace, input.Name),
		"pods":     len(pods),
		"logs":     allLogs,
	}
	// Steering hint when any pod's stream hit its tail cap. Compare against
	// RawLines (pre-grep) so grep-filtered streams still surface the hint.
	// Heuristic mirrors handleGetPodLogs.
	for _, e := range allLogs {
		if int64(e.RawLines) >= tailLines {
			resp["narrowHint"] = fmt.Sprintf(
				"at least one pod's log stream tailed to %d lines (cap reached) — narrow with since= (e.g. 10m), grep= regex, container=, or raise tail_lines",
				tailLines,
			)
			break
		}
	}
	if w := computeWorkloadLogsWarnings(pods, input.Previous); len(w) > 0 {
		resp["warnings"] = w
	}
	return toJSONResult(resp)
}

func workloadSelectorMCPError(ctx context.Context, err error, kind, namespace, name string) error {
	if errors.Is(err, k8s.ErrWorkloadAccessDenied) || apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
		return fmt.Errorf("forbidden: cannot access %s %s/%s: %w", kind, namespace, name, err)
	}
	if apierrors.IsNotFound(err) || errors.Is(err, k8score.ErrResourceNotFound) {
		return notFoundError(ctx, err, kind, namespace, name)
	}
	return fmt.Errorf("get %s %s/%s: %w", kind, namespace, name, err)
}

type mcpWorkloadLogEmptyMetadata struct {
	Reason  string
	Message string
	Command string
}

func describeMCPWorkloadLogEmpty(ctx context.Context, kind, namespace, name string) mcpWorkloadLogEmptyMetadata {
	switch kind {
	case "jobs":
		return describeMCPJobLogEmpty(namespace, name)
	case "workflows":
		return describeMCPWorkflowLogEmpty(ctx, namespace, name)
	default:
		return mcpWorkloadLogEmptyMetadata{
			Reason:  "no-pods",
			Message: "no pods found for this workload",
		}
	}
}

func describeMCPJobLogEmpty(namespace, name string) mcpWorkloadLogEmptyMetadata {
	metadata := mcpWorkloadLogEmptyMetadata{
		Reason:  "no-pods",
		Message: "No pods found for this Job yet. Check scheduling, admission, or controller events.",
		Command: "kubectl logs job/" + name + " -n " + namespace,
	}
	cache := k8s.GetResourceCache()
	if cache == nil || cache.Jobs() == nil {
		return metadata
	}
	job, err := cache.Jobs().Jobs(namespace).Get(name)
	if err != nil {
		return metadata
	}
	applyMCPTerminalJobEmptyState(&metadata, job, namespace, name)
	return metadata
}

func applyMCPTerminalJobEmptyState(metadata *mcpWorkloadLogEmptyMetadata, job *batchv1.Job, namespace, name string) {
	if !k8s.IsJobTerminal(job) {
		return
	}
	metadata.Reason = "pods-gone"
	metadata.Message = "This Job has finished, but its pods are no longer present in Kubernetes. If logs were retained externally, use your logging system; otherwise inspect the Job conditions and events."
	metadata.Command = "kubectl describe job/" + name + " -n " + namespace
}

func describeMCPWorkflowLogEmpty(ctx context.Context, namespace, name string) mcpWorkloadLogEmptyMetadata {
	metadata := mcpWorkloadLogEmptyMetadata{
		Reason:  "no-pods",
		Message: "No Workflow pods found yet. Check scheduling, admission, or controller events.",
		Command: "argo logs " + name + " -n " + namespace,
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		return metadata
	}
	workflow, err := cache.GetDynamicWithGroup(ctx, "Workflow", namespace, name, "argoproj.io")
	if err != nil {
		return metadata
	}
	applyMCPTerminalWorkflowEmptyState(&metadata, workflow.Object, namespace, name)
	return metadata
}

func applyMCPTerminalWorkflowEmptyState(metadata *mcpWorkloadLogEmptyMetadata, workflow map[string]any, namespace, name string) {
	if !k8s.IsWorkflowTerminal(workflow) {
		return
	}
	metadata.Reason = "pods-gone"
	if k8s.WorkflowArchiveLogsConfigured(workflow) {
		metadata.Message = "This Workflow has finished and its pods are no longer present. Archived logs appear to be enabled; use the configured Argo or logging UI, or try argo logs " + name + " -n " + namespace + "."
	} else {
		metadata.Message = "This Workflow has finished and its pods are no longer present. Argo may have garbage-collected them; Kubernetes pod logs are no longer available here."
	}
}

func addMCPWorkloadLogEmptyMetadata(response map[string]any, metadata mcpWorkloadLogEmptyMetadata) {
	if metadata.Reason != "" {
		response["emptyReason"] = metadata.Reason
	}
	if metadata.Message != "" {
		response["emptyMessage"] = metadata.Message
	}
	if metadata.Command != "" {
		response["command"] = metadata.Command
	}
}

// schedulingBlockerWarnings detects when a restart won't accomplish what the
// agent likely wants: if the workload currently has Pending pods blocked on
// scheduling or post-bind (CNI/volume) issues, a rolling restart just creates
// more pods that hit the same wall. The agent should fix the underlying
// constraint instead (taints/affinity/capacity/CNI/storage). Best-effort —
// never blocks the restart.
//
// Admission failures (quota/PSA/webhook) are intentionally out of scope: they
// block pod creation entirely, so there are no Pending pods to key on, and the
// FailedCreate event names the controller rather than a Pod.
func schedulingBlockerWarnings(kind, namespace, name string) []string {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil
	}
	selector, err := k8s.GetWorkloadSelector(cache, kind, namespace, name)
	if err != nil {
		return nil
	}
	pods := cache.GetPodsForWorkload(namespace, selector)
	if len(pods) == 0 {
		return nil
	}

	var pendingCount int
	podNames := make(map[string]bool, len(pods))
	for _, p := range pods {
		if p.Status.Phase == corev1.PodPending {
			pendingCount++
		}
		podNames[p.Name] = true
	}
	if pendingCount == 0 {
		return nil
	}

	all := k8s.DetectSchedulingProblems(cache, namespace)
	all = append(all, k8s.DetectPostBindProblems(cache, namespace)...)

	reasons := map[string]struct{}{}
	for _, p := range all {
		if p.Kind != "Pod" || !podNames[p.Name] {
			continue
		}
		if p.Reason != "" {
			reasons[p.Reason] = struct{}{}
		}
	}
	if len(reasons) == 0 {
		// Pending pods exist but with no detected scheduling/admission cause —
		// could be initial pull or short transient. Skip the warning rather
		// than surface a generic "pending" note that the agent will ignore.
		return nil
	}

	rs := make([]string, 0, len(reasons))
	for r := range reasons {
		rs = append(rs, r)
	}
	sort.Strings(rs)
	return []string{fmt.Sprintf(
		"%d of %d pod(s) are currently `Pending` with cause(s): %s. A rolling restart replaces existing pods with new ones that face the same constraint — fix the underlying issue (taints/affinity/resources/quota/PSA) before restarting.",
		pendingCount, len(pods), strings.Join(rs, ", "),
	)}
}

// computeWorkloadLogsWarnings aggregates the not-Running and crashloop logs
// hints that get_pod_logs surfaces, summarized across all pods of the workload.
func computeWorkloadLogsWarnings(pods []*corev1.Pod, previous bool) []string {
	var notRunning, crashloop int
	for _, p := range pods {
		if p.Status.Phase != corev1.PodRunning && p.Status.Phase != corev1.PodSucceeded {
			notRunning++
		}
		if !previous && pickCrashIndicator(p.Status.ContainerStatuses) != nil {
			crashloop++
		}
	}
	var out []string
	if notRunning > 0 {
		out = append(out, fmt.Sprintf(
			"%d of %d pod(s) are not in `Running` phase; their containers haven't produced application logs yet. Inspect scheduling/pull state via `diagnose` or `get_resource` with include=events.",
			notRunning, len(pods),
		))
	}
	if crashloop > 0 {
		out = append(out, fmt.Sprintf(
			"%d of %d pod(s) have container restarts on record; the error(s) that killed prior containers are in the previous instance's logs — call again with `previous: true` to see them.",
			crashloop, len(pods),
		))
	}
	return out
}

// podLogEntry is the per-pod-per-container log row returned by fetchPodLogs.
//
// RawLines is the line count of the pre-grep stream so the workload-logs
// narrowHint can detect upstream truncation correctly even when grep is
// active. FilteredLogs.TotalLines reflects the post-grep count.
type podLogEntry struct {
	Pod       string                 `json:"pod"`
	Container string                 `json:"container"`
	RawLines  int                    `json:"-"`
	Logs      aicontext.FilteredLogs `json:"logs,omitempty"`
	Error     string                 `json:"error,omitempty"`
}

// fetchPodLogs fans out kubectl-logs requests across the given pods x containers.
// containerFilter "" includes every container; non-empty restricts to that name.
// grep is server-side regex applied before diagnostic filtering. previous=true
// fetches the prior terminated container instance (CrashLoopBackOff diagnosis).
// Returns entries sorted by (pod, container) for deterministic output.
// Resolves the kube client from ctx so the call still honors per-request RBAC.
func fetchPodLogs(ctx context.Context, pods []*corev1.Pod, namespace, containerFilter, grep string, tailLines int64, sinceSeconds *int64, previous bool) []podLogEntry {
	client := k8s.ClientFromContext(ctx)
	if client == nil {
		return nil
	}

	var allLogs []podLogEntry
	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, pod := range pods {
		containers := k8s.GetContainersForPod(pod, containerFilter, true)
		for _, c := range containers {
			wg.Add(1)
			go func(podName, containerName string) {
				defer wg.Done()

				opts := &corev1.PodLogOptions{
					Container:    containerName,
					TailLines:    &tailLines,
					SinceSeconds: sinceSeconds,
					Previous:     previous,
					Timestamps:   true,
				}

				entry := podLogEntry{
					Pod:       podName,
					Container: containerName,
				}

				stream, err := client.CoreV1().Pods(namespace).GetLogs(podName, opts).Stream(ctx)
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

				// Capture pre-grep line count so callers can detect upstream
				// truncation even when grep filters heavily — see RawLines.
				entry.RawLines = countLines(string(data))
				// handleGetWorkloadLogs pre-validates the regex, but this
				// helper is exported within the package — propagate any
				// filter error per-entry so a future caller that skips
				// pre-validation doesn't silently lose log lines.
				filtered, filterErr := aicontext.FilterLogsByPattern(string(data), grep)
				if filterErr != nil {
					entry.Error = fmt.Sprintf("filter error: %v", filterErr)
				} else {
					entry.Logs = filtered
				}

				mu.Lock()
				allLogs = append(allLogs, entry)
				mu.Unlock()
			}(pod.Name, c)
		}
	}

	wg.Wait()

	sort.Slice(allLogs, func(i, j int) bool {
		if allLogs[i].Pod != allLogs[j].Pod {
			return allLogs[i].Pod < allLogs[j].Pod
		}
		return allLogs[i].Container < allLogs[j].Container
	})
	return allLogs
}

// Node tool input and handler

type manageNodeInput struct {
	Action             string `json:"action" jsonschema:"action to perform: cordon, uncordon, or drain"`
	Name               string `json:"name" jsonschema:"node name"`
	DeleteEmptyDirData *bool  `json:"delete_empty_dir_data,omitempty" jsonschema:"evict pods with emptyDir volumes (default true, set false to skip them)"`
	Force              bool   `json:"force,omitempty" jsonschema:"force evict pods not managed by a controller (default false)"`
	Timeout            int    `json:"timeout,omitempty" jsonschema:"drain timeout in seconds (default 60)"`
}

func handleManageNode(ctx context.Context, req *mcp.CallToolRequest, input manageNodeInput) (*mcp.CallToolResult, any, error) {
	if input.Name == "" {
		return nil, nil, fmt.Errorf("node name is required")
	}

	client := k8s.ClientFromContext(ctx)
	if client == nil {
		return nil, nil, fmt.Errorf("not connected to cluster")
	}

	switch strings.ToLower(input.Action) {
	case "cordon":
		if err := k8s.CordonNodeWithClient(ctx, input.Name, client); err != nil {
			return nil, nil, fmt.Errorf("cordon failed: %w", err)
		}
		return toJSONResult(map[string]string{
			"status":  "ok",
			"message": fmt.Sprintf("Node %s cordoned (marked unschedulable)", input.Name),
		})

	case "uncordon":
		if err := k8s.UncordonNodeWithClient(ctx, input.Name, client); err != nil {
			return nil, nil, fmt.Errorf("uncordon failed: %w", err)
		}
		return toJSONResult(map[string]string{
			"status":  "ok",
			"message": fmt.Sprintf("Node %s uncordoned (marked schedulable)", input.Name),
		})

	case "drain":
		// Default deleteEmptyDirData to true (most pods use emptyDir for tmp/caches)
		deleteLocal := true
		if input.DeleteEmptyDirData != nil {
			deleteLocal = *input.DeleteEmptyDirData
		}
		opts := k8s.DrainOptions{
			IgnoreDaemonSets:   true,
			DeleteEmptyDirData: deleteLocal,
			Force:              input.Force,
			Timeout:            60 * time.Second,
		}
		if input.Timeout > 0 {
			opts.Timeout = time.Duration(input.Timeout) * time.Second
		}
		result, err := k8s.DrainNodeWithClient(ctx, input.Name, opts, client)
		if err != nil {
			return nil, nil, fmt.Errorf("drain failed: %w", err)
		}
		status := "ok"
		msg := fmt.Sprintf("Drained node %s: %d pods evicted", input.Name, len(result.EvictedPods))
		if len(result.Errors) > 0 {
			status = "partial"
			msg = fmt.Sprintf("Drain partially completed on node %s: %d evicted, %d failed",
				input.Name, len(result.EvictedPods), len(result.Errors))
		}
		return toJSONResult(map[string]any{
			"status":      status,
			"message":     msg,
			"evictedPods": result.EvictedPods,
			"errors":      result.Errors,
		})

	default:
		return nil, nil, fmt.Errorf("unknown action %q: must be cordon, uncordon, or drain", input.Action)
	}
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

func normalizeWorkloadLogsKind(kind string) string {
	if strings.TrimSpace(kind) == "" {
		return "deployments"
	}
	switch strings.ToLower(kind) {
	case "job", "jobs":
		return "jobs"
	case "workflow", "workflows":
		return "workflows"
	default:
		return normalizeWorkloadKind(kind)
	}
}
