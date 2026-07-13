package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/internal/filter"
	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/meaningfulchanges"
	"github.com/skyhook-io/radar/internal/resourcecontextrefs"
	"github.com/skyhook-io/radar/internal/search"
	"github.com/skyhook-io/radar/internal/summarycontext"
	"github.com/skyhook-io/radar/internal/timeline"
	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
	"github.com/skyhook-io/radar/pkg/health"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	topology "github.com/skyhook-io/radar/pkg/topology"
)

// registerTools registers every MCP tool exposed at /mcp. The user-facing
// setup dialog catalog (web/src/components/home/mcpToolCatalog.ts) must list
// the same set — TestSetupDialogCoversAllTools fails CI when they diverge, so
// add/remove the catalog entry alongside any change here.
func registerTools(server *mcp.Server, includeWrites bool) {
	boolPtr := func(b bool) *bool { return &b }
	// All radar tools operate against the connected cluster (closed world),
	// not the open internet — set OpenWorldHint=false so MCP clients that
	// gate on this hint don't treat radar like a web-search tool.
	readOnly := &mcp.ToolAnnotations{
		ReadOnlyHint:  true,
		OpenWorldHint: boolPtr(false),
	}
	// writeTool reflects worst-case action across the tools that share it:
	// apply_resource can force SSA ownership; manage_node drains evict pods; manage_workload
	// rollback/restart overwrites desired state or terminates pods;
	// manage_gitops terminate/rollback aborts or overwrites; manage_cronjob
	// suspend mutates schedule state (not additive).
	writeTool := &mcp.ToolAnnotations{
		DestructiveHint: boolPtr(true),
		OpenWorldHint:   boolPtr(false),
	}

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_dashboard",
		Description: "Use for inventory-style cluster or namespace health triage, like " +
			"`kubectl get all` plus detected problems and warning events in one call. " +
			"Returns resource counts, failing pods, unhealthy workloads, recent Warning " +
			"events, and Helm release status so you can rank likely suspects before " +
			"calling get_resource or logs. Routing: unknown broken thing -> issues; " +
			"content/name search -> search; service routing/dependencies -> get_topology " +
			"or get_neighborhood; inventory/counts/Helm/events overview -> get_dashboard. " +
			"Use namespace for app-local triage; omit it when the root may be cluster-scoped.",
		Annotations: readOnly,
	}, logToolCall("get_dashboard", handleGetDashboard))

	mcp.AddTool(server, &mcp.Tool{
		Name: "top_resources",
		Description: "Use when investigating high CPU, memory pressure, OOMKills, " +
			"slow services, noisy pods, or uneven node load. Returns live metrics " +
			"ranked like `kubectl top pods|nodes | sort`, joined with Kubernetes " +
			"context: pod status, readiness, restarts, owner workload, requests, and " +
			"limits. kind=pods ranks individual Pods, kind=workloads aggregates Pods " +
			"to Deployments/StatefulSets/DaemonSets/Jobs, and kind=nodes ranks Nodes. " +
			"Use before reading logs when the symptom mentions CPU, memory, GC, OOM, " +
			"latency, or load.",
		Annotations: readOnly,
	}, logToolCall("top_resources", handleTopResources))

	mcp.AddTool(server, &mcp.Tool{
		Name: "list_resources",
		Description: "Use for a jq-like namespace sweep when you know the resource kind " +
			"(pods/po, deployments/deploy, services/svc, configmaps/cm, CRDs). Returns compact Kubernetes-shaped " +
			"rows plus summaryContext by default (managedBy, health, issueCount) so you can " +
			"compare many similar resources and pick suspects before calling get_resource. " +
			"For unknown kind/name searches, use search. For broad health triage, use " +
			"get_dashboard or issues first.",
		Annotations: readOnly,
	}, logToolCall("list_resources", handleListResources))

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_resource",
		Description: "Use AFTER narrowing to one resource. Returns the resource's " +
			"Kubernetes-shaped spec/status/metadata plus resourceContext when available " +
			"(relationships, refs, issue/audit/policy rollups — issues carry " +
			"diagnostic_context with cross-subject causal links + a confidence tier). This is the drill-down " +
			"tool, not the best first call for broad incidents. Start with issues, " +
			"get_dashboard, search, or list_resources to rank candidates; then call " +
			"get_resource for the exact object. If you are looking for a string across " +
			"ConfigMaps, CRD specs, env refs, or object content, use search instead of " +
			"fetching resources one by one. Use the group parameter for ambiguous " +
			"kinds such as Knative Service vs core Service.",
		Annotations: readOnly,
	}, logToolCall("get_resource", handleGetResource))

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_topology",
		Description: "Use to map a multi-service incident or dependency graph, preferably " +
			"scoped to a namespace. " +
			"Returns Kubernetes resource nodes and edges (Services, workloads, Pods, " +
			"Ingresses, ConfigMaps, Secrets, owners) so you can see service-to-workload " +
			"routing and ownership relationships instead of inspecting resources one by one. " +
			"Use view=traffic for routing/connectivity questions and view=resources for " +
			"ownership/deployment hierarchy. NOTE: view=traffic is SPEC-DERIVED routing " +
			"(Ingress→Service→Pod declarations) — it does not show observed traffic, " +
			"request rates, or live connections. Always specify namespace unless you " +
			"specifically need a cross-namespace graph. If you already know the suspicious " +
			"root, use get_neighborhood for a smaller focused graph.",
		Annotations: readOnly,
	}, logToolCall("get_topology", handleGetTopology))

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_neighborhood",
		Description: "Use when investigating cross-resource failures around a known " +
			"resource: service routing, targetPort/selector/endpoints problems, dependency " +
			"timeouts, config/secret refs, owner chains, or traffic not reaching pods. " +
			"Returns the BFS-expanded topology neighborhood around one root, which is " +
			"usually cheaper and clearer than get_topology once you have a suspect. " +
			"Typical flow: issues/search/list_resources identify a Service or workload, " +
			"then get_neighborhood traces its upstream/downstream Services, workloads, " +
			"Pods, refs, and owners. Profile auto (default) picks a bounded edge set " +
			"from the root kind; profile all expands every edge type and is heavier, " +
			"use it only when auto produced a too-narrow neighborhood. Hops defaults to " +
			"1 and maxes at 2. Nodes are RBAC-filtered; denied neighbors appear only as " +
			"aggregate omitted counts.",
		Annotations: readOnly,
	}, logToolCall("get_neighborhood", handleGetNeighborhood))

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_events",
		Description: "Use for recent Kubernetes Warning events after an overview points " +
			"at a namespace or resource, or when the symptom is scheduling, pulling images, " +
			"restarts, failed mounts, readiness, or controller errors. Events are deduplicated " +
			"and sorted by recency with reason, message, and count. For a ranked issue list " +
			"that includes problems/conditions, use issues first.",
		Annotations: readOnly,
	}, logToolCall("get_events", handleGetEvents))

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_pod_logs",
		Description: "Use only after narrowing to a specific Pod/container. Returns " +
			"diagnostically relevant log lines (errors, panics, stack traces, warnings) " +
			"or falls back to recent tail lines. Set grep to server-side filter like " +
			"`kubectl logs | grep PATTERN` when you know an error string, request path, " +
			"service name, or trace id. For broad incidents, first use issues, " +
			"get_dashboard, search, list_resources, or get_neighborhood to avoid reading " +
			"logs from many unrelated pods. If the target is a config value, feature flag, " +
			"CRD field, env ref, or YAML/spec content, use search rather than logs.",
		Annotations: readOnly,
	}, logToolCall("get_pod_logs", handleGetPodLogs))

	mcp.AddTool(server, &mcp.Tool{
		Name: "diagnose",
		Description: "Use when the agent's decision is 'this workload or GitOps reconciler " +
			"is broken — find the root cause / localize the failure'. For a single " +
			"Pod/Deployment/StatefulSet/DaemonSet, bundles: the resource (Kubernetes-shaped detail) + diagnostic " +
			"resourceContext (managedBy, exposes, selectedBy, uses, runsOn, " +
			"issue/audit/policy rollups — issues carry diagnostic_context with cross-subject " +
			"causal links + a confidence tier to walk symptom→root) + current AND previous container logs across the " +
			"workload's pods + recent Warning events filtered to this resource + a " +
			"recentChanges section for the workload and directly referenced " +
			"ConfigMaps (no Secret content) + a " +
			"startupBlockers section when the workload can't reach Running (unschedulable " +
			"with the offending node constraint named, admission/quota rejection, or a " +
			"post-bind CNI/volume stall). For Application/Kustomization/Flux HelmRelease, returns " +
			"the reconciler resource + GitOps status summary + related parsed issues " +
			"(cause/action/remediation), without pod-log fan-out. Use for " +
			"CrashLoopBackOff, OOMKills, failed deploys, image-pull errors, readiness " +
			"flaps, scheduling failures, error-spewing services, GitOps sync/health failures, or any workload " +
			"root-causing where you would otherwise call get_resource → events → " +
			"get_pod_logs → get_pod_logs(previous=true) in sequence — this returns the " +
			"same data in one round-trip. If you only need ONE facet (e.g. just spec, " +
			"just logs), prefer the targeted tool. For other CRDs or non-workload kinds, " +
			"use get_resource (with optional include=events).",
		Annotations: readOnly,
	}, logToolCall("diagnose", handleDiagnose))

	mcp.AddTool(server, &mcp.Tool{
		Name: "list_namespaces",
		Description: "List all Kubernetes namespaces with their status. " +
			"Use to discover available namespaces before filtering other queries.",
		Annotations: readOnly,
	}, logToolCall("list_namespaces", handleListNamespaces))

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_changes",
		Description: "Use when the symptom is 'this worked earlier' or 'something broke " +
			"after a deploy/config change.' Returns recent meaningful changes ranked with " +
			"spec/config changes first, including field-level diffs for Deployment env/probes " +
			"and structured ConfigMap data when available. This is often faster than reading " +
			"ReplicaSet histories or individual audit/log streams, especially when issues " +
			"are empty or dominated by baseline failures. Pair with since to bound the window; " +
			"filter by namespace, kind, or name when you know the scope. Omit namespace when " +
			"the relevant change may be outside the app namespace. Also includes native Helm " +
			"release deployment/operation history as `source: helm` changes, so failed upgrades, " +
			"rollbacks, and successful current revisions appear in the same recent-change feed. " +
			"If `sourcesErrored` includes helm, treat the result as partial rather than proof " +
			"that no Helm deployment happened. Use get_helm_release include=history,operations " +
			"for the full Helm revision trail.",
		Annotations: readOnly,
	}, logToolCall("get_changes", handleGetChanges))

	// --- Audit tool (read-only) ---

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_cluster_audit",
		Description: "Use when the agent's decision is 'is this cluster well-configured / " +
			"compliant?' — STATIC CONFIG POSTURE, not live operational state. Returns " +
			"best-practice findings: Security (runAsRoot, privileged containers, dangerous " +
			"capabilities, hostPath/hostNetwork, secret-in-ConfigMap), Reliability (single " +
			"replicas, missing PDB, missing TopologySpread, podHARisk, Service/Ingress " +
			"without matching backends, stuckTerminating, deprecatedAPIVersion), and " +
			"Efficiency (missing resource requests/limits, orphaned ConfigMaps/Secrets, " +
			"under/over-utilization). Each finding has remediation guidance. " +
			"INDEPENDENT of operational health: a healthy pod can have many audit findings " +
			"(badly configured but working), a crashing pod can have zero (cleanly " +
			"configured but failing). For 'what's broken right now?' use the issues tool. " +
			"Respects user's audit settings (ignored namespaces, disabled checks). Filter " +
			"by namespace, category, or severity. Resources absent from findings should " +
			"NOT be reported as non-compliant — empty findings for a scope means no " +
			"violations, not a failed check.",
		Annotations: readOnly,
	}, logToolCall("get_cluster_audit", handleGetAudit))

	// --- Helm tools (read-only) ---

	mcp.AddTool(server, &mcp.Tool{
		Name: "list_helm_releases",
		Description: "List all Helm releases in the cluster with their status, resource health, " +
			"storage namespace, Flux ownership when detected, lastOperation when Helm history " +
			"indicates a current failed upgrade, rollback-after-failure, rollback, or stuck pending " +
			"operation, and a capped operations trail for failed upgrades, rollbacks, and stuck operations. " +
			"Use this first for Helm deployment debugging; if storageNamespace is present, pass that " +
			"value as namespace to get_helm_release. Follow with get_helm_release include=history,operations " +
			"for the full revision trail.",
		Annotations: readOnly,
	}, logToolCall("list_helm_releases", handleListHelmReleases))

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_helm_release",
		Description: "Get detailed information about a specific Helm release including owned resources " +
			"and their status. The namespace parameter is the Helm storage namespace: use storageNamespace " +
			"from list_helm_releases when present, otherwise use namespace. The default response includes " +
			"storageNamespace, managedByFluxHelmRelease, resource health, operationInsight, and lastOperation when Helm history " +
			"indicates a current failed upgrade, rollback-after-failure, rollback, or stuck pending operation, " +
			"where operationInsight gives active-vs-recovered state, the likely resource to inspect, and suggested revision comparison when available, " +
			"plus hooks and failed/running hookDiagnostics with live Job/Pod/Event/redacted-log evidence when available. " +
			"Optionally include values, revision history, operation history, manifest diff, values diff, " +
			"notes diff, or rendered-resource set diff between revisions using the 'include' parameter. " +
			"Values returned through MCP are key-aware redacted. " +
			"(comma-separated: values, history, operations, diff, values_diff, notes_diff, resource_diff). " +
			"diff_revision_1 and diff_revision_2 are used when include contains any *_diff or diff token.",
		Annotations: readOnly,
	}, logToolCall("get_helm_release", handleGetHelmRelease))

	// --- Packages tool (read-only) ---
	//
	// Higher-level than list_helm_releases: collapses Helm releases,
	// workload labels, CRD registrations, and GitOps declarations
	// (Argo Applications + Flux HelmReleases/Kustomizations) into a
	// unified "what's installed in this cluster" view with source
	// provenance. Each row's `sources` field shows which detection
	// channels voted "this is installed" — H, L, C, A, F — and the
	// MCP response includes sourceLegend so those terse stable codes
	// are interpretable without external docs.
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_packages",
		Description: "List installed packages (Helm releases, label-managed workloads, CRDs, " +
			"Argo Applications, Flux HelmReleases + Kustomizations) with their sources, " +
			"versions, and health. Each row carries a `sources` array (H=Helm API, " +
			"L=workload labels, C=CRD registrations, A=Argo declaration, F=Flux declaration) " +
			"so the caller can see WHY this package is detected; the MCP response also includes " +
			"`sourceLegend` mapping those stable codes to readable meanings, plus a `contributors` " +
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
		Description: "Use when the agent's decision is 'what's broken right now?' — LIVE " +
			"OPERATIONAL STATE, not config posture. Returns a ranked list of currently " +
			"failing resources: failing Deployments/StatefulSets/CronJobs/HPAs/Nodes/Jobs/" +
			"PVCs, active native Helm release failures or stuck pending operations, " +
			"dangling-reference errors like Pod→missing PVC/CM/Secret/SA, HPA→missing " +
			"scaleTargetRef, Ingress→missing backend Service, RoleBinding→missing Role, " +
			"webhook→missing Service, pod startup blockers — why a Pod can't reach Running: " +
			"unschedulable (arch/taint/resources/affinity), admission-rejected " +
			"(quota/PodSecurity/webhook), or stuck post-bind (CNI/volume), and False " +
			".status.conditions on CRDs from Argo/Flux/Knative/Crossplane/cert-manager/KEDA. " +
			"Severity normalized to critical/warning. This is one curated stream — there is " +
			"no source filter; each row carries a `source` label (problem|missing_ref|" +
			"scheduling|condition) you can slice on via the CEL filter= if needed. Some " +
			"rows include `diagnostic_context`: deterministic facts AND cross-subject " +
			"causal links — a failing root (Node under resource pressure, broken PVC, " +
			"Service with no endpoints, unavailable metrics APIService) annotated with the " +
			"downstream issues it may explain, in `facts[].related_issues` (each `count` " +
			"is how many affected resources fold into that linked issue). Each causal link " +
			"carries a `confidence`: `high` = a declared structural edge (selector, owner, " +
			"claimName) — treat as fact; `medium` = inferred/co-located (e.g. pods ON a " +
			"pressured node) — a lead to verify, NOT proof; `low` = heuristic. The fact's " +
			"`role` (candidate = possible cause, affected, rollup, context) places the row " +
			"in the causal picture. The REVERSE is also provided: a symptom row may carry " +
			"`incident_parent` (the root issue that explains it — id + ref + confidence + " +
			"fact_type), so you can walk symptom→root directly without scanning every root's " +
			"facts. It is set only when a single root is unambiguous (high beats medium; " +
			"distinct same-confidence roots leave it unset). Use these links to walk from a " +
			"symptom to its likely " +
			"root, but confirm a medium link before acting on it. " +
			"When `recent_changes` is present, consider it if the issue list does not " +
			"explain the reported symptom; `recent_changes_reason` says why Radar " +
			"attached it. It lists recent spec/config changes that may explain failures " +
			"not yet visible as runtime issues, or help distinguish creation-time " +
			"baseline failures from the active incident. " +
			"Single-namespace responses additionally correlate changes PER critical issue: " +
			"`correlated_changes` lists recent non-status changes on that issue's subject " +
			"(and its referenced ConfigMaps); `no_recent_changes.window_seconds` states the " +
			"subject had NO tracked changes in that window — strong evidence the issue is " +
			"chronic rather than change-driven. An issue with neither marker was not " +
			"checked (see `correlation_truncated`) — never read absence as 'no changes'. " +
			"ConfigMap change entries carry `consumed_by` (workloads that mount/reference " +
			"them directly). " +
			"For raw Kubernetes Warning events use get_events; for static best-practice / " +
			"security-posture findings (runAsRoot, missing PDB, no probes, missing resource " +
			"limits) use get_cluster_audit — a separate axis that must never be conflated (a " +
			"healthy pod can have many audit findings; a crashing pod can have zero). Kyverno " +
			"PolicyReport violations are not in either — they surface per-resource via " +
			"get_resource's resourceContext policy rollup. " +
			"Recovered Helm rollbacks are deploy history, not live issues; use get_changes for " +
			"Kubernetes timeline changes plus Helm deployment history, and get_helm_release for " +
			"full Helm revision/history/hook diagnostics. " +
			"After identifying a suspect issue, call diagnose when the affected resource " +
			"is a workload (Pod/Deployment/StatefulSet/DaemonSet) or GitOps reconciler " +
			"(Application/Kustomization/HelmRelease with group=helm.toolkit.fluxcd.io). " +
			"For native Helm issues (kind=HelmRelease, group=helm.sh), call get_helm_release " +
			"with include=history,operations. For other non-workload kinds, call get_resource. " +
			"Use get_neighborhood when the failure likely crosses " +
			"Services/workloads/Pods/dependencies. Use namespace for app-local triage; " +
			"omit it when the root may be cluster-scoped or outside the app namespace.",
		Annotations: readOnly,
	}, logToolCall("issues", handleIssuesTool))

	// --- Search (read-only) ---

	mcp.AddTool(server, &mcp.Tool{
		Name: "search",
		Description: "Find resources by content/term match when you do not know which object " +
			"contains a string, config key, env ref, image, label/annotation value, " +
			"ConfigMap data, CRD field, or status message. Tokens are AND'd. " +
			"Secret content is intentionally NOT indexed — Secret names match by " +
			"metadata, but data values won't appear in snippets to avoid leaking " +
			"secret material through search results. " +
			"Examples: `readinessProbe user-service`, `image:flagd`, `kind:Pod label:app=cart error`. " +
			"Modifiers such as kind:Pod, ns:foo, label:app=bar, and image:redis narrow a " +
			"term match; modifier-only queries are enumeration, so use list_resources when " +
			"you already know the kind/namespace. Returns ranked hits with snippets and " +
			"summaryContext. Use CEL filter for structural predicates. Searches typed kinds " +
			"plus warmed CRDs; cold CRDs need list_resources first.",
		Annotations: readOnly,
	}, logToolCall("search", handleSearch))

	// --- RBAC reverse-lookup (read-only) ---

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_subject_permissions",
		Description: "Get the effective RBAC permissions of a Kubernetes subject " +
			"(ServiceAccount, User, or Group) — what can this principal do across " +
			"the cluster. Returns: the bindings that grant access (each pointing at " +
			"its Role/ClusterRole), a deduplicated flat rule list, and (for " +
			"ServiceAccounts) the Pods running as this SA. " +
			"Use this to answer 'is this SA over-privileged?', 'why can X do Y?', " +
			"or 'what's the blast radius if this Pod is compromised?'. " +
			"For ServiceAccount, namespace is required. For User/Group, omit namespace " +
			"(those are external identities, not namespaced resources). " +
			"Inherited grants from implicit group memberships (system:authenticated, " +
			"system:serviceaccounts) are included for ServiceAccount subjects with the " +
			"`inheritedFromGroup` field set per binding so you can distinguish direct " +
			"from inherited grants.",
		Annotations: readOnly,
	}, logToolCall("get_subject_permissions", handleGetSubjectPermissions))

	// --- Prometheus tools (read-only) ---

	mcp.AddTool(server, &mcp.Tool{
		Name: "query_prometheus",
		Description: "Use when the question needs metric VALUES or history: CPU/memory over time, " +
			"request rates, error ratios, saturation, restarts trend, 'was there a spike?'. Executes " +
			"PromQL against the cluster's Prometheus (auto-discovered or configured in Radar; also " +
			"works with PromQL-compatible backends: Thanos, VictoriaMetrics, Mimir). " +
			"type=instant returns current values; type=range returns time series for a window " +
			"(since=1h default). For live top-N snapshots prefer top_resources; for metric/label " +
			"NAME discovery use discover_metrics first — do not guess metric names. " +
			"High-cardinality queries must be wrapped in topk(5, ...): oversized results return a " +
			"summary with a suggested rewrite instead of data.",
		Annotations: readOnly,
	}, logToolCall("query_prometheus", handleQueryPrometheus))

	mcp.AddTool(server, &mcp.Tool{
		Name: "discover_metrics",
		Description: "Use BEFORE query_prometheus when unsure of exact metric or label names. " +
			"Lists metric names matching a selector (match={__name__=~\"node_cpu.*\"}) with their " +
			"type (counter vs gauge — counters need rate()) and help text, or values of one label " +
			"(label=namespace). Returns up to limit values from the last hour of active series; " +
			"truncated=true means narrow the match. Never answer about metric values from this " +
			"tool — names prove existence, not magnitude.",
		Annotations: readOnly,
	}, logToolCall("discover_metrics", handleDiscoverMetrics))

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_prometheus_rules",
		Description: "List Prometheus alerting and recording rules with their PromQL definitions, " +
			"state (firing/pending/inactive), labels, annotations, and active alert instances. " +
			"When investigating a firing alert, ALWAYS fetch its rule definition here FIRST, then " +
			"run its query with query_prometheus to see the actual values. state=firing shows " +
			"everything currently alerting. Filters are substring matches; results cap at limit " +
			"(default 50) with truncated=true — narrow rather than paging.",
		Annotations: readOnly,
	}, logToolCall("get_prometheus_rules", handleGetPrometheusRules))

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_workload_logs",
		Description: "Get aggregated logs from all pods of a workload (Deployment, StatefulSet, " +
			"DaemonSet, Job, or Argo Workflow). Logs are collected from all matching pods concurrently, then " +
			"server-side filtered to errors, warnings, panics, and stack traces using " +
			"deterministic regex patterns and deduplicated. Set grep for additional " +
			"server-side filtering before that summary stage, like `kubectl logs | grep PATTERN`. " +
			"More useful than get_pod_logs when you need logs across all replicas of a workload. " +
			"If the target is a config value, feature flag, CRD field, env ref, or YAML/spec " +
			"content, use search rather than logs.",
		Annotations: readOnly,
	}, logToolCall("get_workload_logs", handleGetWorkloadLogs))

	// --- Write tools (workload, cronjob, gitops, apply, patch, node) ---
	// Registered only on the full server. The read-only mount omits them so a
	// read-only investigation can't even discover a mutating tool — server-side
	// enforcement that doesn't depend on the agent CLI's own tool allowlisting.
	if !includeWrites {
		return
	}

	mcp.AddTool(server, &mcp.Tool{
		Name: "manage_workload",
		Description: "Perform operations on a Kubernetes workload (Deployment, StatefulSet, or DaemonSet). " +
			"Supported actions: 'restart' triggers a rolling restart, 'scale' changes the replica count " +
			"(requires 'replicas' parameter), 'rollback' reverts to a previous revision " +
			"(requires 'revision' parameter). Use list_resources or get_dashboard first to identify the target.",
		Annotations: writeTool,
	}, logToolCall("manage_workload", handleManageWorkload))

	mcp.AddTool(server, &mcp.Tool{
		Name: "manage_cronjob",
		Description: "Perform operations on a Kubernetes CronJob. Supported actions: " +
			"'trigger' creates a manual Job run from the CronJob's template, " +
			"'suspend' pauses the CronJob schedule (no new Jobs will be created), " +
			"'resume' re-enables a suspended CronJob's schedule.",
		Annotations: writeTool,
	}, logToolCall("manage_cronjob", handleManageCronJob))

	mcp.AddTool(server, &mcp.Tool{
		Name: "manage_gitops",
		Description: "Perform operations on GitOps resources (ArgoCD or FluxCD). " +
			"For ArgoCD: actions are 'sync' (trigger deployment), 'refresh', 'terminate', 'rollback', " +
			"'suspend' (disable auto-sync), 'resume' (re-enable auto-sync). Resource kind is always Application. " +
			"For FluxCD: actions are 'reconcile' (trigger sync), 'sync-with-source', 'suspend', 'resume'. " +
			"Requires 'kind' parameter (kustomization, helmrelease, gitrepository, etc.).",
		Annotations: writeTool,
	}, logToolCall("manage_gitops", handleManageGitOps))

	mcp.AddTool(server, &mcp.Tool{
		Name: "apply_resource",
		Description: "Create or update a Kubernetes resource from a YAML manifest. " +
			"In 'apply' mode (default), performs a server-side apply with FieldManager=radar " +
			"and reports field ownership conflicts instead of taking ownership by default. " +
			"Set force=true only when you intend to take field ownership from other managers " +
			"(Helm, Flux, GitOps controllers, kubectl). " +
			"In 'create' mode, performs a strict create that fails if the resource already exists. " +
			"Supports multi-document YAML separated by '---'. " +
			"Use dry_run to validate without persisting changes and preview the server-side result. " +
			"Multi-document failures return per-document status because earlier documents may already be applied. " +
			"By default returns compact " +
			"post-mutation state, submitted-vs-live spec differences, rollout/pod status " +
			"for workloads, and current related issues; " +
			"set verify=false only when you need a terse write result.",
		Annotations: writeTool,
	}, logToolCall("apply_resource", handleApplyResource))

	mcp.AddTool(server, &mcp.Tool{
		Name: "patch_resource",
		Description: "Patch one existing Kubernetes resource with JSON Patch, JSON Merge Patch, or strategic merge patch. " +
			"Use this for precise field/list mutations such as removing a bad dnsConfig, hostPort, " +
			"initContainers field, sidecar container, nodeSelector, or replacing one scalar value. " +
			"Prefer this over apply_resource when you know the exact field to mutate and do not want " +
			"to rewrite the full manifest or take broad server-side-apply ownership. " +
			"For patch_type=json, patch must be an RFC 6902 JSON Patch array. For patch_type=merge, " +
			"patch must be a JSON object. For patch_type=strategic, use a JSON object against built-in " +
			"Kubernetes kinds when you need name-keyed list merging, such as editing one container. " +
			"By default returns compact post-patch state and " +
			"dry-run preview diffs; JSON Patch calls also include per-operation field checks. Set " +
			"verify=false only when you need a terse write result.",
		Annotations: writeTool,
	}, logToolCall("patch_resource", handlePatchResource))

	mcp.AddTool(server, &mcp.Tool{
		Name: "manage_node",
		Description: "Perform operations on a Kubernetes node. " +
			"Supported actions: 'cordon' marks the node as unschedulable (no new pods will be scheduled), " +
			"'uncordon' marks the node as schedulable again, " +
			"'drain' cordons the node and evicts all non-DaemonSet pods. " +
			"Drain options: 'delete_empty_dir_data' (allow evicting pods with emptyDir volumes), " +
			"'force' (evict pods not managed by a controller), 'timeout' (seconds, default 60).",
		Annotations: writeTool,
	}, logToolCall("manage_node", handleManageNode))
}

