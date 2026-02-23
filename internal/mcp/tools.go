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
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
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
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_dashboard",
		Description: "Get cluster health overview including resource counts, " +
			"problems (failing pods, unhealthy deployments), recent warning events, " +
			"and Helm release status. Start here to understand cluster state before " +
			"drilling into specific resources.",
		Annotations: readOnly,
	}, logToolCall("get_dashboard", handleGetDashboard))

	mcp.AddTool(server, &mcp.Tool{
		Name: "list_resources",
		Description: "List Kubernetes resources of a given kind with minified summaries. " +
			"Supports all built-in kinds (pods, deployments, services, etc.) and CRDs. " +
			"Use to discover what's running before inspecting individual resources.",
		Annotations: readOnly,
	}, logToolCall("list_resources", handleListResources))

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_resource",
		Description: "Get detailed information about a single Kubernetes resource. " +
			"Returns minified spec, status, and metadata. " +
			"Use after list_resources to drill into a specific resource.",
		Annotations: readOnly,
	}, logToolCall("get_resource", handleGetResource))

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_topology",
		Description: "Get the topology graph showing relationships between Kubernetes resources. " +
			"Returns nodes and edges representing Deployments, Services, Ingresses, Pods, etc. " +
			"Use 'traffic' view for network flow or 'resources' view for ownership hierarchy.",
		Annotations: readOnly,
	}, logToolCall("get_topology", handleGetTopology))

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_events",
		Description: "Get recent Kubernetes warning events, deduplicated and sorted by recency. " +
			"Useful for diagnosing issues — shows event reason, message, and occurrence count.",
		Annotations: readOnly,
	}, logToolCall("get_events", handleGetEvents))

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_pod_logs",
		Description: "Get filtered log lines from a pod, prioritizing errors and warnings. " +
			"Returns diagnostically relevant lines (errors, panics, stack traces) or " +
			"falls back to the last 20 lines if no error patterns match.",
		Annotations: readOnly,
	}, logToolCall("get_pod_logs", handleGetPodLogs))

	mcp.AddTool(server, &mcp.Tool{
		Name: "list_namespaces",
		Description: "List all Kubernetes namespaces with their status. " +
			"Use to discover available namespaces before filtering other queries.",
		Annotations: readOnly,
	}, logToolCall("list_namespaces", handleListNamespaces))

	// --- Helm tools (read-only) ---

	mcp.AddTool(server, &mcp.Tool{
		Name: "list_helm_releases",
		Description: "List all Helm releases in the cluster with their status and health. " +
			"Returns release name, namespace, chart, version, status (deployed/failed/pending), " +
			"and resource health (healthy/degraded/unhealthy). " +
			"Use to get an overview of what's deployed via Helm before inspecting individual releases.",
		Annotations: readOnly,
	}, logToolCall("list_helm_releases", handleListHelmReleases))

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_helm_release",
		Description: "Get detailed information about a specific Helm release including owned resources " +
			"and their status. Optionally include values, revision history, or manifest diff between revisions " +
			"using the 'include' parameter (comma-separated: values, history, diff). " +
			"For diff, also provide diff_revision_1 and optionally diff_revision_2.",
		Annotations: readOnly,
	}, logToolCall("get_helm_release", handleGetHelmRelease))

	// --- Workload logs tool (read-only) ---

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_workload_logs",
		Description: "Get aggregated, AI-filtered logs from all pods of a workload (Deployment, StatefulSet, " +
			"or DaemonSet). Logs are collected from all matching pods concurrently, filtered for errors/warnings, " +
			"and deduplicated. More useful than get_pod_logs when you need logs across all replicas of a workload.",
		Annotations: readOnly,
	}, logToolCall("get_workload_logs", handleGetWorkloadLogs))

	// --- Write tools (workload, cronjob, gitops) ---

	boolPtr := func(b bool) *bool { return &b }

	mcp.AddTool(server, &mcp.Tool{
		Name: "manage_workload",
		Description: "Perform operations on a Kubernetes workload (Deployment, StatefulSet, or DaemonSet). " +
			"Supported actions: 'restart' triggers a rolling restart, 'scale' changes the replica count " +
			"(requires 'replicas' parameter), 'rollback' reverts to a previous revision " +
			"(requires 'revision' parameter). Use list_resources or get_dashboard first to identify the target.",
		Annotations: &mcp.ToolAnnotations{
			DestructiveHint: boolPtr(false),
		},
	}, logToolCall("manage_workload", handleManageWorkload))

	mcp.AddTool(server, &mcp.Tool{
		Name: "manage_cronjob",
		Description: "Perform operations on a Kubernetes CronJob. Supported actions: " +
			"'trigger' creates a manual Job run from the CronJob's template, " +
			"'suspend' pauses the CronJob schedule (no new Jobs will be created), " +
			"'resume' re-enables a suspended CronJob's schedule.",
		Annotations: &mcp.ToolAnnotations{
			DestructiveHint: boolPtr(false),
		},
	}, logToolCall("manage_cronjob", handleManageCronJob))

	mcp.AddTool(server, &mcp.Tool{
		Name: "manage_gitops",
		Description: "Perform operations on GitOps resources (ArgoCD or FluxCD). " +
			"For ArgoCD: actions are 'sync' (trigger deployment), 'suspend' (disable auto-sync), " +
			"'resume' (re-enable auto-sync). Resource kind is always Application. " +
			"For FluxCD: actions are 'reconcile' (trigger sync), 'suspend', 'resume'. " +
			"Requires 'kind' parameter (kustomization, helmrelease, gitrepository, etc.).",
		Annotations: &mcp.ToolAnnotations{
			DestructiveHint: boolPtr(false),
		},
	}, logToolCall("manage_gitops", handleManageGitOps))
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
	Limit     int    `json:"limit,omitempty" jsonschema:"max 100, default 20"`
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
	if input.Limit > 0 {
		limit = min(input.Limit, 100)
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

	result := make([]map[string]any, 0, len(namespaces))
	for _, ns := range namespaces {
		entry := map[string]any{
			"name":   ns.Name,
			"status": string(ns.Status.Phase),
		}
		if len(ns.Labels) > 0 {
			entry["labels"] = ns.Labels
		}
		result = append(result, entry)
	}

	return toJSONResult(result)
}

