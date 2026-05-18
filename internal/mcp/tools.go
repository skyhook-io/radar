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

	"github.com/skyhook-io/radar/internal/filter"
	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/search"
	"github.com/skyhook-io/radar/internal/timeline"
	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
	topology "github.com/skyhook-io/radar/pkg/topology"
)

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
		Name: "get_neighborhood",
		Description: "Get the BFS-expanded neighborhood of a specific resource — the slice " +
			"of the topology graph immediately relevant to one root. Cheaper and more " +
			"focused than get_topology when you already know which resource you care " +
			"about. Profile is 'auto' (default — picks a bounded edge set from the root " +
			"kind) or 'all' (every edge type). Hops controls BFS depth (default 1, max " +
			"2). Nodes are RBAC-filtered against the caller; dropped neighbors are " +
			"listed in `omitted` with reason=rbac_denied. If max_nodes is exceeded " +
			"mid-expansion, truncated=true is set and a partial subgraph is returned.",
		Annotations: readOnly,
	}, logToolCall("get_neighborhood", handleGetNeighborhood))

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

	// --- Packages tool (read-only) ---
	//
	// Higher-level than list_helm_releases: collapses Helm releases,
	// workload labels, CRD registrations, and GitOps declarations
	// (Argo Applications + Flux HelmReleases/Kustomizations) into a
	// unified "what's installed in this cluster" view with source
	// provenance. Each row's `sources` field shows which detection
	// channels voted "this is installed" — H, L, C, A, F.
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_packages",
		Description: "List installed packages (Helm releases, label-managed workloads, CRDs, " +
			"Argo Applications, Flux HelmReleases + Kustomizations) with their sources, " +
			"versions, and health. Each row carries a `sources` array (H=Helm API, " +
			"L=workload labels, C=CRD registrations, A=Argo declaration, F=Flux declaration) " +
			"so the caller can see WHY this package is detected, plus a `contributors` " +
			"array with per-source detail (each source's view of health/version, plus the " +
			"GitOps controller resource identity in declarationName/declarationNamespace " +
			"for sources A and F). Aggregated row-level health is worst-of contributors; " +
			"row-level version is first-source-priority — read `contributors` to detect " +
			"same-cluster disagreement. Use to answer 'what's installed?' / 'what version " +
			"of cert-manager is running?' / 'are there orphaned operators?' in a single " +
			"call instead of combining list_helm_releases + list_resources + manual merge. " +
			"Filter by namespace, source, or chart substring. Response includes " +
			"`sourcesErrored` listing any sources that failed (e.g. RBAC denied for Helm " +
			"release secrets, Helm client not initialized, GitOps informer errors other " +
			"than the controller's CRDs being absent). When this is non-empty, results " +
			"are still returned but are partial — fewer rows than expected may indicate a " +
			"dropped source rather than nothing installed. ArgoCD/FluxCD CRDs that are " +
			"simply not installed in the cluster do NOT appear in sourcesErrored.",
		Annotations: readOnly,
	}, logToolCall("list_packages", handleListPackages))

	// --- Issues (read-only) ---

	mcp.AddTool(server, &mcp.Tool{
		Name: "issues",
		Description: "Unified cluster-health view. Combines hardcoded problem detection " +
			"(failing Deployments / StatefulSets / CronJobs / HPAs / Nodes / Jobs / PVCs), " +
			"recent K8s Warning events, and a generic CRD .status.conditions[] " +
			"fallback that lights up Argo / Flux / Knative / Crossplane / cert-manager / " +
			"KEDA without per-integration code. Severity is normalized to " +
			"critical / warning / info. Audit findings (best-practice scan) are excluded " +
			"by default — pass source=audit to opt them in. Use this instead of " +
			"get_dashboard when you want the full health picture across all sources, or " +
			"to filter by severity / source / kind / namespace.",
		Annotations: readOnly,
	}, logToolCall("issues", handleIssuesTool))

	// --- Search (read-only) ---

	mcp.AddTool(server, &mcp.Tool{
		Name: "search",
		Description: "Free-text resource search across this cluster's cache. Matches on " +
			"name, namespace, label values, annotation values, container images, and " +
			"kind. Tokens are AND'd. Modifiers: kind:Pod, ns:foo, label:app=bar, " +
			"image:redis. Returns ranked hits with optional summary or raw object. " +
			"Use this instead of list_resources when you don't already know the kind, " +
			"namespace, or exact name — for example 'find anything called redis' or " +
			"'show me everything pulling from quay.io/x'. Searches typed kinds plus " +
			"any CRDs already warmed in the cache; cold CRDs need a list_resources " +
			"call first to start watching.",
		Annotations: readOnly,
	}, logToolCall("search", handleSearch))

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
			"For ArgoCD: actions are 'sync' (trigger deployment), 'refresh', 'terminate', 'rollback', " +
			"'suspend' (disable auto-sync), 'resume' (re-enable auto-sync). Resource kind is always Application. " +
			"For FluxCD: actions are 'reconcile' (trigger sync), 'sync-with-source', 'suspend', 'resume'. " +
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
	Group     string `json:"group,omitempty" jsonschema:"API group when the kind is ambiguous (e.g. serving.knative.dev for Knative Service vs core Service)"`
	Namespace string `json:"namespace,omitempty" jsonschema:"filter to a specific namespace"`
}