// Tool input types

type dashboardInput struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"filter to a specific namespace. Use when triaging one app/tenant namespace before drilling into individual resources."`
}

type topResourcesInput struct {
	Kind      string `json:"kind,omitempty" jsonschema:"what to rank: pods (default), workloads, or nodes"`
	Namespace string `json:"namespace,omitempty" jsonschema:"filter pods/workloads to a namespace. Required for namespace-restricted users unless they have cluster-wide namespace access."`
	Sort      string `json:"sort,omitempty" jsonschema:"sort by cpu (default) or memory"`
	Limit     int    `json:"limit,omitempty" jsonschema:"max rows returned, default 20, max 100"`
}

type listResourcesInput struct {
	Kind      string `json:"kind" jsonschema:"resource kind to list for a broad sweep, e.g. pods/po, deployments/deploy, services/svc, configmaps/cm. Prefer this before get_resource when comparing many same-kind objects."`
	Group     string `json:"group,omitempty" jsonschema:"API group when the kind is ambiguous (e.g. serving.knative.dev for Knative Service vs core Service)"`
	Namespace string `json:"namespace,omitempty" jsonschema:"filter to a specific namespace for app-scoped triage"`
	Context   string `json:"context,omitempty" jsonschema:"per-row context: default attaches summaryContext (managedBy + health + issueCount) for suspect ranking; 'none' returns bare rows"`
}

type getResourceInput struct {
	Kind      string `json:"kind" jsonschema:"resource kind, e.g. pod, deployment, service"`
	Group     string `json:"group,omitempty" jsonschema:"API group when the kind is ambiguous (e.g. cluster.x-k8s.io for CAPI Cluster vs CNPG Cluster)"`
	Namespace string `json:"namespace,omitempty" jsonschema:"namespace for namespaced kinds. Leave empty for cluster-scoped kinds (Node, ClusterRole, ClusterRoleBinding, IngressClass, PriorityClass, StorageClass, etc.)."`
	Name      string `json:"name" jsonschema:"resource name"`
	Include   string `json:"include,omitempty" jsonschema:"optional supplemental data after narrowing to this object: events, metrics, changes. include=changes follows the existing comma-separated include pattern. Separate from context. For logs use get_pod_logs / get_workload_logs (container, previous, since, grep) or diagnose for the full workload bundle."`
	Context   string `json:"context,omitempty" jsonschema:"resourceContext tier: 'basic' (default; attaches managedBy / exposes / selectedBy / uses / runsOn / issueSummary / auditSummary rollups) or 'none' (bare minified resource). For full diagnostic tier with logs + events bundled, use the diagnose tool instead."`
}

type topologyInput struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"filter to a specific namespace for a multi-service incident map; recommended unless you need cross-namespace topology"`
	View      string `json:"view,omitempty" jsonschema:"view mode: traffic for service routing/connectivity or resources for ownership hierarchy"`
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
	Grep      string `json:"grep,omitempty" jsonschema:"optional regular expression to keep matching log lines before diagnostic filtering, like kubectl logs | grep PATTERN"`
	Since     string `json:"since,omitempty" jsonschema:"only return logs newer than this duration (e.g. 30s, 10m, 1h), like kubectl logs --since"`
	Previous  bool   `json:"previous,omitempty" jsonschema:"return logs from the previous terminated container instance (e.g. for CrashLoopBackOff diagnosis), like kubectl logs -p"`
}