// Dashboard builder for MCP (simplified version of server/dashboard.go)

type mcpDashboard struct {
	Cluster        mcpClusterInfo   `json:"cluster"`
	Nodes          mcpNodeSummary   `json:"nodes"`
	VersionSkew    []string         `json:"versionSkew,omitempty"`
	Health         mcpHealthSummary `json:"health"`
	Problems       []mcpProblem     `json:"problems"`
	WarningEvents  int              `json:"warningEvents"`
	TopWarnings    []mcpWarning     `json:"topWarnings"`
	HelmReleases   mcpHelmSummary   `json:"helmReleases"`
	Metrics        *mcpMetrics      `json:"metrics,omitempty"`
	TopologyNodes  int              `json:"topologyNodes"`
	TopologyEdges  int              `json:"topologyEdges"`
	ResourceCounts map[string]int   `json:"resourceCounts"`
}

type mcpMetrics struct {
	CPUUsagePercent   int `json:"cpuUsagePercent,omitempty"`
	CPURequestPercent int `json:"cpuRequestPercent,omitempty"`
	MemUsagePercent   int `json:"memUsagePercent,omitempty"`
	MemRequestPercent int `json:"memRequestPercent,omitempty"`
}

type mcpClusterInfo struct {
	Name     string `json:"name"`
	Platform string `json:"platform"`
	Version  string `json:"version"`
}

type mcpNodeSummary struct {
	Total    int `json:"total"`
	Ready    int `json:"ready"`
	NotReady int `json:"notReady"`
	Cordoned int `json:"cordoned"`
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
	Message   string `json:"message,omitempty"`
	Age       string `json:"age"`
}

type mcpWarning struct {
	Reason  string `json:"reason"`
	Message string `json:"message"`
	Count   int    `json:"count"`
}

type mcpHelmSummary struct {
	Total    int              `json:"total"`
	Releases []mcpHelmRelease `json:"releases,omitempty"`
}