type getResourceInput struct {
	Kind      string `json:"kind" jsonschema:"resource kind, e.g. pod, deployment, service"`
	Group     string `json:"group,omitempty" jsonschema:"API group when the kind is ambiguous (e.g. cluster.x-k8s.io for CAPI Cluster vs CNPG Cluster)"`
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

type searchInput struct {
	Q       string `json:"q" jsonschema:"search string. Free tokens AND'd. Modifiers: kind:Pod, ns:foo, label:k=v, image:redis"`
	Limit   int    `json:"limit,omitempty" jsonschema:"max hits returned (default 50, max 500)"`
	Include string `json:"include,omitempty" jsonschema:"per-hit detail: summary (default), raw, or none"`
	Filter  string `json:"filter,omitempty" jsonschema:"optional CEL boolean expression run against each candidate K8s object. Bindings: kind, apiVersion, metadata, spec, status, labels, annotations. Use has(x.y) before optional fields. Examples: 'kind == \"Pod\" && status.phase == \"Failed\"', 'labels[\"app\"] == \"cart\"', 'has(status.readyReplicas) && status.readyReplicas == 0'"`
}

type issuesInput struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"filter to one namespace"`
	Severity  string `json:"severity,omitempty" jsonschema:"comma-separated: critical,warning"`
	Source    string `json:"source,omitempty" jsonschema:"comma-separated: problem,audit,event,condition. Defaults to problem+condition only. Pass 'event' to opt in K8s Warning events (off by default — they flood thousands per cluster and mostly duplicate problem-source rows). Pass 'audit' to opt in best-practice findings (off by default — 50–200 per cluster)."`
	Kind      string `json:"kind,omitempty" jsonschema:"comma-separated kind filter (e.g. Deployment,Pod)"`
	Since     string `json:"since,omitempty" jsonschema:"event lookback window, e.g. 15m or 1h. Only affects the event source; when events are enabled and since is omitted, defaults to 1h to avoid pulling the full event-cache backlog."`
	Limit     int    `json:"limit,omitempty" jsonschema:"max issues returned (default 200, max 1000)"`
	Filter    string `json:"filter,omitempty" jsonschema:"optional CEL boolean expression run against each composed Issue. Bindings: severity, source, kind, group, ns (the namespace — note: use 'ns' not 'namespace' because the latter is a CEL reserved word), name, reason, message, count (int), cluster, last_seen (unix seconds). Examples: 'severity == \"critical\" && count > 5', 'source == \"condition\" && ns.startsWith(\"prod-\")'"`
}

// Tool handlers

func handleGetDashboard(ctx context.Context, req *mcp.CallToolRequest, input dashboardInput) (*mcp.CallToolResult, any, error) {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil, nil, fmt.Errorf("not connected to cluster")
	}

	// Dashboard summary doesn't currently take a multi-namespace input, so we
	// gate on the requested namespace, or on cluster-wide namespaced access
	// when the caller doesn't pin a namespace. Cluster-scoped dashboard fields
	// are still gated per kind below.
	if input.Namespace != "" {
		if !checkNamespaceAccess(ctx, input.Namespace) {
			return nil, nil, fmt.Errorf("forbidden: no access to namespace %q", input.Namespace)
		}
	} else if filterNamespacesForUser(ctx, nil) != nil {
		return nil, nil, fmt.Errorf("forbidden: dashboard summary requires cluster-wide namespace access — pass a specific namespace you have access to")
	}

	dashboard := buildDashboard(ctx, cache, input.Namespace, canReadClusterScopedKind(ctx, "nodes", "", "list"), canReadClusterScopedKind(ctx, "namespaces", "", "list"))
	return toJSONResult(dashboard)
}

func handleListResources(ctx context.Context, req *mcp.CallToolRequest, input listResourcesInput) (*mcp.CallToolResult, any, error) {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil, nil, fmt.Errorf("not connected to cluster")
	}

	kind := strings.ToLower(input.Kind)
	group := input.Group
	var requested []string
	if input.Namespace != "" {
		requested = []string{input.Namespace}
	}

	// Cluster-scoped kinds (static cluster-only list + cluster-scoped CRDs
	// from discovery) are gated per-kind via SAR. Run BEFORE the namespace
	// filter check so users with explicit cluster-scoped RBAC but no
	// namespace access can still read those resources.
	//
	// "namespaces" is cluster-scoped at the K8s API. Full Namespace objects
	// require explicit list-namespaces SAR. Read access to resources IN a
	// namespace (list pods etc.) does not imply read access to the Namespace
	// resource itself. Restricted users use the dedicated list_namespaces
	// MCP tool, which serves a synthesized {name, status} view.
	isNamespacesKind := kind == "namespaces" || kind == "namespace"
	clusterScoped, _, _ := k8s.ClassifyKindScope(kind, group)
	if clusterScoped && !isNamespacesKind {
		if !canReadClusterScopedKind(ctx, kind, group, "list") {
			return toJSONResult([]any{})
		}
	}
	if isNamespacesKind && !canReadClusterScopedKind(ctx, "namespaces", "", "list") {
		return toJSONResult([]any{})
	}

	// Filter the requested namespaces against the user's RBAC. Returns:
	//   nil       — auth off or cluster-wide namespaced access: pass through
	//   []string{} — user has no namespace access
	//   [...]     — restrict reads to these namespaces
	allowed := filterNamespacesForUser(ctx, requested)
	if !clusterScoped && allowed != nil && len(allowed) == 0 {
		return toJSONResult([]any{})
	}

	// Per-kind RBAC inside a namespace for Secrets. The cache reads as the SA,
	// and the chart can grant the SA cluster-wide secrets (rbac.secrets,
	// rbac.helm, auth.mode != "none", or cloud.enabled — Helm release
	// visibility), so per-user RBAC must gate the read. canReadInNamespace
	// and filterNamespacesByCanRead pass through when no user is on context
	// (auth-mode=none — SA RBAC at the cache layer is the only gate). Other
	// namespaced kinds are deferred.
	if kind == "secrets" || kind == "secret" {
		if allowed == nil {
			if !canReadInNamespace(ctx, "", "secrets", "", "list") {
				return toJSONResult([]any{})
			}
		} else {
			allowed = filterNamespacesByCanRead(ctx, "", "secrets", "list", allowed)
			if len(allowed) == 0 {
				return toJSONResult([]any{})
			}
		}
	}

	// For cluster-scoped reads, force a cluster-wide list (don't iterate
	// per allowed namespace — cluster-scoped resources don't live there).
	listScope := allowed
	if clusterScoped {
		listScope = nil
	}

	// Try typed cache first
	objs, err := k8s.FetchResourceList(cache, kind, listScope)
	if err == k8s.ErrUnknownKind {
		// Fall through to dynamic cache for CRDs. ClassifyKindScope/SAR
		// above already authorized cluster-scoped CRDs; namespaced CRDs
		// are scoped via listScope.
		return listDynamicResources(ctx, cache, kind, group, listScope)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list %s: %w", kind, err)
	}

	// Cluster-scoped kinds (FetchResourceList ignores `allowed` for them) and
	// "namespaces" need post-filtering for namespace-restricted users.
	// Skip post-filter when the user is cluster-scoped-authorized for a
	// non-namespaces kind (clusterScoped && !allowed-restricted) — they
	// should see the full cluster-scoped result. For "namespaces" the
	// per-user filter ALWAYS applies even though it's cluster-scoped at
	// the API.
	if allowed != nil && (!clusterScoped || isNamespacesKind) {
		objs = retainAllowedObjects(objs, allowed, kind)
	}

	results, err := aicontext.MinifyList(objs, aicontext.LevelSummary)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to minify: %w", err)
	}

	return toJSONResult(results)
}

func listDynamicResources(ctx context.Context, cache *k8s.ResourceCache, kind, group string, namespaces []string) (*mcp.CallToolResult, any, error) {
	var allItems []any
	if len(namespaces) > 0 {
		for _, ns := range namespaces {
			items, err := cache.ListDynamicWithGroup(ctx, kind, ns, group)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to list %s: %w", kind, err)
			}
			for _, item := range items {
				allItems = append(allItems, aicontext.MinifyUnstructured(item, aicontext.LevelSummary))
			}
		}
	} else {
		items, err := cache.ListDynamicWithGroup(ctx, kind, "", group)
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
	group := input.Group
	namespace := input.Namespace
	name := input.Name

	// Cluster-scoped GETs are gated per-kind via SAR — that catches both
	// the static cluster-only list and dynamic cluster-scoped CRDs (via
	// discovery). Run BEFORE the namespace check so users with cluster-
	// scoped RBAC but no namespace access can still read those resources.
	//
	// "namespaces" is cluster-scoped at the K8s API but exposed as a
	// per-user filtered list — gate via the user's namespace access for
	// the requested name, not via cluster-scoped SAR.
	isNamespacesKind := kind == "namespaces" || kind == "namespace"
	clusterScoped, _, _ := k8s.ClassifyKindScope(kind, group)
	if isNamespacesKind {
		// Full Namespace object access requires explicit get-namespaces SAR.
		// Read access to resources IN a namespace does not imply read access
		// to the Namespace object itself.
		if !canReadClusterScopedKind(ctx, "namespaces", "", "get") {
			return nil, nil, fmt.Errorf("forbidden: no access to namespace %q", name)
		}
	} else if clusterScoped {
		if !canReadClusterScopedKind(ctx, kind, group, "get") {
			return nil, nil, fmt.Errorf("forbidden: %s requires explicit cluster-scoped RBAC", kind)
		}
	} else if !checkNamespaceAccess(ctx, namespace) {
		return nil, nil, fmt.Errorf("forbidden: no access to namespace %q", namespace)
	} else if kind == "secrets" || kind == "secret" {
		// Per-kind RBAC inside the namespace — the chart can grant the SA
		// cluster-wide secrets (Helm release visibility), so namespace-list
		// discovery is not a sufficient gate. The list handler has the
		// matching list-SAR.
		if !canReadInNamespace(ctx, "", "secrets", namespace, "get") {
			return nil, nil, fmt.Errorf("forbidden: no access to secrets in namespace %q", namespace)
		}
	}

	// Try typed cache first. rawObj is the un-minified resource, threaded
	// into attachResourceExtras so ManagedBy synthesis can disambiguate by
	// group (avoids Knative Service vs core Service kind/plural collisions).
	var resourceData any
	var rawObj any
	obj, err := k8s.FetchResource(cache, kind, namespace, name)
	if err == k8s.ErrUnknownKind {
		// Fall through to dynamic cache for CRDs
		u, dynErr := cache.GetDynamicWithGroup(ctx, kind, namespace, name, group)
		if dynErr != nil {
			return nil, nil, fmt.Errorf("resource not found: %w", dynErr)
		}
		resourceData = aicontext.MinifyUnstructured(u, aicontext.LevelDetail)
		rawObj = u
	} else if err != nil {
		return nil, nil, fmt.Errorf("resource not found: %w", err)
	} else {
		k8s.SetTypeMeta(obj)
		minified, minErr := aicontext.Minify(obj, aicontext.LevelDetail)
		if minErr != nil {
			return nil, nil, fmt.Errorf("failed to minify: %w", minErr)
		}
		resourceData = minified
		rawObj = obj
	}

	includes := parseIncludes(input.Include)
	if len(includes) == 0 {
		return toJSONResult(resourceData)
	}

	// Build enriched response with requested extras
	result := map[string]any{"resource": resourceData}
	attachResourceExtras(ctx, cache, result, includes, kind, namespace, name, rawObj)
	return toJSONResult(result)
}

// attachResourceExtras populates optional extras (events, relationships, metrics, logs)
// on the result map based on the includes set. rawObj is the already-fetched
// resource (typed or *unstructured); passed through so relationship synthesis
// uses the authoritative object instead of a group-blind kind/name lookup.
func attachResourceExtras(ctx context.Context, cache *k8s.ResourceCache, result map[string]any, includes map[string]bool, kind, namespace, name string, rawObj any) {
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
			if rels := topology.GetRelationshipsWithObject(displayKind, namespace, name, rawObj, topo,
				k8s.NewTopologyResourceProvider(k8s.GetResourceCache()),
				k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery()), nil); rels != nil {
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

	var requested []string
	if input.Namespace != "" {
		requested = []string{input.Namespace}
	}
	allowed := filterNamespacesForUser(ctx, requested)
	if allowed != nil && len(allowed) == 0 {
		return toJSONResult([]mcpChange{})
	}

	queryOpts := timeline.QueryOptions{
		Since:        time.Now().Add(-since),
		FilterPreset: "default",
	}
	switch {
	case input.Namespace != "":
		queryOpts.Namespaces = []string{input.Namespace}
	case allowed != nil:
		queryOpts.Namespaces = allowed
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
	// Topology weaves cluster-scoped (Nodes, IngressClasses, NetworkPolicies)
	// with namespaced resources, so we gate on access to whatever the caller
	// requested. Namespace-restricted users must scope to a namespace they
	// can see; otherwise we refuse rather than build a partial graph.
	var requested []string
	if input.Namespace != "" {
		requested = []string{input.Namespace}
	}
	allowed := filterNamespacesForUser(ctx, requested)
	if allowed != nil && len(allowed) == 0 {
		if input.Namespace != "" {
			return nil, nil, fmt.Errorf("forbidden: no access to namespace %q", input.Namespace)
		}
		return nil, nil, fmt.Errorf("forbidden: topology requires either a namespace you can access or cluster-wide access")
	}

	opts := topology.DefaultBuildOptions()
	if input.Namespace != "" {
		opts.Namespaces = []string{input.Namespace}
	} else if allowed != nil {
		// Namespace-restricted user with no explicit pick — scope to their allowed set.
		opts.Namespaces = allowed
	}
	if input.View == "traffic" {
		opts.ViewMode = topology.ViewModeTraffic
	}

	builder := topology.NewBuilder(k8s.NewTopologyResourceProvider(k8s.GetResourceCache())).WithDynamic(k8s.NewTopologyDynamicProvider(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery()))
	topo, err := builder.Build(opts)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to build topology: %w", err)
	}

	// Topology pulls cluster-scoped resources (Nodes, Karpenter NodePool /
	// NodeClaim, GatewayClass, PV, StorageClass, …) regardless of the
	// namespace scope. Strip the kinds the user can't list so a namespace-
	// restricted user can't enumerate cluster infrastructure via the
	// topology tool. Cluster-wide pod access does NOT imply cluster-scoped
	// reads; per-kind SAR is the gate.
	if deny := deniedClusterScopedTopoKinds(ctx); len(deny) > 0 {
		topo.StripNodeKinds(deny)
	}

	if strings.ToLower(input.Format) == "summary" {
		return toJSONResult(buildTopologySummary(topo))
	}

	return toJSONResult(topo)
}

// deniedClusterScopedTopoKinds returns the set of cluster-scoped topology
// NodeKinds the calling user cannot list. Walks topology.ClusterScopedKinds
// (centralized table — see pkg/topology/cluster_scoped_kinds.go). Reuses
// canReadClusterScopedKind's per-user canI cache so subsequent topology
// calls within the same TTL don't re-SAR.
func deniedClusterScopedTopoKinds(ctx context.Context) map[topology.NodeKind]bool {
	deny := make(map[topology.NodeKind]bool)
	for _, ck := range topology.ClusterScopedKinds {
		if !canReadClusterScopedKind(ctx, ck.Resource, ck.Group, "list") {
			deny[ck.Kind] = true
		}
	}
	return deny
}

// topologySummary is an LLM-friendly text representation of the topology.
type topologySummary struct {
	Namespaces []nsSummary   `json:"namespaces"`
	Problems   []string      `json:"problems,omitempty"`
	Stats      topologyStats `json:"stats"`
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

	var requested []string
	if input.Namespace != "" {
		requested = []string{input.Namespace}
	}
	allowed := filterNamespacesForUser(ctx, requested)
	if allowed != nil && len(allowed) == 0 {
		return toJSONResult([]any{})
	}

	var events []*corev1.Event
	var err error
	switch {
	case input.Namespace != "":
		events, err = eventLister.Events(input.Namespace).List(labels.Everything())
	case allowed != nil:
		// Namespace-restricted user, no explicit pick: aggregate across allowed.
		for _, ns := range allowed {
			items, listErr := eventLister.Events(ns).List(labels.Everything())
			if listErr != nil {
				err = listErr
				break
			}
			events = append(events, items...)
		}
	default:
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
	if !checkNamespaceAccess(ctx, input.Namespace) {
		return nil, nil, fmt.Errorf("forbidden: no access to namespace %q", input.Namespace)
	}

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

	// Filter to the user's allowed namespaces. nil = no filter (auth off /
	// cluster-admin). Empty = no access (return []).
	allowed := filterNamespacesForUser(ctx, nil)
	if allowed != nil {
		set := make(map[string]bool, len(allowed))
		for _, ns := range allowed {
			set[ns] = true
		}
		filtered := namespaces[:0]
		for _, ns := range namespaces {
			if set[ns.Name] {
				filtered = append(filtered, ns)
			}
		}
		namespaces = filtered
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
	// Unavailable + UnavailableReason are populated when the Helm read
	// failed (Helm client not initialized, RBAC denied, network error,
	// etc.) — distinguishes "this cluster has zero Helm releases" from
	// "Helm is broken; results aren't an honest count." LLM consumers
	// should surface UnavailableReason to the user instead of confidently
	// reporting Total=0.
	Unavailable       bool   `json:"unavailable,omitempty"`
	UnavailableReason string `json:"unavailableReason,omitempty"`
}

type mcpHelmRelease struct {
	Name           string `json:"name"`
	Namespace      string `json:"namespace"`
	Chart          string `json:"chart"`
	ChartVersion   string `json:"chartVersion"`
	Status         string `json:"status"`
	ResourceHealth string `json:"resourceHealth,omitempty"`
}

func buildDashboard(ctx context.Context, cache *k8s.ResourceCache, namespace string, includeNodes bool, includeNamespaces bool) mcpDashboard {
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
		if p.Kind == "Node" && !includeNodes {
			continue
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
	if includeNodes {
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
	}

	// Simple resource counts for other types
	countResources(cache, namespace, &d, includeNamespaces)

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
	helmClient := helm.GetClient()
	if helmClient == nil {
		d.HelmReleases.Unavailable = true
		d.HelmReleases.UnavailableReason = "Helm client not initialized."
	} else {
		username, groups := userFromContext(ctx)
		releases, err := helmClient.ListReleasesAsUser(namespace, username, groups)
		if err != nil {
			// Not fatal for the dashboard — a viewer with no helm access
			// still sees everything else. Surface to LLM consumers via
			// Unavailable so they don't confidently report "Total=0
			// releases" when in fact the read failed.
			log.Printf("[mcp] Dashboard helm list failed: %v", err)
			d.HelmReleases.Unavailable = true
			if helm.IsForbiddenError(err) {
				d.HelmReleases.UnavailableReason = "RBAC denied: caller cannot list Helm release secrets in this scope."
			} else {
				d.HelmReleases.UnavailableReason = "Helm read failed: " + err.Error()
			}
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
	if includeNodes {
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
	}

	// Topology summary
	if includeNodes || namespace != "" {
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

func countResources(cache *k8s.ResourceCache, namespace string, d *mcpDashboard, includeNamespaces bool) {
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
	if includeNamespaces {
		if nsLister := cache.Namespaces(); nsLister != nil {
			items, _ := nsLister.List(labels.Everything())
			d.ResourceCounts["namespaces"] = len(items)
		}
	}
}

func handleIssuesTool(ctx context.Context, _ *mcp.CallToolRequest, input issuesInput) (*mcp.CallToolResult, any, error) {
	provider := issues.NewCacheProvider()
	if provider == nil {
		return nil, nil, fmt.Errorf("not connected to cluster")
	}
	var allowedNamespaces []string
	if input.Namespace != "" {
		if !checkNamespaceAccess(ctx, input.Namespace) {
			return toJSONResult(map[string]any{"issues": []issues.Issue{}, "total": 0, "total_matched": 0})
		}
		allowedNamespaces = []string{input.Namespace}
	} else {
		allowedNamespaces = filterNamespacesForUser(ctx, nil)
		if allowedNamespaces != nil && len(allowedNamespaces) == 0 {
			return toJSONResult(map[string]any{"issues": []issues.Issue{}, "total": 0, "total_matched": 0})
		}
	}
	severities, err := parseSeverityList(input.Severity)
	if err != nil {
		return nil, nil, err
	}
	sources, err := parseSourceList(input.Source)
	if err != nil {
		return nil, nil, err
	}
	filters := issues.Filters{
		Severities: severities,
		Sources:    sources,
		Kinds:      splitCSVStr(input.Kind),
		Limit:      input.Limit,
		Namespaces: allowedNamespaces,
		CanReadClusterScoped: func(kind, group string) bool {
			return canReadClusterScopedKind(ctx, kind, group, "list")
		},
	}
	if input.Filter != "" {
		f, err := filter.CachedIssueFilter(input.Filter)
		if err != nil {
			return nil, nil, fmt.Errorf("filter: %w", err)
		}
		filters.Filter = f
	}
	if input.Since != "" {
		d, err := time.ParseDuration(input.Since)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid since=%q: %w", input.Since, err)
		}
		if d < 0 {
			return nil, nil, fmt.Errorf("since must be non-negative, got %s", d)
		}
		filters.Since = d
	}
	// Audit + event sources are both opt-in (default off). The
	// MCP input doesn't surface separate include_* knobs, so the
	// source list IS the opt-in. Mirror the HTTP handler's
	// behavior — including the 1h since-default when events are
	// enabled with no explicit window, so an MCP caller doesn't
	// silently inherit the full event-cache backlog.
	for _, s := range filters.Sources {
		switch s {
		case issues.SourceAudit:
			filters.IncludeAudit = true
		case issues.SourceEvent:
			filters.IncludeEvents = true
		}
	}
	if filters.IncludeEvents && filters.Since == 0 {
		filters.Since = time.Hour
	}
	out, stats := issues.ComposeWithStats(provider, filters)
	resp := map[string]any{
		"issues": out,
		"total":  len(out),
		// total_matched is the uncapped count — tells the caller
		// whether the response is windowed or the whole set. Without
		// it, an MCP agent can't distinguish "200 returned" from
		// "200 of 1000". Mirrors the HTTP /api/issues response shape.
		"total_matched": stats.TotalMatched,
	}
	if stats.FilterErrors > 0 {
		resp["filter_errors"] = stats.FilterErrors
		resp["filter_error_sample"] = stats.FilterErrorSample
	}
	return toJSONResult(resp)
}

func parseSeverityList(v string) ([]issues.Severity, error) {
	if v == "" {
		return nil, nil
	}
	var out []issues.Severity
	for _, p := range strings.Split(v, ",") {
		switch strings.ToLower(strings.TrimSpace(p)) {
		case "":
			continue
		case "critical":
			out = append(out, issues.SeverityCritical)
		case "warning":
			out = append(out, issues.SeverityWarning)
		default:
			return nil, fmt.Errorf("unknown severity %q (want: critical, warning)", p)
		}
	}
	return out, nil
}

func parseSourceList(v string) ([]issues.Source, error) {
	if v == "" {
		return nil, nil
	}
	var out []issues.Source
	for _, p := range strings.Split(v, ",") {
		switch strings.ToLower(strings.TrimSpace(p)) {
		case "":
			continue
		case "problem":
			out = append(out, issues.SourceProblem)
		case "audit":
			out = append(out, issues.SourceAudit)
		case "event":
			out = append(out, issues.SourceEvent)
		case "condition":
			out = append(out, issues.SourceCondition)
		default:
			return nil, fmt.Errorf("unknown source %q (want: problem, audit, event, condition)", p)
		}
	}
	return out, nil
}

func splitCSVStr(v string) []string {
	if v == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func intersectAllowedNamespaces(allowed, requested []string) []string {
	if allowed == nil {
		return requested
	}
	if len(requested) == 0 {
		return allowed
	}
	set := make(map[string]struct{}, len(allowed))
	for _, ns := range allowed {
		set[ns] = struct{}{}
	}
	out := make([]string, 0, len(requested))
	for _, ns := range requested {
		if _, ok := set[ns]; ok {
			out = append(out, ns)
		}
	}
	return out
}

// mcpSensitiveSearchKinds is the MCP mirror of the REST sensitiveSearchKinds —
// cluster-scoped kinds that need their own list-SAR per user. Secrets are
// namespaced and handled by mcpSearchSecretsRBAC instead, which supports
// per-namespace permission.
var mcpSensitiveSearchKinds = []struct {
	Kind     string
	Resource string
	Group    string
}{
	{"Node", "nodes", ""},
	{"PersistentVolume", "persistentvolumes", ""},
	{"StorageClass", "storageclasses", "storage.k8s.io"},
	{"Namespace", "namespaces", ""},
}

func mcpSearchSkipKinds(ctx context.Context) map[string]bool {
	user, perms := resolveUserPerms(ctx)
	if user == nil {
		return nil
	}
	client := k8s.GetClient()
	if client == nil {
		out := make(map[string]bool, len(mcpSensitiveSearchKinds))
		for _, k := range mcpSensitiveSearchKinds {
			out[k.Kind] = true
		}
		return out
	}
	out := make(map[string]bool, len(mcpSensitiveSearchKinds))
	for _, k := range mcpSensitiveSearchKinds {
		if perms != nil {
			if allowed, ok := perms.CanI("list", k.Group, k.Resource, ""); ok {
				if !allowed {
					out[k.Kind] = true
				}
				continue
			}
		}
		allowed, err := subjectCanI(ctx, client, user.Username, user.Groups, "", k.Group, k.Resource, "list")
		if err != nil {
			log.Printf("[mcp] search SAR failed for %s on %s/%s: %v", user.Username, k.Group, k.Resource, err)
			out[k.Kind] = true
			continue
		}
		if perms != nil {
			perms.SetCanI("list", k.Group, k.Resource, "", allowed)
		}
		if !allowed {
			out[k.Kind] = true
		}
	}
	return out
}

// mcpSearchSecretsRBAC mirrors Server.computeSearchSecretsRBAC for MCP. See
// that function for the three-case semantics. scanNamespaces is the
// effective scan scope (intersection of the user's RBAC-allowed namespaces
// and any `ns:` modifier in the query) — nil means a true cluster-wide scan.
//
// Cached canI hits short-circuit before the live-client path, so a seeded
// test cache authorizes without needing a real K8s client.
func mcpSearchSecretsRBAC(ctx context.Context, scanNamespaces []string) (decision string, scopedNamespaces []string) {
	if user, _ := resolveUserPerms(ctx); user == nil {
		return "", nil
	}

	if scanNamespaces == nil {
		if canReadInNamespace(ctx, "", "secrets", "", "list") {
			return "", nil
		}
		return "skip", nil
	}
	if len(scanNamespaces) == 0 {
		return "skip", nil
	}
	scoped := make([]string, 0, len(scanNamespaces))
	for _, ns := range scanNamespaces {
		if canReadInNamespace(ctx, "", "secrets", ns, "list") {
			scoped = append(scoped, ns)
		}
	}
	if len(scoped) == 0 {
		return "skip", nil
	}
	return "override", scoped
}

func handleSearch(ctx context.Context, req *mcp.CallToolRequest, input searchInput) (*mcp.CallToolResult, any, error) {
	provider := search.NewCacheProvider()
	if provider == nil {
		return nil, nil, fmt.Errorf("not connected to cluster")
	}
	parsed := search.Parse(input.Q)
	allowed := filterNamespacesForUser(ctx, nil)
	if allowed != nil && len(allowed) == 0 {
		return toJSONResult(search.Result{Hits: []search.Hit{}})
	}
	scanNamespaces := intersectAllowedNamespaces(allowed, parsed.NSFilter)
	if allowed != nil && len(scanNamespaces) == 0 {
		return toJSONResult(search.Result{Hits: []search.Hit{}})
	}
	parsed.NSFilter = scanNamespaces
	var include search.IncludeMode
	switch input.Include {
	case "", "summary":
		include = search.IncludeSummary
	case "raw":
		include = search.IncludeRaw
	case "none":
		include = search.IncludeNone
	default:
		return nil, nil, fmt.Errorf("unknown include=%q (want: summary, raw, none)", input.Include)
	}

	skipKinds := mcpSearchSkipKinds(ctx)
	// Secrets: per-namespace SAR fanout. See REST handleSearch for rationale.
	// scanNamespaces (not allowed) is the SAR-fanout input — a user with
	// AllowedNamespaces==nil (cluster-wide-pods sentinel) who constrains
	// with `ns:team-a` should fanout over team-a, not run a cluster-scope
	// `list secrets` SAR they may not have.
	var namespacesByKind map[string][]string
	switch decision, scoped := mcpSearchSecretsRBAC(ctx, scanNamespaces); decision {
	case "skip":
		if skipKinds == nil {
			skipKinds = make(map[string]bool)
		}
		skipKinds["Secret"] = true
	case "override":
		// scoped ⊆ scanNamespaces ⊆ parsed.NSFilter already (the SAR fanout
		// iterates scanNamespaces, which is the upstream intersection of
		// allowed and parsed.NSFilter). Use the SAR result directly.
		namespacesByKind = map[string][]string{"Secret": scoped}
	}

	opts := search.Options{
		Limit:            input.Limit,
		Include:          include,
		Namespaces:       scanNamespaces,
		SkipKinds:        skipKinds,
		NamespacesByKind: namespacesByKind,
		CanReadClusterScoped: func(kind, group, resource string) bool {
			return canReadClusterScopedKind(ctx, kind, group, "list")
		},
	}
	if input.Filter != "" {
		f, err := filter.CachedObjectFilter(input.Filter)
		if err != nil {
			return nil, nil, fmt.Errorf("filter: %w", err)
		}
		opts.Filter = f
	}
	result, err := search.Search(ctx, provider, parsed, opts)
	if err != nil {
		return nil, nil, err
	}
	return toJSONResult(result)
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