type searchInput struct {
	Query   string `json:"query" jsonschema:"search query for unknown resources or broad content scans. Free tokens AND'd. Matches identity plus searchable object content. Examples: adServiceFailure, kind:NetworkChaos delay, kind:ConfigMap flagd, image:flagd. Modifiers: kind:Pod, kind:NetworkChaos, ns:foo, label:k=v, image:redis"`
	Limit   int    `json:"limit,omitempty" jsonschema:"max hits returned (default 50, max 500)"`
	Include string `json:"include,omitempty" jsonschema:"per-hit detail: summary (default), raw, or none"`
	Filter  string `json:"filter,omitempty" jsonschema:"optional CEL boolean expression run against each candidate K8s object. Bindings: kind, apiVersion, metadata, spec, status, labels, annotations. Use has(x.y) before optional fields. Examples: 'kind == \"Pod\" && status.phase == \"Failed\"', 'labels[\"app\"] == \"cart\"', 'has(status.readyReplicas) && status.readyReplicas == 0'"`
	Context string `json:"context,omitempty" jsonschema:"per-hit context: default attaches summaryContext (managedBy + health + issueCount) for suspect ranking; 'none' returns bare hits"`
}

type issuesInput struct {
	Namespace string `json:"namespace,omitempty" jsonschema:"filter to one namespace"`
	Severity  string `json:"severity,omitempty" jsonschema:"comma-separated: critical,warning"`
	Kind      string `json:"kind,omitempty" jsonschema:"comma-separated kind filter (e.g. Deployment,Pod)"`
	Limit     int    `json:"limit,omitempty" jsonschema:"max issues returned (default 200, max 1000)"`
	Filter    string `json:"filter,omitempty" jsonschema:"optional CEL boolean expression run against each composed Issue. Bindings: severity (critical|warning), category (e.g. crashloop, image_pull_failed, missing_config_ref, gitops_sync_failed), category_group (startup|runtime|scheduling|configuration|networking|storage|scaling|security|control_plane; runtime here is an issue taxonomy group, not issue_timing), source (problem=built-in Radar detector, missing_ref=dangling by-name reference, scheduling=pod startup blocker, condition=False controller/CRD condition), kind, group, ns (the namespace — use 'ns', not 'namespace' which is a CEL reserved word), name, reason, message, cause, action, remediation_kind, remediation_target, count (int, the affected-resource fan-out), grouping_scope (workload|service|node|…), restart_count (int), last_terminated_reason, operation_retry_count (int, a GitOps controller's sync-operation retries — distinct from restart_count), stuck (bool, issue not expected to self-recover), issue_timing (string timing evidence: 'started_at_resource_creation' = evidence places the failing state during resource creation or first reconciliation; 'started_after_resource_was_healthy' = evidence shows a meaningful healthy window before the failing condition appeared; absent = Radar has no clean signal, do NOT infer timing from age alone; this is timing evidence, not a root-cause verdict), issue_timing_basis (string: evidence used — 'condition' | 'owner_condition' | 'pod_creation' | 'deletion' | 'phase' | 'spec'), first_seen + last_seen (unix seconds — prefer first_seen for onset/age; last_seen churns to compose-time). For cross-cluster scoping use clusters= (not a CEL predicate). Examples: 'severity == \"critical\" && count > 5', 'category_group == \"startup\"', 'restart_count > 10', 'remediation_kind == \"create-namespace\"', 'stuck && operation_retry_count >= 5', 'issue_timing == \"started_after_resource_was_healthy\"', 'first_seen < timestamp(\"2026-05-01T00:00:00Z\").getSeconds()'"`
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
	// Surface the truncation explicitly so the agent doesn't read the capped
	// problem list as the full set. The wire shape already carries
	// TotalProblems + ProblemsBySeverity for richer counts; this hint is
	// the steering signal that says "you're seeing a subset, narrow with
	// namespace= for full coverage."
	if dashboard.TotalProblems > len(dashboard.Problems) {
		return toJSONResult(getDashboardResponseMCP{
			mcpDashboard: dashboard,
			NarrowHint: fmt.Sprintf(
				"showing %d of %d problems (sorted by severity then recency) — narrow with namespace= for the full list at that scope",
				len(dashboard.Problems), dashboard.TotalProblems,
			),
		})
	}
	return toJSONResult(dashboard)
}

