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

	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	topology "github.com/skyhook-io/radar/pkg/topology"
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
			"Use after list_resources to drill into a specific resource. " +
			"Optionally include related context (events, relationships, metrics, logs) " +
			"using the 'include' parameter (comma-separated) to avoid extra tool calls.",
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

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_changes",
		Description: "Get recent resource changes (creates, updates, deletes) from the cluster timeline. " +
			"Use to investigate what changed before an incident. " +
			"Filter by namespace, resource kind, or specific resource name.",
		Annotations: readOnly,
	}, logToolCall("get_changes", handleGetChanges))

	// --- Audit tool (read-only) ---

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_cluster_audit",
		Description: "Run best-practice checks against cluster resources and return findings " +
			"with remediation guidance. Checks cover security (running as root, privileged " +
			"containers, dangerous capabilities), reliability (missing probes, single replicas, " +
			"no PDB), and efficiency (missing resource requests/limits). " +
			"Each finding includes what's wrong and how to fix it. " +
			"Respects user's audit settings (ignored namespaces, disabled checks). " +
			"Filter by namespace, category, or severity.",
		Annotations: readOnly,
	}, logToolCall("get_cluster_audit", handleGetAudit))

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

	mcp.AddTool(server, &mcp.Tool{
		Name: "apply_resource",
		Description: "Create or update a Kubernetes resource from a YAML manifest. " +
			"In 'apply' mode (default), performs a server-side apply (idempotent create-or-update). " +
			"In 'create' mode, performs a strict create that fails if the resource already exists. " +
			"Supports multi-document YAML separated by '---'. " +
			"Use dry_run to validate without persisting changes.",
		Annotations: &mcp.ToolAnnotations{
			DestructiveHint: boolPtr(false),
		},
	}, logToolCall("apply_resource", handleApplyResource))

	mcp.AddTool(server, &mcp.Tool{
		Name: "manage_node",
		Description: "Perform operations on a Kubernetes node. " +
			"Supported actions: 'cordon' marks the node as unschedulable (no new pods will be scheduled), " +
			"'uncordon' marks the node as schedulable again, " +
			"'drain' cordons the node and evicts all non-DaemonSet pods. " +
			"Drain options: 'delete_empty_dir_data' (allow evicting pods with emptyDir volumes), " +
			"'force' (evict pods not managed by a controller), 'timeout' (seconds, default 60).",
		Annotations: &mcp.ToolAnnotations{
			DestructiveHint: boolPtr(false),
		},
	}, logToolCall("manage_node", handleManageNode))
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
	Include   string `json:"include,omitempty" jsonschema:"comma-separated extras to include: events, relationships, metrics, logs"`
}

type topologyInput struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"filter to a specific namespace"`
	View      string `json:"view,omitempty" jsonschema:"view mode: traffic for network flow or resources for ownership hierarchy"`
	Format    string `json:"format,omitempty" jsonschema:"output format: graph (default, full node/edge data) or summary (text description of resource chains)"`
}

type eventsInput struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"filter to a specific namespace"`
	Limit     int    `json:"limit,omitempty" jsonschema:"max 100, default 20"`
	Kind      string `json:"kind,omitempty" jsonschema:"filter to events involving this resource kind (e.g. Pod, Deployment)"`
	Name      string `json:"name,omitempty" jsonschema:"filter to events involving this resource name"`
}