type mcpHelmRelease struct {
	Name           string `json:"name"`
	Namespace      string `json:"namespace"`
	Chart          string `json:"chart"`
	ChartVersion   string `json:"chartVersion"`
	Status         string `json:"status"`
	ResourceHealth string `json:"resourceHealth,omitempty"`
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
			switch k8s.ClassifyPodHealth(pod, now) {
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
						Reason:    k8s.PodProblemReason(pod),
						Age:       k8s.FormatAge(now.Sub(pod.CreationTimestamp.Time)),
					})
				}
			}
		}
	}

	// Deployment problems
	if depLister := cache.Deployments(); depLister != nil {
		if namespace != "" {
			items, _ := depLister.Deployments(namespace).List(labels.Everything())
			d.ResourceCounts["deployments"] = len(items)
			for _, dep := range items {
				if dep.Status.UnavailableReplicas > 0 && len(d.Problems) < 10 {
					d.Problems = append(d.Problems, mcpProblem{
						Kind:      "Deployment",
						Namespace: dep.Namespace,
						Name:      dep.Name,
						Reason:    fmt.Sprintf("%d/%d available", dep.Status.AvailableReplicas, dep.Status.Replicas),
						Age:       k8s.FormatAge(now.Sub(dep.CreationTimestamp.Time)),
					})
				}
			}
		} else {
			items, _ := depLister.List(labels.Everything())
			d.ResourceCounts["deployments"] = len(items)
			for _, dep := range items {
				if dep.Status.UnavailableReplicas > 0 && len(d.Problems) < 10 {
					d.Problems = append(d.Problems, mcpProblem{
						Kind:      "Deployment",
						Namespace: dep.Namespace,
						Name:      dep.Name,
						Reason:    fmt.Sprintf("%d/%d available", dep.Status.AvailableReplicas, dep.Status.Replicas),
						Age:       k8s.FormatAge(now.Sub(dep.CreationTimestamp.Time)),
					})
				}
			}
		}
	}

	// StatefulSet problems: readyReplicas < replicas
	if ssLister := cache.StatefulSets(); ssLister != nil {
		if namespace != "" {
			ssets, _ := ssLister.StatefulSets(namespace).List(labels.Everything())
			for _, ss := range ssets {
				if ss.Status.ReadyReplicas < ss.Status.Replicas && len(d.Problems) < 10 {
					d.Problems = append(d.Problems, mcpProblem{
						Kind:      "StatefulSet",
						Namespace: ss.Namespace,
						Name:      ss.Name,
						Reason:    fmt.Sprintf("%d/%d ready", ss.Status.ReadyReplicas, ss.Status.Replicas),
						Age:       k8s.FormatAge(now.Sub(ss.CreationTimestamp.Time)),
					})
				}
			}
		} else {
			ssets, _ := ssLister.List(labels.Everything())
			for _, ss := range ssets {
				if ss.Status.ReadyReplicas < ss.Status.Replicas && len(d.Problems) < 10 {
					d.Problems = append(d.Problems, mcpProblem{
						Kind:      "StatefulSet",
						Namespace: ss.Namespace,
						Name:      ss.Name,
						Reason:    fmt.Sprintf("%d/%d ready", ss.Status.ReadyReplicas, ss.Status.Replicas),
						Age:       k8s.FormatAge(now.Sub(ss.CreationTimestamp.Time)),
					})
				}
			}
		}
	}

	// DaemonSet problems: numberUnavailable > 0
	if dsLister := cache.DaemonSets(); dsLister != nil {
		if namespace != "" {
			dsets, _ := dsLister.DaemonSets(namespace).List(labels.Everything())
			for _, ds := range dsets {
				if ds.Status.NumberUnavailable > 0 && len(d.Problems) < 10 {
					d.Problems = append(d.Problems, mcpProblem{
						Kind:      "DaemonSet",
						Namespace: ds.Namespace,
						Name:      ds.Name,
						Reason:    fmt.Sprintf("%d unavailable", ds.Status.NumberUnavailable),
						Age:       k8s.FormatAge(now.Sub(ds.CreationTimestamp.Time)),
					})
				}
			}
		} else {
			dsets, _ := dsLister.List(labels.Everything())
			for _, ds := range dsets {
				if ds.Status.NumberUnavailable > 0 && len(d.Problems) < 10 {
					d.Problems = append(d.Problems, mcpProblem{
						Kind:      "DaemonSet",
						Namespace: ds.Namespace,
						Name:      ds.Name,
						Reason:    fmt.Sprintf("%d unavailable", ds.Status.NumberUnavailable),
						Age:       k8s.FormatAge(now.Sub(ds.CreationTimestamp.Time)),
					})
				}
			}
		}
	}

	// HPA problems
	if hpaLister := cache.HorizontalPodAutoscalers(); hpaLister != nil {
		var hpas []*autoscalingv2.HorizontalPodAutoscaler
		if namespace != "" {
			hpas, _ = hpaLister.HorizontalPodAutoscalers(namespace).List(labels.Everything())
		} else {
			hpas, _ = hpaLister.List(labels.Everything())
		}
		for _, hp := range k8s.DetectHPAProblems(hpas) {
			if len(d.Problems) < 10 {
				d.Problems = append(d.Problems, mcpProblem{
					Kind:      "HorizontalPodAutoscaler",
					Namespace: hp.Namespace,
					Name:      hp.Name,
					Reason:    hp.Reason,
				})
			}
		}
	}

	// CronJob problems
	if cjLister := cache.CronJobs(); cjLister != nil {
		var cronjobs []*batchv1.CronJob
		if namespace != "" {
			cronjobs, _ = cjLister.CronJobs(namespace).List(labels.Everything())
		} else {
			cronjobs, _ = cjLister.List(labels.Everything())
		}
		for _, cp := range k8s.DetectCronJobProblems(cronjobs) {
			if len(d.Problems) < 10 {
				d.Problems = append(d.Problems, mcpProblem{
					Kind:      "CronJob",
					Namespace: cp.Namespace,
					Name:      cp.Name,
					Reason:    cp.Reason,
				})
			}
		}
	}

	// Node problems (cluster-scoped, not filtered by namespace)
	if nodeLister := cache.Nodes(); nodeLister != nil {
		nodes, _ := nodeLister.List(labels.Everything())
		d.ResourceCounts["nodes"] = len(nodes)
		d.Nodes.Total = len(nodes)

		for _, node := range nodes {
			h := k8s.ClassifyNodeHealth(node)
			if h.Ready {
				if h.Unschedulable {
					d.Nodes.Cordoned++
				} else {
					d.Nodes.Ready++
				}
			} else {
				d.Nodes.NotReady++
			}
		}

		nodeProblems := k8s.DetectNodeProblems(nodes)
		for _, np := range nodeProblems {
			if len(d.Problems) < 10 {
				age := ""
				for _, n := range nodes {
					if n.Name == np.NodeName {
						age = k8s.FormatAge(now.Sub(n.CreationTimestamp.Time))
						break
					}
				}
				d.Problems = append(d.Problems, mcpProblem{
					Kind:   "Node",
					Name:   np.NodeName,
					Reason: np.Problem,
					Age:    age,
				})
			}
		}

		// Version skew
		if skew := k8s.DetectVersionSkew(nodes); skew != nil {
			for v := range skew.Versions {
				d.VersionSkew = append(d.VersionSkew, v)
			}
			sort.Strings(d.VersionSkew)
		}
	}

	// Simple resource counts for other types
	countResources(cache, namespace, &d)

	// Warning events — deduplicate first, then sort by count
	if eventLister := cache.Events(); eventLister != nil {
		var events []*corev1.Event
		if namespace != "" {
			events, _ = eventLister.Events(namespace).List(labels.Everything())
		} else {
			events, _ = eventLister.List(labels.Everything())
		}

		var warningValues []corev1.Event
		for _, e := range events {
			if e.Type == "Warning" {
				warningValues = append(warningValues, *e)
			}
		}
		d.WarningEvents = len(warningValues)

		// Deduplicate and sort by count descending to surface systemic issues
		deduplicated := aicontext.DeduplicateEvents(warningValues)
		sort.Slice(deduplicated, func(i, j int) bool {
			return deduplicated[i].Count > deduplicated[j].Count
		})

		limit := min(len(deduplicated), 5)
		for _, e := range deduplicated[:limit] {
			d.TopWarnings = append(d.TopWarnings, mcpWarning{
				Reason:  e.Reason,
				Message: k8s.Truncate(e.Message, 200),
				Count:   e.Count,
			})
		}
	}

	// Helm releases — sort failed-first before slicing
	if helmClient := helm.GetClient(); helmClient != nil {
		releases, err := helmClient.ListReleases(namespace)
		if err == nil {
			d.HelmReleases.Total = len(releases)

			// Sort: failed/pending-install first, then unhealthy/degraded
			sort.SliceStable(releases, func(i, j int) bool {
				return helmStatusPriority(releases[i].Status, releases[i].ResourceHealth) < helmStatusPriority(releases[j].Status, releases[j].ResourceHealth)
			})

			limit := min(len(releases), 5)
			for _, r := range releases[:limit] {
				d.HelmReleases.Releases = append(d.HelmReleases.Releases, mcpHelmRelease{
					Name:           r.Name,
					Namespace:      r.Namespace,
					Chart:          r.Chart,
					ChartVersion:   r.ChartVersion,
					Status:         r.Status,
					ResourceHealth: r.ResourceHealth,
				})
			}
		}
	}

	// Metrics (best-effort — silently skip if metrics-server unavailable)
	if client := k8s.GetClient(); client != nil {
		data, err := client.RESTClient().Get().
			AbsPath("/apis/metrics.k8s.io/v1beta1/nodes").
			DoRaw(ctx)
		if err == nil {
			var nodeMetricsList struct {
				Items []struct {
					Usage struct {
						CPU    string `json:"cpu"`
						Memory string `json:"memory"`
					} `json:"usage"`
				} `json:"items"`
			}
			if err := json.Unmarshal(data, &nodeMetricsList); err != nil {
				log.Printf("[mcp] Failed to parse node metrics: %v", err)
			} else if len(nodeMetricsList.Items) > 0 {
				if nodeLister := cache.Nodes(); nodeLister != nil {
					allNodes, _ := nodeLister.List(labels.Everything())
					var cpuCapMillis, memCapBytes int64
					for _, n := range allNodes {
						cpuCapMillis += n.Status.Capacity.Cpu().MilliValue()
						memCapBytes += n.Status.Capacity.Memory().Value()
					}

					var cpuUsageMillis, memUsageBytes int64
					for _, item := range nodeMetricsList.Items {
						cpuUsageMillis += k8s.ParseCPUToMillis(item.Usage.CPU)
						memUsageBytes += k8s.ParseMemoryToBytes(item.Usage.Memory)
					}

					var cpuReqMillis, memReqBytes int64
					if podLister := cache.Pods(); podLister != nil {
						allPods, _ := podLister.List(labels.Everything())
						for _, pod := range allPods {
							if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
								continue
							}
							for _, c := range pod.Spec.Containers {
								if c.Resources.Requests != nil {
									if cpu, ok := c.Resources.Requests[corev1.ResourceCPU]; ok {
										cpuReqMillis += cpu.MilliValue()
									}
									if mem, ok := c.Resources.Requests[corev1.ResourceMemory]; ok {
										memReqBytes += mem.Value()
									}
								}
							}
						}
					}

					if cpuCapMillis > 0 && memCapBytes > 0 {
						d.Metrics = &mcpMetrics{
							CPUUsagePercent:   int(cpuUsageMillis * 100 / cpuCapMillis),
							CPURequestPercent: int(cpuReqMillis * 100 / cpuCapMillis),
							MemUsagePercent:   int(memUsageBytes * 100 / memCapBytes),
							MemRequestPercent: int(memReqBytes * 100 / memCapBytes),
						}
					}
				}
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
}

// helmStatusPriority returns a sort priority for Helm release statuses.
// Lower values sort first — failed and unhealthy releases are surfaced first.
func helmStatusPriority(status, resourceHealth string) int {
	if status == "failed" {
		return 0
	}
	if status == "pending-install" || status == "pending-upgrade" || status == "pending-rollback" {
		return 1
	}
	switch resourceHealth {
	case "unhealthy":
		return 2
	case "degraded":
		return 3
	}
	return 4
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