// getDashboardResponseMCP wraps mcpDashboard so the JSON shape stays
// identical except for the added narrowHint field when the dashboard cap
// truncated the problem list. Same pattern as get_changes / get_events.
type getDashboardResponseMCP struct {
	mcpDashboard
	NarrowHint string `json:"narrowHint,omitempty"`
}

func handleTopResources(ctx context.Context, _ *mcp.CallToolRequest, input topResourcesInput) (*mcp.CallToolResult, any, error) {
	opts := k8s.NormalizeTopMetricsOptions(k8s.TopMetricsOptions{
		Kind:      input.Kind,
		Namespace: input.Namespace,
		Sort:      input.Sort,
		Limit:     input.Limit,
	})
	switch opts.Kind {
	case k8s.TopMetricsKindPods, k8s.TopMetricsKindWorkloads:
	case k8s.TopMetricsKindNodes:
		if !canReadClusterScopedKind(ctx, "nodes", "", "list") {
			return toJSONResult(k8s.TopMetricsResponse{
				Kind:   opts.Kind,
				Sort:   opts.Sort,
				Reason: "no access to nodes (cluster-scoped resource requires explicit RBAC)",
			})
		}
	default:
		return nil, nil, fmt.Errorf("unknown kind %q (want pods, workloads, or nodes)", input.Kind)
	}

	if opts.Kind != k8s.TopMetricsKindNodes {
		if opts.Namespace != "" {
			if !checkNamespaceAccess(ctx, opts.Namespace) {
				return toJSONResult(k8s.TopMetricsResponse{
					Kind:      opts.Kind,
					Sort:      opts.Sort,
					Namespace: opts.Namespace,
					Reason:    "no access to namespace",
				})
			}
		} else if filterNamespacesForUser(ctx, nil) != nil {
			return nil, nil, fmt.Errorf("namespace is required when access is namespace-restricted")
		}
	}

	resp := k8s.BuildTopMetrics(opts)
	// Two narrowHint conditions can fire together:
	//   1. results at/above the limit suggest the cap was reached
	//   2. some resources were skipped because metrics-server hasn't
	//      scraped them yet (new pods, Pending, scrape gap) — useful
	//      to surface so the agent knows "no top consumers" isn't
	//      necessarily "no data" when SkippedNoMetrics is high.
	itemCount := len(resp.Items)
	if itemCount == 0 {
		itemCount = len(resp.Workloads)
	}
	var hints []string
	if opts.Limit > 0 && itemCount >= opts.Limit {
		hints = append(hints, fmt.Sprintf(
			"returned %d rows at limit — narrow with namespace= (for pods/workloads), tighten sort by switching cpu/memory, or raise limit (cap 100)",
			itemCount,
		))
	}
	if resp.SkippedNoMetrics > 0 {
		hints = append(hints, fmt.Sprintf(
			"%d %s skipped (no metrics samples yet — typically new/Pending pods or a metrics-server scrape gap)",
			resp.SkippedNoMetrics, opts.Kind,
		))
	}
	if len(hints) > 0 {
		return toJSONResult(topResourcesResponseMCP{
			TopMetricsResponse: resp,
			NarrowHint:         strings.Join(hints, "; "),
		})
	}
	return toJSONResult(resp)
}

