package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"

	aicontext "github.com/skyhook-io/radar/internal/ai/context"
	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/topology"
)

// logToolCall logs an MCP tool invocation with colored formatting for terminal visibility.
func logToolCall[In any](name string, handler func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, any, error)) func(context.Context, *mcp.CallToolRequest, In) (*mcp.CallToolResult, any, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input In) (*mcp.CallToolResult, any, error) {
		args, _ := json.Marshal(input)
		log.Printf("\033[1;35m[MCP]\033[0m \033[1m%s\033[0m %s", name, string(args))
		start := time.Now()
		result, extra, err := handler(ctx, req, input)
		dur := time.Since(start)
		if err != nil {
			log.Printf("\033[1;35m[MCP]\033[0m \033[1m%s\033[0m \033[31mERROR\033[0m (%s) %v", name, dur.Round(time.Millisecond), err)
		} else {
			log.Printf("\033[1;35m[MCP]\033[0m \033[1m%s\033[0m \033[32mOK\033[0m (%s)", name, dur.Round(time.Millisecond))
		}
		return result, extra, err
	}
}

func registerTools(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "get_dashboard",
		Description: "Get cluster health overview including resource counts, " +
			"problems (failing pods, unhealthy deployments), recent warning events, " +
			"and Helm release status. Start here to understand cluster state before " +
			"drilling into specific resources.",
	}, logToolCall("get_dashboard", handleGetDashboard))

	mcp.AddTool(server, &mcp.Tool{
		Name: "list_resources",
		Description: "List Kubernetes resources of a given kind with minified summaries. " +
			"Supports all built-in kinds (pods, deployments, services, etc.) and CRDs. " +
			"Use to discover what's running before inspecting individual resources.",
	}, logToolCall("list_resources", handleListResources))

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_resource",
		Description: "Get detailed information about a single Kubernetes resource. " +
			"Returns minified spec, status, and metadata. " +
			"Use after list_resources to drill into a specific resource.",
	}, logToolCall("get_resource", handleGetResource))

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_topology",
		Description: "Get the topology graph showing relationships between Kubernetes resources. " +
			"Returns nodes and edges representing Deployments, Services, Ingresses, Pods, etc. " +
			"Use 'traffic' view for network flow or 'resources' view for ownership hierarchy.",
	}, logToolCall("get_topology", handleGetTopology))

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_events",
		Description: "Get recent Kubernetes warning events, deduplicated and sorted by recency. " +
			"Useful for diagnosing issues — shows event reason, message, and occurrence count.",
	}, logToolCall("get_events", handleGetEvents))

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_pod_logs",
		Description: "Get filtered log lines from a pod, prioritizing errors and warnings. " +
			"Returns diagnostically relevant lines (errors, panics, stack traces) or " +
			"falls back to the last 20 lines if no error patterns match.",
	}, logToolCall("get_pod_logs", handleGetPodLogs))

	mcp.AddTool(server, &mcp.Tool{
		Name: "list_namespaces",
		Description: "List all Kubernetes namespaces with their status. " +
			"Use to discover available namespaces before filtering other queries.",
	}, logToolCall("list_namespaces", handleListNamespaces))
}

// Tool input types

type dashboardInput struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"filter to a specific namespace"`
}

type listResourcesInput struct {
	Kind      string `json:"kind" jsonschema:"resource kind to list, e.g. pods, deployments, services, configmaps"`
	Namespace string `json:"namespace,omitempty" jsonschema:"filter to a specific namespace"`
}

type getResourceInput struct {
	Kind      string `json:"kind" jsonschema:"resource kind, e.g. pod, deployment, service"`
	Namespace string `json:"namespace" jsonschema:"resource namespace"`
	Name      string `json:"name" jsonschema:"resource name"`
}

type topologyInput struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"filter to a specific namespace"`
	View      string `json:"view,omitempty" jsonschema:"view mode: traffic for network flow or resources for ownership hierarchy"`
}