type getChangesInput struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"filter to a specific namespace"`
	Kind      string `json:"kind,omitempty" jsonschema:"filter to a resource kind (e.g. Deployment, Pod)"`
	Name      string `json:"name,omitempty" jsonschema:"filter to a specific resource name"`
	Since     string `json:"since,omitempty" jsonschema:"duration to look back, e.g. 1h, 30m, 24h (default 1h)"`
	Limit     int    `json:"limit,omitempty" jsonschema:"max changes to return (default 20, max 50)"`
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
	var resourceData any
	obj, err := k8s.FetchResource(cache, kind, namespace, name)
	if err == k8s.ErrUnknownKind {
		// Fall through to dynamic cache for CRDs
		u, dynErr := cache.GetDynamicWithGroup(ctx, kind, namespace, name, "")
		if dynErr != nil {
			return nil, nil, fmt.Errorf("resource not found: %w", dynErr)
		}
		resourceData = aicontext.MinifyUnstructured(u, aicontext.LevelDetail)
	} else if err != nil {
		return nil, nil, fmt.Errorf("resource not found: %w", err)
	} else {
		k8s.SetTypeMeta(obj)
		minified, minErr := aicontext.Minify(obj, aicontext.LevelDetail)
		if minErr != nil {
			return nil, nil, fmt.Errorf("failed to minify: %w", minErr)
		}
		resourceData = minified
	}

	includes := parseIncludes(input.Include)
	if len(includes) == 0 {
		return toJSONResult(resourceData)
	}

	// Build enriched response with requested extras
	result := map[string]any{"resource": resourceData}
	attachResourceExtras(ctx, cache, result, includes, kind, namespace, name)
	return toJSONResult(result)
}

// attachResourceExtras populates optional extras (events, relationships, metrics, logs)
// on the result map based on the includes set.
func attachResourceExtras(ctx context.Context, cache *k8s.ResourceCache, result map[string]any, includes map[string]bool, kind, namespace, name string) {
	if includes["events"] {
		if eventLister := cache.Events(); eventLister != nil {
			var events []*corev1.Event
			var listErr error
			if namespace != "" {
				events, listErr = eventLister.Events(namespace).List(labels.Everything())
			} else {
				events, listErr = eventLister.List(labels.Everything())
			}
			if listErr != nil {
				log.Printf("[mcp] Failed to list events for %s/%s/%s: %v", kind, namespace, name, listErr)
			}
			// Filter to events involving this resource
			var matched []corev1.Event
			displayKind := normalizeDisplayKind(kind)
			for _, e := range events {
				if strings.EqualFold(e.InvolvedObject.Kind, displayKind) && e.InvolvedObject.Name == name {
					matched = append(matched, *e)
				}
			}
			if len(matched) > 0 {
				deduplicated := aicontext.DeduplicateEvents(matched)
				if len(deduplicated) > 10 {
					deduplicated = deduplicated[:10]
				}
				result["events"] = deduplicated
			}
		}
	}

	if includes["relationships"] {
		opts := topology.DefaultBuildOptions()
		if namespace != "" {
			opts.Namespaces = []string{namespace}
		}
		builder := topology.NewBuilder(k8s.NewTopologyResourceProvider(k8s.GetResourceCache())).WithDynamic(k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery()))
		topo, err := builder.Build(opts)
		if err != nil {
			log.Printf("[mcp] Failed to build topology for relationships %s/%s/%s: %v", kind, namespace, name, err)
		} else {
			displayKind := normalizeDisplayKind(kind)
			if rels := topology.GetRelationships(displayKind, namespace, name, topo,
				k8s.NewTopologyResourceProvider(k8s.GetResourceCache()),
				k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery())); rels != nil {
				result["relationships"] = rels
			}
		}
	}

	if includes["metrics"] {
		if isPodKind(kind) {
			if metrics, err := k8s.GetPodMetrics(ctx, namespace, name); err == nil {
				result["metrics"] = metrics
			}
		}
	}

	if includes["logs"] {
		if isPodKind(kind) {
			if client := k8s.ClientFromContext(ctx); client != nil {
				tailLines := int64(100)
				opts := &corev1.PodLogOptions{TailLines: &tailLines}
				stream, err := client.CoreV1().Pods(namespace).GetLogs(name, opts).Stream(ctx)
				if err != nil {
					log.Printf("[mcp] Failed to get logs for %s/%s: %v", namespace, name, err)
				} else {
					defer stream.Close()
					data, readErr := io.ReadAll(stream)
					if readErr != nil {
						log.Printf("[mcp] Failed to read logs for %s/%s: %v", namespace, name, readErr)
					} else {
						result["logs"] = aicontext.FilterLogs(string(data))
					}
				}
			}
		}
	}
}

// normalizeDisplayKind converts a lowercase kind to its display form for matching
// against InvolvedObject.Kind and topology node kinds (e.g. "pod" → "Pod").
func normalizeDisplayKind(kind string) string {
	displayKinds := map[string]string{
		"pod": "Pod", "pods": "Pod",
		"service": "Service", "services": "Service",
		"deployment": "Deployment", "deployments": "Deployment",
		"daemonset": "DaemonSet", "daemonsets": "DaemonSet",
		"statefulset": "StatefulSet", "statefulsets": "StatefulSet",
		"replicaset": "ReplicaSet", "replicasets": "ReplicaSet",
		"ingress": "Ingress", "ingresses": "Ingress",
		"configmap": "ConfigMap", "configmaps": "ConfigMap",
		"secret": "Secret", "secrets": "Secret",
		"job": "Job", "jobs": "Job",
		"cronjob": "CronJob", "cronjobs": "CronJob",
		"node": "Node", "nodes": "Node",
		"namespace": "Namespace", "namespaces": "Namespace",
		"persistentvolumeclaim": "PersistentVolumeClaim", "persistentvolumeclaims": "PersistentVolumeClaim",
		"persistentvolume": "PersistentVolume", "persistentvolumes": "PersistentVolume",
		"storageclass": "StorageClass", "storageclasses": "StorageClass",
		"horizontalpodautoscaler": "HorizontalPodAutoscaler", "horizontalpodautoscalers": "HorizontalPodAutoscaler",
		"poddisruptionbudget": "PodDisruptionBudget", "poddisruptionbudgets": "PodDisruptionBudget",
		"role": "Role", "roles": "Role",
		"clusterrole": "ClusterRole", "clusterroles": "ClusterRole",
		"rolebinding": "RoleBinding", "rolebindings": "RoleBinding",
		"clusterrolebinding": "ClusterRoleBinding", "clusterrolebindings": "ClusterRoleBinding",
		"serviceaccount": "ServiceAccount", "serviceaccounts": "ServiceAccount",
		"ingressclass": "IngressClass", "ingressclasses": "IngressClass",
		"priorityclass": "PriorityClass", "priorityclasses": "PriorityClass",
		"runtimeclass": "RuntimeClass", "runtimeclasses": "RuntimeClass",
		"lease": "Lease", "leases": "Lease",
		"mutatingwebhookconfiguration": "MutatingWebhookConfiguration", "mutatingwebhookconfigurations": "MutatingWebhookConfiguration",
		"validatingwebhookconfiguration": "ValidatingWebhookConfiguration", "validatingwebhookconfigurations": "ValidatingWebhookConfiguration",
	}
	if display, ok := displayKinds[kind]; ok {
		return display
	}
	return kind
}

func isPodKind(kind string) bool {
	return kind == "pod" || kind == "pods"
}

func handleGetChanges(ctx context.Context, req *mcp.CallToolRequest, input getChangesInput) (*mcp.CallToolResult, any, error) {
	store := timeline.GetStore()
	if store == nil {
		return nil, nil, fmt.Errorf("timeline store not initialized")
	}

	since := 1 * time.Hour
	if input.Since != "" {
		parsed, err := time.ParseDuration(input.Since)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid duration %q: %w", input.Since, err)
		}
		if parsed <= 0 {
			return nil, nil, fmt.Errorf("duration must be positive, got %q", input.Since)
		}
		since = parsed
	}

	limit := 20
	if input.Limit > 0 {
		limit = min(input.Limit, 50)
	}

	queryOpts := timeline.QueryOptions{
		Since:        time.Now().Add(-since),
		FilterPreset: "default",
	}
	if input.Namespace != "" {
		queryOpts.Namespaces = []string{input.Namespace}
	}
	if input.Kind != "" {
		queryOpts.Kinds = []string{input.Kind}
	}
	// When name filtering is needed client-side, fetch more to compensate for post-filter reduction
	if input.Name != "" {
		queryOpts.Limit = limit * 10
	} else {
		queryOpts.Limit = limit
	}

	events, err := store.Query(ctx, queryOpts)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to query timeline: %w", err)
	}

	// Client-side name filter (QueryOptions doesn't support name filtering)
	if input.Name != "" {
		filtered := events[:0]
		for _, e := range events {
			if e.Name == input.Name {
				filtered = append(filtered, e)
			}
		}
		events = filtered
		if len(events) > limit {
			events = events[:limit]
		}
	}

	changes := make([]mcpChange, 0, len(events))
	for _, e := range events {
		summary := ""
		if e.Diff != nil && e.Diff.Summary != "" {
			summary = e.Diff.Summary
		} else if e.Message != "" {
			summary = k8s.Truncate(e.Message, 100)
		}
		changes = append(changes, mcpChange{
			Kind:       e.Kind,
			Namespace:  e.Namespace,
			Name:       e.Name,
			ChangeType: string(e.EventType),
			Summary:    summary,
			Timestamp:  e.Timestamp.Format(time.RFC3339),
		})
	}

	return toJSONResult(changes)
}

func handleGetTopology(ctx context.Context, req *mcp.CallToolRequest, input topologyInput) (*mcp.CallToolResult, any, error) {
	opts := topology.DefaultBuildOptions()
	if input.Namespace != "" {
		opts.Namespaces = []string{input.Namespace}
	}
	if input.View == "traffic" {
		opts.ViewMode = topology.ViewModeTraffic
	}

	builder := topology.NewBuilder(k8s.NewTopologyResourceProvider(k8s.GetResourceCache())).WithDynamic(k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery()))
	topo, err := builder.Build(opts)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to build topology: %w", err)
	}

	if strings.ToLower(input.Format) == "summary" {
		return toJSONResult(buildTopologySummary(topo))
	}

	return toJSONResult(topo)
}

// topologySummary is an LLM-friendly text representation of the topology.
type topologySummary struct {
	Namespaces []nsSummary    `json:"namespaces"`
	Problems   []string       `json:"problems,omitempty"`
	Stats      topologyStats  `json:"stats"`
}

type nsSummary struct {
	Namespace string   `json:"namespace"`
	Chains    []string `json:"chains"`
}

type topologyStats struct {
	Nodes int `json:"nodes"`
	Edges int `json:"edges"`
}

func buildTopologySummary(topo *topology.Topology) topologySummary {
	// Build lookup maps
	nodeByID := make(map[string]*topology.Node, len(topo.Nodes))
	for i := range topo.Nodes {
		nodeByID[topo.Nodes[i].ID] = &topo.Nodes[i]
	}

	// Build adjacency: source → targets
	children := make(map[string][]string)
	parents := make(map[string][]string)
	for _, e := range topo.Edges {
		children[e.Source] = append(children[e.Source], e.Target)
		parents[e.Target] = append(parents[e.Target], e.Source)
	}

	// Find root nodes (no incoming edges)
	roots := make(map[string]bool)
	for _, n := range topo.Nodes {
		if len(parents[n.ID]) == 0 {
			roots[n.ID] = true
		}
	}

	// Walk chains from roots, group by namespace
	visited := make(map[string]bool)
	nsChains := make(map[string][]string)
	var problems []string

	for _, n := range topo.Nodes {
		if !roots[n.ID] || visited[n.ID] {
			continue
		}
		chain := walkChain(n.ID, nodeByID, children, visited, 0)
		if chain == "" {
			continue
		}
		ns := nodeNamespace(nodeByID[n.ID])
		nsChains[ns] = append(nsChains[ns], chain)
	}

	// Also walk any unvisited nodes (cycles or isolated nodes)
	for _, n := range topo.Nodes {
		if visited[n.ID] {
			continue
		}
		desc := describeNode(&n)
		ns := nodeNamespace(&n)
		nsChains[ns] = append(nsChains[ns], desc)
		visited[n.ID] = true
	}

	// Detect problems
	for _, n := range topo.Nodes {
		if n.Status == topology.StatusUnhealthy || n.Status == topology.StatusDegraded {
			problems = append(problems, fmt.Sprintf("%s %s: %s", n.Kind, n.Name, n.Status))
		}
	}

	// Build sorted namespace list
	var namespaces []nsSummary
	sortedNs := make([]string, 0, len(nsChains))
	for ns := range nsChains {
		sortedNs = append(sortedNs, ns)
	}
	sort.Strings(sortedNs)
	for _, ns := range sortedNs {
		namespaces = append(namespaces, nsSummary{
			Namespace: ns,
			Chains:    nsChains[ns],
		})
	}

	return topologySummary{
		Namespaces: namespaces,
		Problems:   problems,
		Stats:      topologyStats{Nodes: len(topo.Nodes), Edges: len(topo.Edges)},
	}
}

// walkChain recursively describes a resource chain from a root node.
func walkChain(nodeID string, nodeByID map[string]*topology.Node, children map[string][]string, visited map[string]bool, depth int) string {
	if depth > 10 || visited[nodeID] {
		return ""
	}
	visited[nodeID] = true

	node, ok := nodeByID[nodeID]
	if !ok {
		return ""
	}

	desc := describeNode(node)
	kids := children[nodeID]
	if len(kids) == 0 {
		return desc
	}

	// For single-child chains, flatten into arrows
	if len(kids) == 1 {
		childDesc := walkChain(kids[0], nodeByID, children, visited, depth+1)
		if childDesc != "" {
			return desc + " → " + childDesc
		}
		return desc
	}

	// For multiple children, list them
	var childDescs []string
	for _, kid := range kids {
		childDesc := walkChain(kid, nodeByID, children, visited, depth+1)
		if childDesc != "" {
			childDescs = append(childDescs, childDesc)
		}
	}
	if len(childDescs) == 0 {
		return desc
	}
	if len(childDescs) == 1 {
		return desc + " → " + childDescs[0]
	}
	return desc + " → [" + strings.Join(childDescs, ", ") + "]"
}

func describeNode(n *topology.Node) string {
	desc := fmt.Sprintf("%s/%s", n.Kind, n.Name)

	// Add status annotation for unhealthy nodes
	if n.Status == topology.StatusUnhealthy {
		desc += " (unhealthy)"
	} else if n.Status == topology.StatusDegraded {
		desc += " (degraded)"
	}

	// Add useful data annotations
	if n.Data != nil {
		if ready, ok := n.Data["readyReplicas"]; ok {
			if desired, ok2 := n.Data["replicas"]; ok2 {
				desc += fmt.Sprintf(" (%v/%v ready)", ready, desired)
			}
		}
		if host, ok := n.Data["host"]; ok && host != "" {
			desc += fmt.Sprintf(" [%v]", host)
		}
	}

	return desc
}

func nodeNamespace(n *topology.Node) string {
	if n.Data != nil {
		if ns, ok := n.Data["namespace"].(string); ok && ns != "" {
			return ns
		}
	}
	return "(cluster)"
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

	// Filter by InvolvedObject kind/name if specified
	if input.Kind != "" || input.Name != "" {
		filtered := events[:0]
		for _, e := range events {
			if input.Kind != "" && !strings.EqualFold(e.InvolvedObject.Kind, input.Kind) {
				continue
			}
			if input.Name != "" && e.InvolvedObject.Name != input.Name {
				continue
			}
			filtered = append(filtered, e)
		}
		events = filtered
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
	clientset := k8s.ClientFromContext(ctx)
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
	RecentChanges  []mcpChange      `json:"recentChanges,omitempty"`
	WarningEvents  int              `json:"warningEvents"`
	TopWarnings    []mcpWarning     `json:"topWarnings"`
	HelmReleases   mcpHelmSummary   `json:"helmReleases"`
	Metrics        *mcpMetrics      `json:"metrics,omitempty"`
	TopologyNodes  int              `json:"topologyNodes"`
	TopologyEdges  int              `json:"topologyEdges"`
	ResourceCounts map[string]int   `json:"resourceCounts"`
}

type mcpChange struct {
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace,omitempty"`
	Name       string `json:"name"`
	ChangeType string `json:"changeType"`
	Summary    string `json:"summary,omitempty"`
	Timestamp  string `json:"timestamp"`
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
	Group     string `json:"group,omitempty"`
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

	// Workload/HPA/CronJob/Node problems (excluding pods, handled above)
	for _, p := range k8s.DetectProblems(cache, namespace) {
		if len(d.Problems) >= 10 {
			break
		}
		d.Problems = append(d.Problems, mcpProblem{
			Kind:      p.Kind,
			Namespace: p.Namespace,
			Name:      p.Name,
			Reason:    p.Reason,
			Message:   p.Message,
			Age:       p.Age,
		})
	}

	// CAPI problems (Cluster API resources)
	for _, p := range k8s.DetectCAPIProblems(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery(), namespace) {
		if len(d.Problems) >= 10 {
			break
		}
		d.Problems = append(d.Problems, mcpProblem{
			Kind:      p.Kind,
			Namespace: p.Namespace,
			Name:      p.Name,
			Group:     p.Group,
			Reason:    p.Reason,
			Message:   p.Message,
			Age:       p.Age,
		})
	}

	// Deployment resource count
	if depLister := cache.Deployments(); depLister != nil {
		if namespace != "" {
			items, _ := depLister.Deployments(namespace).List(labels.Everything())
			d.ResourceCounts["deployments"] = len(items)
		} else {
			items, _ := depLister.List(labels.Everything())
			d.ResourceCounts["deployments"] = len(items)
		}
	}

	// Node health summary (cluster-scoped, not filtered by namespace)
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
		username, groups := userFromContext(ctx)
		releases, err := helmClient.ListReleasesAsUser(namespace, username, groups)
		if err != nil {
			// Not fatal for the dashboard — a viewer with no helm access
			// still sees everything else. Log so the absence isn't silent.
			log.Printf("[mcp] Dashboard helm list failed: %v", err)
		} else {
			d.HelmReleases.Total = len(releases)

			// Sort: failed/pending-install first, then unhealthy/degraded
			sort.SliceStable(releases, func(i, j int) bool {
				return helm.StatusPriority(releases[i].Status, releases[i].ResourceHealth) < helm.StatusPriority(releases[j].Status, releases[j].ResourceHealth)
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

	// Metrics (best-effort — silently skip if metrics-server unavailable).
	// Metrics-server forwards the impersonation headers, so a user without
	// metrics.k8s.io/nodes access gets a 403 here and the field is left empty.
	if client := k8s.ClientFromContext(ctx); client != nil {
		data, err := client.CoreV1().RESTClient().Get().
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
	builder := topology.NewBuilder(k8s.NewTopologyResourceProvider(k8s.GetResourceCache())).WithDynamic(k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery()))
	if topo, err := builder.Build(opts); err == nil {
		d.TopologyNodes = len(topo.Nodes)
		d.TopologyEdges = len(topo.Edges)
	} else {
		log.Printf("[mcp] Failed to build topology for dashboard: %v", err)
	}

	// Correlate recent changes with problems — only show changes for broken resources
	if store := timeline.GetStore(); store != nil && len(d.Problems) > 0 {
		problemKeys := make(map[string]bool, len(d.Problems))
		for _, p := range d.Problems {
			problemKeys[fmt.Sprintf("%s/%s/%s", p.Kind, p.Namespace, p.Name)] = true
		}

		queryOpts := timeline.QueryOptions{
			Since:        now.Add(-1 * time.Hour),
			Limit:        20,
			FilterPreset: "workloads",
		}
		if namespace != "" {
			queryOpts.Namespaces = []string{namespace}
		}
		changes, err := store.Query(ctx, queryOpts)
		if err != nil {
			log.Printf("[mcp] Failed to query timeline for dashboard changes: %v", err)
		}
		if err == nil {
			for _, c := range changes {
				key := fmt.Sprintf("%s/%s/%s", c.Kind, c.Namespace, c.Name)
				// Also check owner chain (e.g. Pod problem → Deployment change)
				ownerKey := ""
				if c.Owner != nil {
					ownerKey = fmt.Sprintf("%s/%s/%s", c.Owner.Kind, c.Namespace, c.Owner.Name)
				}
				if problemKeys[key] || (ownerKey != "" && problemKeys[ownerKey]) {
					summary := ""
					if c.Diff != nil && c.Diff.Summary != "" {
						summary = c.Diff.Summary
					} else if c.Message != "" {
						summary = k8s.Truncate(c.Message, 100)
					}
					d.RecentChanges = append(d.RecentChanges, mcpChange{
						Kind:       c.Kind,
						Namespace:  c.Namespace,
						Name:       c.Name,
						ChangeType: string(c.EventType),
						Summary:    summary,
						Timestamp:  c.Timestamp.Format(time.RFC3339),
					})
					if len(d.RecentChanges) >= 5 {
						break
					}
				}
			}
		}
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