// topResourcesResponseMCP embeds TopMetricsResponse so the JSON shape stays
// identical except for the added narrowHint field.
type topResourcesResponseMCP struct {
	k8s.TopMetricsResponse
	NarrowHint string `json:"narrowHint,omitempty"`
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

	// When a group is specified, route to the dynamic cache so CRDs whose
	// plural collides with a core kind (e.g. Knative serving.knative.dev/Service
	// vs corev1 ""/Service) reach the right resource. FetchResourceList is
	// group-blind — it would silently return the core typed list, dropping the
	// caller's group filter. But a built-in addressed by its own group
	// (deployments?group=apps) is a typed lookup — the dynamic cache has no
	// informer for built-ins — so only true CRDs / plural collisions go dynamic.
	// Mirrors the group-aware dispatch in the REST handlers.
	if group != "" && !k8s.TypedKindOwnsGroup(kind, group) {
		return listDynamicResources(ctx, cache, kind, group, listScope, clusterScoped, input.Context)
	}

	// Try typed cache first (group=="" → core/built-in lookup).
	objs, err := k8s.FetchResourceList(cache, kind, listScope)
	if err == k8s.ErrUnknownKind {
		// Fall through to dynamic cache for CRDs. ClassifyKindScope/SAR
		// above already authorized cluster-scoped CRDs; namespaced CRDs
		// are scoped via listScope. Pass clusterScoped through so the
		// issue index drops the namespace filter for cluster-scoped
		// CRDs — those issues live at namespace="" and would otherwise
		// be filtered out by the user's namespaced-access set.
		return listDynamicResources(ctx, cache, kind, group, listScope, clusterScoped, input.Context)
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

	// Attach summaryContext per row unless caller opted out. Issue index
	// is scoped to the listed kind so the per-row count reflects only
	// the resource being listed (not unrelated noise in the namespace).
	//
	// Cluster-scoped kinds (Node, PV, cluster-scoped CRDs) emit issues
	// at namespace="" — scoping the index to the user's namespaced
	// access set would silently zero issueCount on every row. The
	// cluster-scoped RBAC gate above (canReadClusterScopedKind) already
	// authorized the read, so we pass nil here to compose cluster-wide.
	if input.Context != "none" {
		idxNamespaces := allowed
		if clusterScoped {
			idxNamespaces = nil
		}
		if builder := newResourceSummaryContextBuilder(idxNamespaces); builder != nil {
			summarycontext.AttachToTypedList(results, objs, builder)
		}
	}

	return toJSONResult(results)
}

func listDynamicResources(ctx context.Context, cache *k8s.ResourceCache, kind, group string, namespaces []string, clusterScoped bool, contextMode string) (*mcp.CallToolResult, any, error) {
	var rawItems []*unstructured.Unstructured
	if len(namespaces) > 0 {
		for _, ns := range namespaces {
			items, err := cache.ListDynamicWithGroup(ctx, kind, ns, group)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to list %s: %w", kind, err)
			}
			rawItems = append(rawItems, items...)
		}
	} else {
		items, err := cache.ListDynamicWithGroup(ctx, kind, "", group)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to list %s: %w", kind, err)
		}
		rawItems = items
	}

	allItems := make([]any, 0, len(rawItems))
	for _, item := range rawItems {
		allItems = append(allItems, aicontext.MinifyUnstructured(item, aicontext.LevelSummary))
	}

	if contextMode != "none" {
		// Cluster-scoped CRDs emit issues at namespace="" — passing a
		// namespace-restricted slice would silently zero issueCount on
		// every row. Caller has already gated cluster-scoped reads via
		// canReadClusterScopedKind, so cluster-wide compose is safe.
		idxNamespaces := namespaces
		if clusterScoped {
			idxNamespaces = nil
		}
		if builder := newResourceSummaryContextBuilder(idxNamespaces); builder != nil {
			summarycontext.AttachToUnstructuredList(allItems, rawItems, builder)
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

	// Fetch the resource. When group is set, skip the typed cache and route
	// directly to the dynamic cache: typed FetchResource is group-blind
	// (e.g. for kind=services it returns the core Service regardless of any
	// group qualifier), so a group-qualified call like serving.knative.dev/
	// Service would silently leak the wrong object.
	var resourceData any
	var rawObj runtime.Object
	if group != "" && !k8s.TypedKindOwnsGroup(kind, group) {
		u, dynErr := cache.GetDynamicWithGroup(ctx, kind, namespace, name, group)
		if dynErr != nil {
			return nil, nil, notFoundError(ctx, dynErr, kind, namespace, name)
		}
		resourceData = aicontext.MinifyUnstructured(u, aicontext.LevelDetail)
		rawObj = u
	} else {
		obj, err := k8s.FetchResource(cache, kind, namespace, name)
		if err == k8s.ErrUnknownKind {
			u, dynErr := cache.GetDynamicWithGroup(ctx, kind, namespace, name, group)
			if dynErr != nil {
				return nil, nil, notFoundError(ctx, dynErr, kind, namespace, name)
			}
			resourceData = aicontext.MinifyUnstructured(u, aicontext.LevelDetail)
			rawObj = u
		} else if err != nil {
			return nil, nil, notFoundError(ctx, err, kind, namespace, name)
		} else {
			k8s.SetTypeMeta(obj)
			minified, minErr := aicontext.Minify(obj, aicontext.LevelDetail)
			if minErr != nil {
				return nil, nil, fmt.Errorf("failed to minify: %w", minErr)
			}
			resourceData = minified
			rawObj = obj
		}
	}

	// Large ConfigMaps are token bombs that make agents anchor on volume
	// instead of signal — truncate oversized data values with an explicit
	// note. Applied regardless of context mode.
	var truncationNote string
	if meaningfulchanges.ConfigMapKind(kind) {
		resourceData, truncationNote = truncateLargeConfigMapData(resourceData)
	}

	// Build the resourceContext section unless the caller opted out. Basic
	// tier is the default: cheap managedBy / exposes / selectedBy /
	// runsOn / uses / issueSummary / auditSummary / policySummary. Pass
	// context=none for a bare minified resource (bulk scans, raw jq work).
	contextMode := strings.ToLower(strings.TrimSpace(input.Context))
	includes := parseIncludes(input.Include)
	skipContext := contextMode == "none"

	var resourceCtx *resourcecontext.ResourceContext
	var warnings []string
	if !skipContext {
		resourceCtx = buildMCPResourceContext(ctx, rawObj, kind, namespace, name, resourcecontext.TierBasic)

		// State-derived advisory warnings (deletionTimestamp, external manager,
		// terminating namespace, workload health-condition history, PVC stuck
		// Pending). Cheap — operates on the object we already fetched. Skipped
		// for context=none so that mode stays a bare object for raw jq work;
		// the ConfigMap truncation note appended below is the one warning that
		// survives context=none, because silent truncation would read as a
		// complete value.
		warnings = k8score.EnrichRuntimeObjectWarnings(rawObj)
	}
	if truncationNote != "" {
		warnings = append(warnings, truncationNote)
	}

	defaultConfigMapChanges := meaningfulchanges.ConfigMapKind(kind)
	includeChanges := includes["changes"] || defaultConfigMapChanges
	var recentChanges []issuesapi.RecentChange
	var changesErr string
	if includeChanges {
		changes, _, err := meaningfulchanges.RecentForResource(ctx, kind, namespace, name, meaningfulchanges.DefaultSince, meaningfulchanges.ResourceLimit, meaningfulchanges.DefaultFieldLimit)
		if err != nil {
			changesErr = err.Error()
		} else {
			recentChanges = changes
		}
	}

	// Three shapes:
	//   - bare resource: no includes, context=none, no warnings (a truncation
	//     warning upgrades this to {resource, warnings})
	//   - resource + resourceContext: no includes, default context
	//   - resource + resourceContext + extras: includes set
	if len(includes) == 0 && resourceCtx == nil && len(warnings) == 0 && !defaultConfigMapChanges {
		return toJSONResult(resourceData)
	}

	result := map[string]any{"resource": resourceData}
	if resourceCtx != nil {
		result["resourceContext"] = resourceCtx
	}
	if len(warnings) > 0 {
		result["warnings"] = warnings
	}
	if includeChanges {
		if changesErr != "" {
			result["recentChangesError"] = changesErr
		} else if includes["changes"] || len(recentChanges) > 0 {
			result["recentChanges"] = recentChanges
		}
	}
	if len(includes) > 0 {
		attachResourceExtras(ctx, cache, result, includes, kind, namespace, name)
	}
	return toJSONResult(result)
}

// buildMCPResourceContext assembles the resourceContext section for MCP
// get_resource. Mirrors the REST handler's buildAIResourceContext: pre-
// computes IssueSummary + AuditSummary in the caller, threads the
// PolicyReport index when Kyverno is installed, hands a request-scoped
// RBAC checker to Build for per-ref gating, and lets Build's own
// fallback resolve Relationships via topology.GetRelationshipsWithObject
// (which applies KindForGVK so cross-group CRDs map to the right
// topology node).
func buildMCPResourceContext(ctx context.Context, obj runtime.Object, kind, namespace, name string, tier resourcecontext.ContextTier) *resourcecontext.ResourceContext {
	if obj == nil {
		return nil
	}
	cache := k8s.GetResourceCache()

	gvk := obj.GetObjectKind().GroupVersionKind()
	canonicalKind := gvk.Kind
	if canonicalKind == "" {
		canonicalKind = kind
	}
	canonicalGroup := gvk.Group

	issueSum := computeMCPIssueSummary(cache, canonicalGroup, canonicalKind, namespace, name)
	auditSum := computeMCPAuditSummary(cache, canonicalGroup, canonicalKind, namespace, name)

	opts := resourcecontext.Options{
		Tier:            tier,
		AccessChecker:   newMCPRequestScopedChecker(ctx),
		IssueSummary:    issueSum,
		AuditSummary:    auditSum,
		AppReferences:   resourcecontextrefs.AppReferencesFromEnvServiceChecks(k8s.FindEnvServiceRefChecksForObject(cache, obj)),
		ServiceBackends: mcpServiceBackendLookup{cache: cache},
	}

	if idx := k8s.GetPolicyReportIndex(); idx != nil {
		opts.PolicyReports = mcpPolicyReportLookupAdapter{idx: idx}
	}

	if topo, prov, dyn, ok := mcpTopologyForContext(namespace); ok {
		opts.Topology = topo
		opts.Provider = prov
		opts.DynamicProv = dyn
	}

	return resourcecontext.Build(ctx, obj, opts)
}

// attachResourceExtras populates optional extras (events, metrics, logs) on
// the result map based on the includes set. relationship synthesis moved to
// resourceContext via Build and is no longer routed through this function.
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
				result["eventsError"] = listErr.Error()
			} else {
				// Supplemental include — controller-level events only. Pod-level
				// events on a workload's pods (CrashLoopBackOff, etc.) require
				// resolving the pod set; that's the diagnose tool's job, not
				// this include's. nil podNames intentionally restricts to
				// InvolvedObject == this kind+name.
				matched := filterEventsByInvolvedObject(events, normalizeDisplayKind(kind), name, nil)
				if len(matched) > 0 {
					deduplicated := aicontext.DeduplicateEvents(matched)
					if len(deduplicated) > 10 {
						deduplicated = deduplicated[:10]
					}
					result["events"] = deduplicated
				}
			}
		} else {
			result["eventsError"] = "events lister unavailable (insufficient permissions or cache cold)"
		}
	}

	if includes["metrics"] {
		if isPodKind(kind) {
			if metrics, err := k8s.GetPodMetrics(ctx, namespace, name); err == nil {
				result["metrics"] = metrics
			} else {
				log.Printf("[mcp] Failed to get pod metrics for %s/%s: %v", namespace, name, err)
				result["metricsError"] = err.Error()
			}
		}
	}

	if includes["changes"] {
		// The handler may have already attempted changes (data OR error key set).
		// Gate on both — retrying after a recorded failure could attach a fresh
		// payload next to the stale error, handing clients a contradictory result.
		_, hasChanges := result["recentChanges"]
		_, hasChangesErr := result["recentChangesError"]
		if !hasChanges && !hasChangesErr {
			if changes, _, err := meaningfulchanges.RecentForResource(ctx, kind, namespace, name, meaningfulchanges.DefaultSince, meaningfulchanges.ResourceLimit, meaningfulchanges.DefaultFieldLimit); err == nil {
				result["recentChanges"] = changes
			} else {
				result["recentChangesError"] = err.Error()
			}
		}
	}

	// include=logs was dropped from get_resource (it was Pod-only and lacked
	// container/previous/since/grep). Signal it explicitly rather than silently
	// no-op'ing, so a client on a stale schema is redirected instead of seeing
	// an empty success.
	if includes["logs"] {
		result["logsError"] = "include=logs is no longer supported here; use get_pod_logs or get_workload_logs (container, previous, since, grep) or diagnose for the full workload bundle"
	}

	// Any other token (typo, or a value like "relationships" that moved to
	// resourceContext) is silently dropped by the branches above. Surface it
	// so the caller learns the token did nothing rather than seeing an empty
	// success — the same reason logs gets an explicit error.
	var unknown []string
	for tok := range includes {
		switch tok {
		case "events", "metrics", "logs", "changes":
		default:
			unknown = append(unknown, tok)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		result["includeError"] = fmt.Sprintf("unknown include value(s): %s (valid: events, metrics, changes)", strings.Join(unknown, ", "))
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

// filterChangesByClusterScopedRBAC drops change entries for cluster-scoped
// kinds the user lacks RBAC to read. The change feed namespace-filters but did
// not honor per-kind cluster-scoped RBAC — fine while every tracked kind was
// namespaced, but now that cluster-scoped kinds (admission webhook configs) are
// in the feed it would be a side channel around the SAR that gates every other
// read. No-op when no per-user RBAC is on context: canReadClusterScopedKind
// returns true (the radar-SA / benchmark case), and it short-circuits true for
// namespaced kinds, so only cluster-scoped denials are dropped.
func filterChangesByClusterScopedRBAC(ctx context.Context, changes []issuesapi.RecentChange) []issuesapi.RecentChange {
	filtered := changes[:0]
	for _, c := range changes {
		if canReadClusterScopedKind(ctx, c.Kind, "", "list") {
			filtered = append(filtered, c)
		}
	}
	return filtered
}

func handleGetChanges(ctx context.Context, req *mcp.CallToolRequest, input getChangesInput) (*mcp.CallToolResult, any, error) {
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
		// Wrap the empty result so capped + uncapped + denied agree on wire shape.
		return toJSONResult(getChangesResponseMCP{Changes: []mcpChange{}})
	}

	query := meaningfulchanges.Query{
		Since:      since,
		Limit:      50,
		FieldLimit: meaningfulchanges.DefaultFieldLimit,
		Name:       input.Name,
	}
	switch {
	case input.Namespace != "":
		query.Namespaces = []string{input.Namespace}
	case allowed != nil:
		query.Namespaces = allowed
	}
	if input.Kind != "" {
		query.Kinds = []string{input.Kind}
	}

	changes, capped, err := meaningfulchanges.Recent(ctx, query)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to query timeline: %w", err)
	}
	var sourcesErrored []string
	helmChanges, err := helmRecentChangesForContext(ctx, input, since)
	if err != nil {
		sourcesErrored = append(sourcesErrored, "helm: "+err.Error())
	} else {
		changes = append(changes, helmChanges...)
	}
	changes = filterChangesByClusterScopedRBAC(ctx, changes)
	totalMatched := len(changes)
	meaningfulchanges.RankAndCap(&changes, limit)

	// Always wrap so capped + uncapped agree on wire shape
	// ({changes: [...], narrowHint?: "..."}).
	resp := getChangesResponseMCP{
		Changes:        changes,
		SourcesErrored: sourcesErrored,
	}
	if capped || totalMatched > len(changes) {
		resp.NarrowHint = fmt.Sprintf(
			"meaningful change results were capped or candidate queries saturated — narrow with namespace=, kind=, name=, shorten since= (e.g. 15m), or raise limit (cap 50)",
		)
	}
	return toJSONResult(resp)
}