type eventsInput struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"filter to a specific namespace"`
	Limit     int    `json:"limit,omitempty" jsonschema:"maximum number of events to return (default 20)"`
}

type podLogsInput struct {
	Namespace string `json:"namespace" jsonschema:"pod namespace"`
	Name      string `json:"name" jsonschema:"pod name"`
	Container string `json:"container,omitempty" jsonschema:"container name, defaults to first container"`
	TailLines int    `json:"tail_lines,omitempty" jsonschema:"number of lines to fetch from the end (default 200)"`
}

// Tool handlers

func handleGetDashboard(ctx context.Context, req *mcp.CallToolRequest, input dashboardInput) (*mcp.CallToolResult, any, error) {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil, nil, fmt.Errorf("not connected to cluster")
	}

	dashboard := buildDashboard(ctx, cache, input.Namespace)
	return toJSONResult(dashboard)
}

func handleListResources(ctx context.Context, req *mcp.CallToolRequest, input listResourcesInput) (*mcp.CallToolResult, any, error) {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil, nil, fmt.Errorf("not connected to cluster")
	}

	kind := strings.ToLower(input.Kind)
	var namespaces []string
	if input.Namespace != "" {
		namespaces = []string{input.Namespace}
	}

	// Try typed cache first
	objs, err := k8s.FetchResourceList(cache, kind, namespaces)
	if err == k8s.ErrUnknownKind {
		// Fall through to dynamic cache for CRDs
		return listDynamicResources(ctx, cache, kind, namespaces)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list %s: %w", kind, err)
	}

	results, err := aicontext.MinifyList(objs, aicontext.LevelSummary)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to minify: %w", err)
	}

	return toJSONResult(results)
}

func listDynamicResources(ctx context.Context, cache *k8s.ResourceCache, kind string, namespaces []string) (*mcp.CallToolResult, any, error) {
	var allItems []any
	if len(namespaces) > 0 {
		for _, ns := range namespaces {
			items, err := cache.ListDynamicWithGroup(ctx, kind, ns, "")
			if err != nil {
				return nil, nil, fmt.Errorf("failed to list %s: %w", kind, err)
			}
			for _, item := range items {
				allItems = append(allItems, aicontext.MinifyUnstructured(item, aicontext.LevelSummary))
			}
		}
	} else {
		items, err := cache.ListDynamicWithGroup(ctx, kind, "", "")
		if err != nil {
			return nil, nil, fmt.Errorf("failed to list %s: %w", kind, err)
		}
		for _, item := range items {
			allItems = append(allItems, aicontext.MinifyUnstructured(item, aicontext.LevelSummary))
		}
	}

	return toJSONResult(allItems)
}

func handleGetResource(ctx context.Context, req *mcp.CallToolRequest, input getResourceInput) (*mcp.CallToolResult, any, error) {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil, nil, fmt.Errorf("not connected to cluster")
	}

	kind := strings.ToLower(input.Kind)
	namespace := input.Namespace
	name := input.Name

	// Try typed cache first
	obj, err := k8s.FetchResource(cache, kind, namespace, name)
	if err == k8s.ErrUnknownKind {
		// Fall through to dynamic cache for CRDs
		u, dynErr := cache.GetDynamicWithGroup(ctx, kind, namespace, name, "")
		if dynErr != nil {
			return nil, nil, fmt.Errorf("resource not found: %w", dynErr)
		}
		return toJSONResult(aicontext.MinifyUnstructured(u, aicontext.LevelDetail))
	}
	if err != nil {
		return nil, nil, fmt.Errorf("resource not found: %w", err)
	}

	k8s.SetTypeMeta(obj)
	result, err := aicontext.Minify(obj, aicontext.LevelDetail)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to minify: %w", err)
	}

	return toJSONResult(result)
}

func handleGetTopology(ctx context.Context, req *mcp.CallToolRequest, input topologyInput) (*mcp.CallToolResult, any, error) {
	opts := topology.DefaultBuildOptions()
	if input.Namespace != "" {
		opts.Namespaces = []string{input.Namespace}
	}
	if input.View == "traffic" {
		opts.ViewMode = topology.ViewModeTraffic
	}

	builder := topology.NewBuilder()
	topo, err := builder.Build(opts)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to build topology: %w", err)
	}

	return toJSONResult(topo)
}

func handleGetEvents(ctx context.Context, req *mcp.CallToolRequest, input eventsInput) (*mcp.CallToolResult, any, error) {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil, nil, fmt.Errorf("not connected to cluster")
	}

	eventLister := cache.Events()
	if eventLister == nil {
		return nil, nil, fmt.Errorf("insufficient permissions to list events")
	}

	var events []*corev1.Event
	var err error
	if input.Namespace != "" {
		events, err = eventLister.Events(input.Namespace).List(labels.Everything())
	} else {
		events, err = eventLister.List(labels.Everything())
	}
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list events: %w", err)
	}

	// Convert to non-pointer slice for DeduplicateEvents
	eventValues := make([]corev1.Event, len(events))
	for i, e := range events {
		eventValues[i] = *e
	}

	deduplicated := aicontext.DeduplicateEvents(eventValues)

	limit := 20
	if input.Limit > 0 && input.Limit < limit {
		limit = input.Limit
	}
	if len(deduplicated) > limit {
		deduplicated = deduplicated[:limit]
	}

	return toJSONResult(deduplicated)
}

func handleGetPodLogs(ctx context.Context, req *mcp.CallToolRequest, input podLogsInput) (*mcp.CallToolResult, any, error) {
	clientset := k8s.GetClient()
	if clientset == nil {
		return nil, nil, fmt.Errorf("not connected to cluster")
	}

	tailLines := int64(200)
	if input.TailLines > 0 {
		tailLines = int64(input.TailLines)
	}

	opts := &corev1.PodLogOptions{
		TailLines: &tailLines,
	}
	if input.Container != "" {
		opts.Container = input.Container
	}

	stream, err := clientset.CoreV1().Pods(input.Namespace).GetLogs(input.Name, opts).Stream(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get logs for %s/%s: %w", input.Namespace, input.Name, err)
	}
	defer stream.Close()

	data, err := io.ReadAll(stream)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read logs: %w", err)
	}

	filtered := aicontext.FilterLogs(string(data))
	return toJSONResult(filtered)
}

func handleListNamespaces(ctx context.Context, req *mcp.CallToolRequest, input struct{}) (*mcp.CallToolResult, any, error) {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil, nil, fmt.Errorf("not connected to cluster")
	}

	lister := cache.Namespaces()
	if lister == nil {
		return nil, nil, fmt.Errorf("insufficient permissions to list namespaces")
	}

	namespaces, err := lister.List(labels.Everything())
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list namespaces: %w", err)
	}

	result := make([]map[string]string, 0, len(namespaces))
	for _, ns := range namespaces {
		result = append(result, map[string]string{
			"name":   ns.Name,
			"status": string(ns.Status.Phase),
		})
	}

	return toJSONResult(result)
}

// Dashboard builder for MCP (simplified version of server/dashboard.go)

type mcpDashboard struct {
	Cluster        mcpClusterInfo   `json:"cluster"`
	Health         mcpHealthSummary `json:"health"`
	Problems       []mcpProblem     `json:"problems"`
	WarningEvents  int              `json:"warningEvents"`
	TopWarnings    []mcpWarning     `json:"topWarnings"`
	HelmReleases   mcpHelmSummary   `json:"helmReleases"`
	TopologyNodes  int              `json:"topologyNodes"`
	TopologyEdges  int              `json:"topologyEdges"`
	ResourceCounts map[string]int   `json:"resourceCounts"`
}

type mcpClusterInfo struct {
	Name     string `json:"name"`
	Platform string `json:"platform"`
	Version  string `json:"version"`
}

type mcpHealthSummary struct {
	HealthyPods int `json:"healthyPods"`
	WarningPods int `json:"warningPods"`
	ErrorPods   int `json:"errorPods"`
}

type mcpProblem struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Reason    string `json:"reason"`
	Age       string `json:"age"`
}

type mcpWarning struct {
	Reason  string `json:"reason"`
	Message string `json:"message"`
	Count   int    `json:"count"`
}

type mcpHelmSummary struct {
	Total    int                `json:"total"`
	Releases []mcpHelmRelease  `json:"releases,omitempty"`
}

type mcpHelmRelease struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Chart     string `json:"chart"`
	Status    string `json:"status"`
}

func buildDashboard(ctx context.Context, cache *k8s.ResourceCache, namespace string) mcpDashboard {
	d := mcpDashboard{
		ResourceCounts: make(map[string]int),
	}

	// Cluster info
	if info, err := k8s.GetClusterInfo(ctx); err == nil {
		d.Cluster = mcpClusterInfo{
			Name:     info.Cluster,
			Platform: info.Platform,
			Version:  info.KubernetesVersion,
		}
	}

	now := time.Now()

	// Pod health
	if podLister := cache.Pods(); podLister != nil {
		var pods []*corev1.Pod
		if namespace != "" {
			pods, _ = podLister.Pods(namespace).List(labels.Everything())
		} else {
			pods, _ = podLister.List(labels.Everything())
		}
		d.ResourceCounts["pods"] = len(pods)
		for _, pod := range pods {
			switch classifyPodHealth(pod, now) {
			case "healthy":
				d.Health.HealthyPods++
			case "warning":
				d.Health.WarningPods++
			case "error":
				d.Health.ErrorPods++
				if len(d.Problems) < 10 {
					d.Problems = append(d.Problems, mcpProblem{
						Kind:      "Pod",
						Namespace: pod.Namespace,
						Name:      pod.Name,
						Reason:    podProblemReason(pod),
						Age:       formatAge(now.Sub(pod.CreationTimestamp.Time)),
					})
				}
			}
		}
	}

	// Deployment problems
	if depLister := cache.Deployments(); depLister != nil {
		var deps []*corev1.Pod // placeholder type - will use actual
		_ = deps
		listDeps := func() {
			if namespace != "" {
				items, _ := cache.Deployments().Deployments(namespace).List(labels.Everything())
				d.ResourceCounts["deployments"] = len(items)
				for _, dep := range items {
					if dep.Status.UnavailableReplicas > 0 && len(d.Problems) < 10 {
						d.Problems = append(d.Problems, mcpProblem{
							Kind:      "Deployment",
							Namespace: dep.Namespace,
							Name:      dep.Name,
							Reason:    fmt.Sprintf("%d/%d available", dep.Status.AvailableReplicas, dep.Status.Replicas),
							Age:       formatAge(now.Sub(dep.CreationTimestamp.Time)),
						})
					}
				}
			} else {
				items, _ := cache.Deployments().List(labels.Everything())
				d.ResourceCounts["deployments"] = len(items)
				for _, dep := range items {
					if dep.Status.UnavailableReplicas > 0 && len(d.Problems) < 10 {
						d.Problems = append(d.Problems, mcpProblem{
							Kind:      "Deployment",
							Namespace: dep.Namespace,
							Name:      dep.Name,
							Reason:    fmt.Sprintf("%d/%d available", dep.Status.AvailableReplicas, dep.Status.Replicas),
							Age:       formatAge(now.Sub(dep.CreationTimestamp.Time)),
						})
					}
				}
			}
		}
		listDeps()
	}

	// Simple resource counts for other types
	countResources(cache, namespace, &d)

	// Warning events
	if eventLister := cache.Events(); eventLister != nil {
		var events []*corev1.Event
		if namespace != "" {
			events, _ = eventLister.Events(namespace).List(labels.Everything())
		} else {
			events, _ = eventLister.List(labels.Everything())
		}

		var warnings []*corev1.Event
		for _, e := range events {
			if e.Type == "Warning" {
				warnings = append(warnings, e)
			}
		}
		d.WarningEvents = len(warnings)

		// Top 5 warnings sorted by recency
		sort.Slice(warnings, func(i, j int) bool {
			ti := warnings[i].LastTimestamp.Time
			tj := warnings[j].LastTimestamp.Time
			if ti.IsZero() {
				ti = warnings[i].CreationTimestamp.Time
			}
			if tj.IsZero() {
				tj = warnings[j].CreationTimestamp.Time
			}
			return ti.After(tj)
		})
		limit := 5
		if len(warnings) < limit {
			limit = len(warnings)
		}
		for _, e := range warnings[:limit] {
			count := int(e.Count)
			if count < 1 {
				count = 1
			}
			d.TopWarnings = append(d.TopWarnings, mcpWarning{
				Reason:  e.Reason,
				Message: truncate(e.Message, 200),
				Count:   count,
			})
		}
	}

	// Helm releases
	if helmClient := helm.GetClient(); helmClient != nil {
		releases, err := helmClient.ListReleases(namespace)
		if err == nil {
			d.HelmReleases.Total = len(releases)
			limit := 5
			if len(releases) < limit {
				limit = len(releases)
			}
			for _, r := range releases[:limit] {
				d.HelmReleases.Releases = append(d.HelmReleases.Releases, mcpHelmRelease{
					Name:      r.Name,
					Namespace: r.Namespace,
					Chart:     r.Chart,
					Status:    r.Status,
				})
			}
		}
	}

	// Topology summary
	opts := topology.DefaultBuildOptions()
	if namespace != "" {
		opts.Namespaces = []string{namespace}
	}
	builder := topology.NewBuilder()
	if topo, err := builder.Build(opts); err == nil {
		d.TopologyNodes = len(topo.Nodes)
		d.TopologyEdges = len(topo.Edges)
	}

	return d
}

func countResources(cache *k8s.ResourceCache, namespace string, d *mcpDashboard) {
	if svcLister := cache.Services(); svcLister != nil {
		if namespace != "" {
			items, _ := svcLister.Services(namespace).List(labels.Everything())
			d.ResourceCounts["services"] = len(items)
		} else {
			items, _ := svcLister.List(labels.Everything())
			d.ResourceCounts["services"] = len(items)
		}
	}
	if ingLister := cache.Ingresses(); ingLister != nil {
		if namespace != "" {
			items, _ := ingLister.Ingresses(namespace).List(labels.Everything())
			d.ResourceCounts["ingresses"] = len(items)
		} else {
			items, _ := ingLister.List(labels.Everything())
			d.ResourceCounts["ingresses"] = len(items)
		}
	}
	if ssLister := cache.StatefulSets(); ssLister != nil {
		if namespace != "" {
			items, _ := ssLister.StatefulSets(namespace).List(labels.Everything())
			d.ResourceCounts["statefulsets"] = len(items)
		} else {
			items, _ := ssLister.List(labels.Everything())
			d.ResourceCounts["statefulsets"] = len(items)
		}
	}
	if dsLister := cache.DaemonSets(); dsLister != nil {
		if namespace != "" {
			items, _ := dsLister.DaemonSets(namespace).List(labels.Everything())
			d.ResourceCounts["daemonsets"] = len(items)
		} else {
			items, _ := dsLister.List(labels.Everything())
			d.ResourceCounts["daemonsets"] = len(items)
		}
	}
	if jobLister := cache.Jobs(); jobLister != nil {
		if namespace != "" {
			items, _ := jobLister.Jobs(namespace).List(labels.Everything())
			d.ResourceCounts["jobs"] = len(items)
		} else {
			items, _ := jobLister.List(labels.Everything())
			d.ResourceCounts["jobs"] = len(items)
		}
	}
	if cjLister := cache.CronJobs(); cjLister != nil {
		if namespace != "" {
			items, _ := cjLister.CronJobs(namespace).List(labels.Everything())
			d.ResourceCounts["cronjobs"] = len(items)
		} else {
			items, _ := cjLister.List(labels.Everything())
			d.ResourceCounts["cronjobs"] = len(items)
		}
	}
	if nsLister := cache.Namespaces(); nsLister != nil {
		items, _ := nsLister.List(labels.Everything())
		d.ResourceCounts["namespaces"] = len(items)
	}
	if nodeLister := cache.Nodes(); nodeLister != nil {
		items, _ := nodeLister.List(labels.Everything())
		d.ResourceCounts["nodes"] = len(items)
	}
}

// classifyPodHealth determines if a pod is healthy, warning, or error.
func classifyPodHealth(pod *corev1.Pod, now time.Time) string {
	if pod.Status.Phase == corev1.PodSucceeded {
		return "healthy"
	}
	if pod.Status.Phase == corev1.PodFailed {
		return "error"
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil {
			reason := cs.State.Waiting.Reason
			if reason == "CrashLoopBackOff" || reason == "ImagePullBackOff" || reason == "ErrImagePull" || reason == "CreateContainerConfigError" {
				return "error"
			}
		}
		if cs.State.Terminated != nil && cs.State.Terminated.Reason == "OOMKilled" {
			return "error"
		}
	}
	if pod.Status.Phase == corev1.PodPending && now.Sub(pod.CreationTimestamp.Time) > 5*time.Minute {
		return "warning"
	}
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.RestartCount > 3 {
			return "warning"
		}
	}
	return "healthy"
}

func podProblemReason(pod *corev1.Pod) string {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.State.Waiting != nil && cs.State.Waiting.Reason != "" {
			return cs.State.Waiting.Reason
		}
		if cs.State.Terminated != nil && cs.State.Terminated.Reason != "" {
			return cs.State.Terminated.Reason
		}
	}
	return string(pod.Status.Phase)
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

func truncate(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// toJSONResult marshals data into a text content MCP result.
func toJSONResult(data any) (*mcp.CallToolResult, any, error) {
	b, err := json.Marshal(data)
	if err != nil {
		log.Printf("[mcp] Failed to marshal result: %v", err)
		return nil, nil, fmt.Errorf("failed to marshal result: %w", err)
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: string(b)},
		},
	}, nil, nil
}