type getChangesResponseMCP struct {
	Changes        []mcpChange `json:"changes"`
	NarrowHint     string      `json:"narrowHint,omitempty"`
	SourcesErrored []string    `json:"sourcesErrored,omitempty"`
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

	// Steering for oversized graph responses — reuses the topology Warnings
	// channel so the response shape stays stable.
	const topologyGraphNodeHintThreshold = 300
	if len(topo.Nodes) > topologyGraphNodeHintThreshold {
		topo.Warnings = append(topo.Warnings, fmt.Sprintf(
			"large graph (%d nodes) — scope with namespace=, use format=summary, or get_neighborhood around a suspect root",
			len(topo.Nodes),
		))
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
		// Wrap the empty result so capped + uncapped + denied agree on wire shape.
		return toJSONResult(getEventsResponseMCP{Events: []aicontext.DeduplicatedEvent{}})
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
	preCap := len(deduplicated)
	if preCap > limit {
		deduplicated = deduplicated[:limit]
	}

	// Always wrap into the response struct so capped + uncapped agree on
	// wire shape ({events: [...], narrowHint?: "..."}).
	resp := getEventsResponseMCP{Events: deduplicated}
	if preCap > limit {
		resp.NarrowHint = fmt.Sprintf(
			"returned %d of %d events — narrow with namespace=, kind=, name=, or raise limit (cap 100)",
			limit, preCap,
		)
	}
	return toJSONResult(resp)
}

type getEventsResponseMCP struct {
	Events     []aicontext.DeduplicatedEvent `json:"events"`
	NarrowHint string                        `json:"narrowHint,omitempty"`
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
	if strings.TrimSpace(input.Grep) != "" {
		if _, err := regexp.Compile(input.Grep); err != nil {
			return nil, nil, fmt.Errorf("invalid grep regex: %w", err)
		}
	}
	sinceSeconds, err := parseLogsSince(input.Since)
	if err != nil {
		return nil, nil, err
	}

	opts := &corev1.PodLogOptions{
		TailLines:    &tailLines,
		SinceSeconds: sinceSeconds,
		Previous:     input.Previous,
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

	// rawLines counts lines BEFORE grep — grep filtering down would make
	// filtered.TotalLines (post-grep) smaller than tailLines even on a
	// capped stream, suppressing the hint exactly when the agent needs it.
	rawLines := countLines(string(data))
	filtered, err := aicontext.FilterLogsByPattern(string(data), input.Grep)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid grep regex: %w", err)
	}

	warnings := computePodLogsWarnings(input.Namespace, input.Name, input.Container, input.Previous, rawLines)
	var narrowHint string
	// Heuristic: if we received tailLines or more raw lines, the kubectl
	// stream was likely capped (there may be older lines we didn't fetch).
	if int64(rawLines) >= tailLines {
		narrowHint = fmt.Sprintf(
			"log stream tailed to %d lines (cap reached) — narrow with since= (e.g. 10m), grep= regex, container=, or raise tail_lines",
			rawLines,
		)
	}
	if narrowHint == "" && len(warnings) == 0 {
		return toJSONResult(filtered)
	}
	return toJSONResult(podLogsResponseMCP{
		FilteredLogs: filtered,
		NarrowHint:   narrowHint,
		Warnings:     warnings,
	})
}

// computePodLogsWarnings inspects the pod's status to surface common
// pitfalls in interpreting an empty/short logs response:
//
//   - when the pod isn't Running, application logs are usually unavailable
//     because the container hasn't started yet. The agent thinks the app
//     isn't writing logs when actually the pod is still scheduling/pulling.
//   - when a container has restarted (restartCount > 0) and the caller didn't
//     pass previous=true, the current container's logs are likely the
//     next-crash in progress; the error that killed the last container is in
//     previous.
//
// Best-effort — if the pod can't be fetched, return no warnings rather than
// failing the logs call (the caller already has whatever logs we returned).
func computePodLogsWarnings(namespace, name, container string, previous bool, rawLines int) []string {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil
	}
	obj, err := k8s.FetchResource(cache, "pods", namespace, name)
	if err != nil {
		return nil
	}
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return nil
	}

	var out []string

	if pod.Status.Phase != corev1.PodRunning && pod.Status.Phase != corev1.PodSucceeded {
		out = append(out, fmt.Sprintf(
			"Pod is in phase `%s`; application logs are typically unavailable until containers start. Inspect scheduling/pull state via `diagnose` (kind=pod), `get_resource` with include=events, or check pod conditions for the underlying blocker.",
			pod.Status.Phase,
		))
	}

	if !previous {
		statuses := pod.Status.ContainerStatuses
		if container != "" {
			statuses = filterContainerStatuses(statuses, container)
		}
		if cs := pickCrashIndicator(statuses); cs != nil {
			reason := cs.LastTerminationState.Terminated.Reason
			if reason == "" {
				reason = "(reason unset)"
			}
			out = append(out, fmt.Sprintf(
				"Container `%s` has restarted %d time(s); last termination: `%s` (exit code %d). The error that triggered the previous crash is in the previous container's logs — call again with `previous: true`%s.",
				cs.Name,
				cs.RestartCount,
				reason,
				cs.LastTerminationState.Terminated.ExitCode,
				crashloopSuffix(rawLines),
			))
		}
	}

	return out
}

// pickCrashIndicator returns the first container with restartCount > 0 that has
// a recorded previous termination, or nil when no container has crashed. The
// previous termination may have an empty reason; the caller renders that.
func pickCrashIndicator(statuses []corev1.ContainerStatus) *corev1.ContainerStatus {
	for i := range statuses {
		cs := &statuses[i]
		if cs.RestartCount == 0 {
			continue
		}
		if cs.LastTerminationState.Terminated == nil {
			continue
		}
		return cs
	}
	return nil
}

func filterContainerStatuses(statuses []corev1.ContainerStatus, name string) []corev1.ContainerStatus {
	for i := range statuses {
		if statuses[i].Name == name {
			return statuses[i : i+1]
		}
	}
	return nil
}

func crashloopSuffix(rawLines int) string {
	if rawLines == 0 {
		return " (current logs returned empty — likely captured between restarts)"
	}
	return ""
}

// countLines returns the number of newline-delimited lines in s, treating
// a trailing newline as a terminator (not a separate line). Used to detect
// stream truncation before grep filtering rewrites TotalLines.
func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// podLogsResponseMCP embeds FilteredLogs so the JSON shape stays identical
// except for added narrowHint + warnings fields.
type podLogsResponseMCP struct {
	aicontext.FilteredLogs
	NarrowHint string   `json:"narrowHint,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
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
	Cluster            mcpClusterInfo         `json:"cluster"`
	Nodes              mcpNodeSummary         `json:"nodes"`
	VersionSkew        []string               `json:"versionSkew,omitempty"`
	Health             mcpHealthSummary       `json:"health"`
	Problems           []mcpProblem           `json:"problems"`
	TotalProblems      int                    `json:"totalProblems"`                // count before the dashboard cap was applied
	ProblemsBySeverity map[string]int         `json:"problemsBySeverity,omitempty"` // critical/high/medium/warning counts across the full set
	RecentChanges      []mcpChange            `json:"recentChanges,omitempty"`
	WarningEvents      int                    `json:"warningEvents"`
	TopWarnings        []mcpWarning           `json:"topWarnings"`
	HelmReleases       mcpHelmSummary         `json:"helmReleases"`
	Metrics            *mcpMetrics            `json:"metrics,omitempty"`
	TopologyNodes      int                    `json:"topologyNodes"`
	TopologyEdges      int                    `json:"topologyEdges"`
	ResourceCounts     map[string]int         `json:"resourceCounts"`
	Visibility         *k8s.VisibilitySummary `json:"visibility,omitempty"`
}

type mcpChange = issuesapi.RecentChange

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
	Kind                 string `json:"kind"`
	Namespace            string `json:"namespace,omitempty"`
	Name                 string `json:"name"`
	Group                string `json:"group,omitempty"`
	Severity             string `json:"severity,omitempty"`
	Reason               string `json:"reason"`
	Message              string `json:"message,omitempty"`
	Age                  string `json:"age"`
	RestartCount         int32  `json:"restartCount,omitempty"`         // Pod problems only
	LastTerminatedReason string `json:"lastTerminatedReason,omitempty"` // Pod problems only (OOMKilled / Error / Completed)
	// ageSeconds is for sort tiebreak; not serialized so it doesn't widen
	// the wire shape callers depend on.
	ageSeconds int64 `json:"-"`
}

// problemSeverityRank maps the Problem.Severity vocabulary onto sort order.
// Lower rank sorts first. Unknown severities fall to the end (rank=99) so a
// future "info"-tier value doesn't accidentally outrank critical via the
// Go zero-value (which would happen with map[string]int default lookups).
func problemSeverityRank(s string) int {
	switch s {
	case "critical":
		return 0
	case "high":
		return 1
	case "medium":
		return 2
	case "warning":
		return 3
	}
	return 99
}

// sortMCPDashboardProblems applies (severity desc, age desc) to the
// dashboard problem list before the cap, so a critical missing-ref isn't
// dropped in favour of a medium-severity workload problem that happened
// to be appended earlier.
func sortMCPDashboardProblems(problems []mcpProblem) {
	sort.SliceStable(problems, func(i, j int) bool {
		ri, rj := problemSeverityRank(problems[i].Severity), problemSeverityRank(problems[j].Severity)
		if ri != rj {
			return ri < rj
		}
		// Same severity: most recent (lowest ageSeconds) first. Newly-
		// failed resources are usually more interesting for triage than
		// the chronic crash-loopers — and chronic ones are already
		// signaled via the RestartCount field on the row.
		return problems[i].ageSeconds < problems[j].ageSeconds
	})
}

// countBySeverity tallies problems across the full uncapped set so the
// agent can see the real scale even when only 30 rows are returned.
func countBySeverity(problems []mcpProblem) map[string]int {
	if len(problems) == 0 {
		return nil
	}
	out := make(map[string]int, 4)
	for _, p := range problems {
		out[p.Severity]++
	}
	return out
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
	Name                     string              `json:"name"`
	Namespace                string              `json:"namespace"`
	StorageNamespace         string              `json:"storageNamespace,omitempty"`
	Chart                    string              `json:"chart"`
	ChartVersion             string              `json:"chartVersion"`
	Status                   string              `json:"status"`
	Revision                 int                 `json:"revision"`
	Updated                  time.Time           `json:"updated"`
	ResourceHealth           string              `json:"resourceHealth,omitempty"`
	HealthIssue              string              `json:"healthIssue,omitempty"`
	HealthSummary            string              `json:"healthSummary,omitempty"`
	LastOperation            *helm.HelmOperation `json:"lastOperation,omitempty"`
	ManagedByFluxHelmRelease string              `json:"managedByFluxHelmRelease,omitempty"`
}

func mcpHelmDashboardPriority(r helm.HelmRelease) int {
	return helm.ReleasePriority(r)
}

func buildDashboard(ctx context.Context, cache *k8s.ResourceCache, namespace string, includeNodes bool, includeNamespaces bool) mcpDashboard {
	d := mcpDashboard{
		ResourceCounts: make(map[string]int),
	}
	if result := k8s.GetCachedPermissionResult(); result != nil {
		d.Visibility = k8s.BuildVisibilitySummary(result, namespace)
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

	// Collect all problems first (uncapped), sort by severity+age, then
	// apply the dashboard cap so higher-severity findings cannot be displaced
	// by lower-severity findings appended earlier in the sequence.
	var allProblems []mcpProblem

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
			switch health.Pod(pod, now).Level {
			case health.LevelDegraded, health.LevelUnknown:
				// unknown = node-lost / unobservable; surface as warning rather than
				// letting the legacy collapse hide it in the healthy count.
				d.Health.WarningPods++
			case health.LevelUnhealthy:
				d.Health.ErrorPods++
				severity := "critical"
				// PodProblemReason returns the kubelet's waiting/terminated
				// reason; ClassifyPodHealth==error implies the pod is in a
				// failing state, so critical is the right default.
				restarts, lastTermReason := health.PodRestartContext(pod)
				ageDur := now.Sub(pod.CreationTimestamp.Time)
				allProblems = append(allProblems, mcpProblem{
					Kind:                 "Pod",
					Namespace:            pod.Namespace,
					Name:                 pod.Name,
					Severity:             severity,
					Reason:               health.PodProblemReason(pod, now),
					Age:                  k8s.FormatAge(ageDur),
					RestartCount:         restarts,
					LastTerminatedReason: lastTermReason,
					ageSeconds:           int64(ageDur.Seconds()),
				})
			default: // healthy, neutral
				d.Health.HealthyPods++
			}
		}
	}

	// DetectProblems emits Pod-level rows (CrashLoopBackOff, not-ready,
	// etc.) as well as workload/HPA/CronJob/Node ones. The dashboard pod
	// loop above is the canonical source for pod problems, so skip Pod
	// here to avoid the same failing pod appearing twice.
	for _, p := range k8s.DetectProblems(cache, namespace) {
		if p.Kind == "Pod" {
			continue
		}
		if p.Kind == "Node" && !includeNodes {
			continue
		}
		allProblems = append(allProblems, mcpProblem{
			Kind:                 p.Kind,
			Namespace:            p.Namespace,
			Name:                 p.Name,
			Group:                p.Group,
			Severity:             p.Severity,
			Reason:               p.Reason,
			Message:              p.Message,
			Age:                  p.Age,
			RestartCount:         p.RestartCount,
			LastTerminatedReason: p.LastTerminatedReason,
			ageSeconds:           p.AgeSeconds,
		})
	}

	// Scheduling problems: unschedulable pods (with the offending node
	// constraint named), admission rejections (quota/PodSecurity/webhook —
	// no Pod exists, so the pod loop above can't see them), and post-bind
	// CNI/volume stalls. The pod loop only emits "error" pods, so these are
	// additive; the seenProblem dedup below keys on reason, letting a pod's
	// scheduling row coexist with a distinct missing-ref row.
	sched := k8s.DetectSchedulingProblems(cache, namespace)
	sched = append(sched, k8s.DetectAdmissionProblems(cache, namespace)...)
	sched = append(sched, k8s.DetectPostBindProblems(cache, namespace)...)
	for _, p := range sched {
		allProblems = append(allProblems, mcpProblem{
			Kind:       p.Kind,
			Namespace:  p.Namespace,
			Name:       p.Name,
			Group:      p.Group,
			Severity:   p.Severity,
			Reason:     p.Reason,
			Message:    p.Message,
			Age:        p.Age,
			ageSeconds: p.AgeSeconds,
		})
	}

	// Missing-ref problems (Pod→missing CM/Secret/PVC/SA, HPA→missing
	// target, Ingress→missing backend, PVC→missing SC, RoleBinding→missing
	// roleRef). Pod rows are intentionally kept here because the pod-error
	// loop above only catches running-but-broken pods (CrashLoopBackOff,
	// not-ready); Pods that fail to schedule on a missing PVC stay Pending
	// without container statuses and would otherwise be invisible. Dedupe
	// Dedupe exact-duplicate rows, not resource identities. Keying on
	// (kind, ns, name, reason) lets distinct missing-ref reasons on the
	// same Pod survive — a Pod missing both PVC and ConfigMap emits two
	// rows — and prevents a generic kubelet-state row (e.g.
	// CreateContainerConfigError) from suppressing the underlying
	// root-cause row (e.g. Missing Secret) for the same resource.
	seenProblem := make(map[string]bool, len(allProblems))
	for _, p := range allProblems {
		seenProblem[p.Kind+"/"+p.Namespace+"/"+p.Name+"/"+p.Reason] = true
	}
	missingRefs := k8s.DetectMissingRefs(cache, namespace)
	missingRefs = append(missingRefs, k8s.DetectMissingWebhookRefs(cache, k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery(), namespace)...)
	for _, p := range missingRefs {
		if seenProblem[p.Kind+"/"+p.Namespace+"/"+p.Name+"/"+p.Reason] {
			continue
		}
		allProblems = append(allProblems, mcpProblem{
			Kind:       p.Kind,
			Namespace:  p.Namespace,
			Name:       p.Name,
			Group:      p.Group,
			Severity:   p.Severity,
			Reason:     p.Reason,
			Message:    p.Message,
			Age:        p.Age,
			ageSeconds: p.AgeSeconds,
		})
	}

	// CAPI problems (Cluster API resources)
	for _, p := range k8s.DetectCAPIProblems(k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery(), namespace) {
		allProblems = append(allProblems, mcpProblem{
			Kind:       p.Kind,
			Namespace:  p.Namespace,
			Name:       p.Name,
			Group:      p.Group,
			Severity:   p.Severity,
			Reason:     p.Reason,
			Message:    p.Message,
			Age:        p.Age,
			ageSeconds: p.AgeSeconds,
		})
	}

	// Sort by severity desc then by age desc (newest first), then cap.
	// Without this, the cap drops critical missing-refs in favour of
	// medium-severity workload problems that happened to be appended
	// earlier — the agent gets a non-representative subset.
	sortMCPDashboardProblems(allProblems)
	d.TotalProblems = len(allProblems)
	d.ProblemsBySeverity = countBySeverity(allProblems)
	const dashboardCap = 30
	if len(allProblems) > dashboardCap {
		allProblems = allProblems[:dashboardCap]
	}
	d.Problems = allProblems

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
				h := health.Node(node)
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
		namespaces := resolveHelmListNamespaces(ctx, namespace)
		if namespaces != nil && len(namespaces) == 0 {
			// No namespace access: leave the zero-value Helm summary.
		} else {
			releases, err := helmClient.ListReleasesAcrossNamespaces(namespaces, username, groups)
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

				// Sort: failed/pending first, then operation-signaled Helm rollbacks,
				// then unhealthy/degraded owned resources.
				sort.SliceStable(releases, func(i, j int) bool {
					return mcpHelmDashboardPriority(releases[i]) < mcpHelmDashboardPriority(releases[j])
				})

				limit := min(len(releases), 5)
				for _, r := range releases[:limit] {
					d.HelmReleases.Releases = append(d.HelmReleases.Releases, mcpHelmRelease{
						Name:                     r.Name,
						Namespace:                r.Namespace,
						StorageNamespace:         r.StorageNamespace,
						Chart:                    r.Chart,
						ChartVersion:             r.ChartVersion,
						Status:                   r.Status,
						Revision:                 r.Revision,
						Updated:                  r.Updated,
						ResourceHealth:           r.ResourceHealth,
						HealthIssue:              r.HealthIssue,
						HealthSummary:            r.HealthSummary,
						LastOperation:            r.LastOperation,
						ManagedByFluxHelmRelease: r.ManagedByFluxHelmRelease,
					})
				}
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
			Since:          now.Add(-1 * time.Hour),
			Limit:          20,
			FilterPreset:   "workloads",
			ClusterContext: k8s.ActiveClusterContext(),
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
			// Explicit denial must NOT read as "[] = nothing broken" — for an
			// agent that's an unauthorized → healthy trust gap. Surface it.
			return nil, nil, fmt.Errorf("forbidden: no access to namespace %q", input.Namespace)
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
	filters := issues.Filters{
		Severities: severities,
		Kinds:      splitCSVStr(input.Kind),
		Limit:      input.Limit,
		Namespaces: allowedNamespaces,
		// Agents get the grouped issue model — structured triage objects,
		// not per-pod evidence rows they'd have to re-aggregate. Raw object
		// state stays available via get_resource / get_events.
		Grouped: true,
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
	composeFilters := filters
	composeFilters.Limit = issues.NoLimit
	out, stats := issues.ComposeWithStats(provider, composeFilters)
	out, stats = issues.MergeExternalIssues(out, stats, filters, nativeHelmIssuesForContext(ctx, allowedNamespaces, filters))
	// Shared base response shape (issues.ListResponse), then MCP-specific
	// enrichments below.
	resp := issues.NewListResponse(out, stats)
	resp.ClusterContext = provider.ClusterContextForIssues(allowedNamespaces, func(group, resource string) bool {
		return canReadInNamespace(ctx, group, resource, "kube-system", "list")
	})
	if len(allowedNamespaces) == 1 && stats.TotalMatched == len(out) && meaningfulchanges.IssueChangesQueryEligible(input.Kind, input.Filter, input.Severity) {
		if recentChangesReason := meaningfulchanges.IssueChangesReason(out); recentChangesReason != "" {
			if changes, _, err := meaningfulchanges.Recent(ctx, meaningfulchanges.Query{
				Namespaces: []string{allowedNamespaces[0]},
				Since:      meaningfulchanges.DefaultSince,
				Limit:      meaningfulchanges.IssueChangesLimit,
				FieldLimit: meaningfulchanges.DefaultFieldLimit,
			}); err == nil && len(changes) > 0 {
				resp.RecentChanges = changes
				resp.RecentChangesReason = recentChangesReason
			}
		}
	}
	// Per-issue change correlation (single-namespace responses): each
	// critical issue carries either its correlated non-status changes or an
	// explicit no_recent_changes marker — deterministic per-subject evidence
	// the global recent_changes list can't bind to individual issues.
	if len(allowedNamespaces) == 1 {
		attachIssueChangeCorrelation(ctx, &resp)
	}
	// Steering hint when the issue list was capped (MCP-only).
	if stats.TotalMatched > len(out) {
		resp.NarrowHint = fmt.Sprintf(
			"returned %d of %d issues — narrow with namespace=, kind=, severity=critical, add filter= CEL, or raise limit (cap 1000)",
			len(out), stats.TotalMatched,
		)
	}
	if resp.ClusterContext != nil && resp.ClusterContext.DNS != nil && resp.ClusterContext.DNS.Hint != "" {
		if resp.NarrowHint != "" {
			resp.NarrowHint += "; "
		}
		resp.NarrowHint += resp.ClusterContext.DNS.Hint
	}
	if result := k8s.GetCachedPermissionResult(); result != nil {
		if visibility := k8s.BuildVisibilitySummary(result, k8s.VisibilityNamespace(allowedNamespaces)); visibility != nil {
			resp.Visibility = visibility
		}
	}
	return toJSONResult(resp)
}

func nativeHelmIssuesForContext(ctx context.Context, namespaces []string, filters issues.Filters) []issues.Issue {
	if !issues.KindFilterIncludes(filters.Kinds, "HelmRelease", "helmreleases") {
		return nil
	}
	helmClient := helm.GetClient()
	if helmClient == nil {
		return nil
	}
	username, groups := userFromContext(ctx)
	helmNamespaces := namespaces
	if helmNamespaces == nil {
		helmNamespaces = resolveHelmListNamespaces(ctx, "")
	}
	releases, err := helmClient.ListReleasesAcrossNamespaces(helmNamespaces, username, groups)
	if err != nil {
		if !helm.IsForbiddenError(err) {
			log.Printf("[mcp] Failed to list Helm releases for issue stream: %v", err)
		}
		return nil
	}
	return issues.NativeHelmReleaseIssues(releases, time.Now())
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
	query := input.Query
	if query == "" {
		return nil, nil, fmt.Errorf("query is required")
	}
	parsed := search.Parse(query)
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
	// Search uses the dual-index variant: hits are mixed-kind (a single
	// query can return both namespaced Pods and cluster-scoped Nodes),
	// so a single namespace-scoped issue index zeroes issueCount on
	// cluster-scoped hits whose problems live at namespace="". The
	// builder routes per-hit by scope; CanReadClusterScoped above
	// already gates which cluster-scoped kinds are reachable.
	if input.Context != "none" {
		if builder := newSearchSummaryContextBuilder(scanNamespaces); builder != nil {
			opts.SummaryBuilder = search.SummaryBuilderFunc(builder)
		}
	}
	result, err := search.Search(ctx, provider, parsed, opts)
	if err != nil {
		return nil, nil, err
	}
	// Steering hint when the result was capped.
	if result.TotalMatched > result.Total {
		return toJSONResult(searchResponseMCP{
			Result: result,
			NarrowHint: fmt.Sprintf(
				"returned %d of %d hits — narrow with modifiers (kind:, ns:, label:k=v, image:), tighten the query, add a filter= CEL expression, or raise limit (cap 500)",
				result.Total, result.TotalMatched,
			),
		})
	}
	return toJSONResult(result)
}

// searchResponseMCP embeds search.Result so the JSON shape stays identical
// except for the added narrowHint field.
type searchResponseMCP struct {
	search.Result
	NarrowHint string `json:"narrowHint,omitempty"`
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
