// Human-facing catalog for the MCP setup dialog (MCPSetupDialog.tsx).
//
// Source of truth for the *set* of tools is the backend registration in
// internal/mcp/tools.go. This list must stay in sync with it — the Go test
// TestSetupDialogCoversAllTools (internal/mcp/tools_catalog_test.go) parses
// this file and fails CI if the tool names here don't exactly match the
// registered tools. When you add or remove an MCP tool there, update this
// catalog too.
//
// Descriptions here are intentionally shorter and more human-facing than the
// LLM-oriented routing descriptions in tools.go — different audience, so they
// are not shared verbatim.

export interface MCPToolParam {
  arg: string
  required?: boolean
  desc: string
}

export interface MCPToolInfo {
  name: string
  write?: boolean
  desc: string
  params: MCPToolParam[]
}

export const MCP_TOOL_CATALOG: MCPToolInfo[] = [
  {
    name: 'get_dashboard',
    desc: 'Cluster or namespace health overview: resource counts, failing pods, unhealthy workloads, recent warning events, and Helm status. Start here before drilling into specific resources.',
    params: [{ arg: 'namespace', desc: 'filter to a specific namespace' }],
  },
  {
    name: 'top_resources',
    desc: 'Live CPU/memory ranking (like `kubectl top`) joined with Kubernetes context — pod status, restarts, owner workload, requests, and limits. Ranks pods, workloads, or nodes.',
    params: [
      { arg: 'kind', desc: 'pods (default), workloads, or nodes' },
      { arg: 'namespace', desc: 'filter pods/workloads to a namespace' },
      { arg: 'sort', desc: 'cpu (default) or memory' },
      { arg: 'limit', desc: 'max rows (default 20, max 100)' },
    ],
  },
  {
    name: 'list_resources',
    desc: 'List Kubernetes resources of a given kind with compact summaries plus per-row health, managedBy, and issue counts. Supports built-in kinds and CRDs.',
    params: [
      { arg: 'kind', required: true, desc: 'resource kind, e.g. pods, deployments, services' },
      { arg: 'group', desc: 'API group when the kind is ambiguous (e.g. serving.knative.dev)' },
      { arg: 'namespace', desc: 'filter to a specific namespace' },
      { arg: 'context', desc: 'per-row context: default (summaryContext) or none' },
    ],
  },
  {
    name: 'get_resource',
    desc: 'A single resource: minified spec/status/metadata plus resourceContext (relationships, refs, issue/audit/policy rollups). Optionally include heavier event/metrics/change/revision data.',
    params: [
      { arg: 'kind', required: true, desc: 'resource kind, e.g. pod, deployment, service' },
      { arg: 'name', required: true, desc: 'resource name' },
      { arg: 'namespace', desc: 'omit for cluster-scoped kinds (Node, ClusterRole, IngressClass, etc.)' },
      { arg: 'group', desc: 'API group when the kind is ambiguous (e.g. serving.knative.dev for Knative Service vs core Service)' },
      { arg: 'include', desc: 'events, metrics, changes, revisions (rollback targets for Deployment/StatefulSet/DaemonSet/Rollout)' },
      { arg: 'context', desc: 'resourceContext tier: basic (default) or none' },
    ],
  },
  {
    name: 'get_topology',
    desc: 'Topology graph of relationships between resources — Services, workloads, Pods, Ingresses, owners. Use traffic view for network flow or resources view for ownership hierarchy.',
    params: [
      { arg: 'namespace', desc: 'filter to a specific namespace' },
      { arg: 'view', desc: 'traffic or resources' },
      { arg: 'format', desc: 'graph (default) or summary (text)' },
    ],
  },
  {
    name: 'get_neighborhood',
    desc: 'BFS-expanded topology around one resource — its upstream/downstream Services, workloads, Pods, refs, and owners. Cheaper and more focused than full topology once you have a suspect.',
    params: [
      { arg: 'kind', required: true, desc: 'resource kind, e.g. pod, deployment, service, application' },
      { arg: 'name', required: true, desc: 'resource name' },
      { arg: 'namespace', desc: 'resource namespace; omit for cluster-scoped kinds' },
      { arg: 'group', desc: 'API group to disambiguate colliding kinds' },
      { arg: 'profile', desc: 'auto (default) or all (every edge type, heavier)' },
      { arg: 'hops', desc: 'BFS depth (default 1, max 2)' },
      { arg: 'max_nodes', desc: 'node budget (default 25)' },
    ],
  },
  {
    name: 'get_events',
    desc: 'Recent Kubernetes warning events, deduplicated and sorted by recency. Shows reason, message, and occurrence count. Filter to a specific resource by kind/name.',
    params: [
      { arg: 'namespace', desc: 'filter to a specific namespace' },
      { arg: 'limit', desc: 'max events (default 20, max 100)' },
      { arg: 'kind', desc: 'filter to events for this resource kind' },
      { arg: 'name', desc: 'filter to events for this resource name' },
    ],
  },
  {
    name: 'get_pod_logs',
    desc: 'Pod logs with secret redaction. Without grep, prioritizes errors and warnings and falls back to recent tail lines; grep returns only regex matches instead.',
    params: [
      { arg: 'namespace', required: true, desc: 'pod namespace' },
      { arg: 'name', required: true, desc: 'pod name' },
      { arg: 'container', desc: 'container name (defaults to first)' },
      { arg: 'tail_lines', desc: 'lines from end (default 200)' },
      { arg: 'grep', desc: 'regex matches to return instead of diagnostic auto-filtering' },
      { arg: 'since', desc: 'only logs newer than this duration (e.g. 30s, 10m, 1h)' },
      { arg: 'previous', desc: 'logs from the previous terminated container (CrashLoopBackOff)' },
    ],
  },
  {
    name: 'diagnose',
    desc: 'Bounded, point-in-time evidence bundle for one narrowed target — not an agent run and not an authoritative root-cause verdict. For workloads, including Argo Rollout, Radar attempts selected, capped current and previous logs where available, alongside resource context, a capped warning-event sample, and startup blockers; GitOps reconcilers, including Flux HelmRelease, get status summary + parsed related issues; network entry kinds (Service / Ingress / HTTPRoute / GRPCRoute / Gateway) get a coverage-honest path trace that identifies a broken hop only when the evidence establishes one, with an optional one-shot reachability test.',
    params: [
      { arg: 'kind', required: true, desc: 'pod, deployment, statefulset, daemonset, Argo Rollout, application, kustomization, Flux HelmRelease, service, ingress, httproute, grpcroute, or gateway' },
      { arg: 'group', desc: 'API group for CRDs or kind collisions (for example argoproj.io for Rollout); built-ins are inferred' },
      { arg: 'namespace', required: true, desc: 'resource namespace' },
      { arg: 'name', required: true, desc: 'resource name' },
      { arg: 'probe', desc: 'network kinds only: add active DNS/TCP/TLS/HTTP probes against the declared path (0-3s wall time)' },
      { arg: 'in_cluster', desc: 'network kinds: run the probe from inside the cluster via short-lived self-destructing pods (real dataplane) - confirms a route the apiserver proxy could only reach indirectly. Creates up to 5 short-lived probe pods (one per dialed target) under your RBAC' },
      { arg: 'container', desc: 'workload kinds: specific container (defaults to all)' },
      { arg: 'tail_lines', desc: 'workload kinds: lines per pod/stream (default 100)' },
      { arg: 'since', desc: 'workload kinds: only logs newer than this duration' },
    ],
  },
  {
    name: 'list_namespaces',
    desc: 'List all Kubernetes namespaces with their status. Use to discover available namespaces before filtering other queries.',
    params: [],
  },
  {
    name: 'get_changes',
    desc: 'Recent meaningful changes from the Kubernetes timeline plus native Helm deployment history (source=helm). Includes failed upgrades, rollbacks, and current Helm revisions; sourcesErrored marks partial source failures.',
    params: [
      { arg: 'namespace', desc: 'filter to a specific namespace' },
      { arg: 'kind', desc: 'filter to a resource kind (e.g. Deployment)' },
      { arg: 'name', desc: 'filter to a specific resource name' },
      { arg: 'since', desc: 'lookback duration, e.g. 1h, 30m (default 1h)' },
      { arg: 'limit', desc: 'max changes (default 20, max 50)' },
    ],
  },
  {
    name: 'get_cluster_audit',
    desc: 'Best-practice and security posture findings — Security, Reliability, Efficiency — each with remediation guidance. Static config posture, independent of live operational health.',
    params: [
      { arg: 'namespace', desc: 'filter to a specific namespace' },
      { arg: 'category', desc: 'Security, Reliability, or Efficiency' },
      { arg: 'severity', desc: 'posture priority: critical, high, medium, or low (built-ins use high or medium)' },
      { arg: 'limit', desc: 'max findings (default 30, max 100)' },
    ],
  },
  {
    name: 'get_cluster_upgrade_readiness',
    desc: 'Upgrade impact analysis for a target Kubernetes minor: the evidenced check catalog (version skew, removed/deprecated APIs, node runtime, drain feasibility, webhook readiness) with per-check coverage and caveats, expandable into findings with evidence and remediation. The first call runs a live scan; results are briefly cached per caller.',
    params: [
      { arg: 'target', desc: 'target Kubernetes minor (default: next minor above current)' },
      { arg: 'check', desc: 'check id to expand into findings' },
      { arg: 'level', desc: 'filter expanded findings: blocker, warning, or review' },
      { arg: 'offset', desc: 'page through findings beyond the per-call cap' },
      { arg: 'scan_id', desc: 'binds paging to one scan snapshot (required with offset)' },
      { arg: 'refresh', desc: 'bypass the cached scan after changing something' },
    ],
  },
  {
    name: 'list_helm_releases',
    desc: 'All Helm releases with status, resource health, storage namespace, Flux ownership, current lastOperation, and capped operation trails for failed upgrades, rollbacks, or stuck pending operations.',
    params: [{ arg: 'namespace', desc: 'filter to a specific namespace' }],
  },
  {
    name: 'get_helm_release',
    desc: 'Detailed Helm release info with owned resources, health, Flux ownership, current lastOperation, operationInsight, hooks, and failed/running hook diagnostics with live Job/Pod/Event/redacted-log evidence when available.',
    params: [
      { arg: 'namespace', required: true, desc: 'Helm storage namespace; use storageNamespace from list_helm_releases when present' },
      { arg: 'name', required: true, desc: 'release name' },
      { arg: 'include', desc: 'values, history, operations, diff, values_diff, notes_diff, resource_diff' },
      { arg: 'diff_revision_1', desc: 'first revision for any diff include' },
      { arg: 'diff_revision_2', desc: 'second revision for any diff include (defaults to current)' },
    ],
  },
  {
    name: 'list_packages',
    desc: 'Unified "what\'s installed" view across Helm releases, workload labels, CRD registrations, and GitOps declarations (Argo + Flux), with sources, versions, health, and a sourceLegend for the stable source codes.',
    params: [
      { arg: 'namespace', desc: 'filter to a specific namespace' },
      { arg: 'source', desc: 'H/helm, L/labels, C/crds, A/argocd, F/fluxcd' },
      { arg: 'chart', desc: 'case-insensitive chart-name substring' },
    ],
  },
  {
    name: 'issues',
    desc: 'Ranked list of what is broken right now — failing workloads, active native Helm release failures or stuck pending operations, dangling references, scheduling blockers, and false CRD conditions. Native Helm rows use group=helm.sh.',
    params: [
      { arg: 'namespace', desc: 'filter to one namespace' },
      { arg: 'severity', desc: 'comma-separated: critical, warning' },
      { arg: 'kind', desc: 'comma-separated kind filter' },
      { arg: 'limit', desc: 'max issues (default 200, max 1000)' },
      { arg: 'filter', desc: 'optional CEL boolean expression' },
    ],
  },
  {
    name: 'search',
    desc: 'Find resources by content/term match — config keys, env refs, images, label values, ConfigMap data, CRD fields, status messages. Secret values are never indexed.',
    params: [
      { arg: 'query', required: true, desc: 'tokens AND\'d; modifiers kind:, ns:, label:, image:' },
      { arg: 'limit', desc: 'max hits (default 50, max 500)' },
      { arg: 'include', desc: 'summary (default), raw, or none' },
      { arg: 'filter', desc: 'optional CEL boolean expression' },
      { arg: 'context', desc: 'per-hit context: default (summaryContext) or none' },
    ],
  },
  {
    name: 'get_subject_permissions',
    desc: 'Effective RBAC for a ServiceAccount, User, or Group. Returns the full bindings/rules dump by default, or an authoritative SubjectAccessReview answer when verb and resource are supplied for a ServiceAccount.',
    params: [
      { arg: 'kind', required: true, desc: 'ServiceAccount, User, or Group' },
      { arg: 'name', required: true, desc: 'subject name' },
      { arg: 'namespace', desc: 'required for ServiceAccount; omit for User/Group' },
      { arg: 'verb', desc: 'access check: Kubernetes API verb; requires resource' },
      { arg: 'resource', desc: 'access check: plural API resource; requires verb' },
      { arg: 'group', desc: 'access check: API group; omit for core/v1' },
      { arg: 'resource_namespace', desc: 'access check target; defaults to subject namespace' },
      { arg: 'subresource', desc: 'access check subresource, e.g. log for pods/log' },
      { arg: 'resource_name', desc: 'access check target resource name' },
    ],
  },
  {
    name: 'query_prometheus',
    desc: "Run PromQL against the cluster's Prometheus (auto-discovered or configured; works with Thanos, VictoriaMetrics, Mimir). Instant queries return current values; range queries return time-series history with automatic step adjustment. Oversized results return a cardinality summary with a suggested topk rewrite instead of raw data.",
    params: [
      { arg: 'query', required: true, desc: 'PromQL query to execute' },
      { arg: 'type', desc: 'instant (default) or range' },
      { arg: 'since', desc: 'range lookback, e.g. 30m, 1h, 24h, 7d (default 1h)' },
      { arg: 'start', desc: 'range RFC3339 start time; overrides since' },
      { arg: 'end', desc: 'range RFC3339 end time (default now)' },
      { arg: 'step', desc: 'range resolution, e.g. 30s, 5m (auto-calculated when omitted)' },
      { arg: 'max_points', desc: 'max data points per series (default 300, max 600)' },
      { arg: 'timeout', desc: 'query timeout in seconds (default 30, max 180)' },
    ],
  },
  {
    name: 'discover_metrics',
    desc: 'Discover exact Prometheus metric names (with type and help text) or values of one label before writing PromQL. Lists active series from the last hour; flags truncation so the selector can be narrowed.',
    params: [
      { arg: 'match', desc: 'PromQL series selector filter, e.g. {__name__=~"node_cpu.*"}; required when label is empty' },
      { arg: 'label', desc: 'discover values of this label instead of metric names, e.g. namespace, pod' },
      { arg: 'limit', desc: 'max values returned (default 100, max 500)' },
    ],
  },
  {
    name: 'get_prometheus_rules',
    desc: 'List Prometheus alerting and recording rules with their PromQL definitions, state (firing/pending/inactive), and active alert instances. The starting point for alert investigation: fetch the rule, then query its expression.',
    params: [
      { arg: 'type', desc: 'alert or record (omit for both)' },
      { arg: 'name', desc: 'substring filter on rule name' },
      { arg: 'group', desc: 'substring filter on rule group name' },
      { arg: 'state', desc: 'alerting rules only: firing, pending, or inactive' },
      { arg: 'limit', desc: 'max rules returned (default 50, max 200)' },
    ],
  },
  {
    name: 'get_workload_logs',
    desc: 'Aggregated logs across all pods of a workload. Without grep, filters for diagnostic relevance; grep returns only matching timestamp-prefixed lines instead.',
    params: [
      { arg: 'kind', desc: 'deployment (default), statefulset, daemonset, job, or workflow' },
      { arg: 'namespace', required: true, desc: 'workload namespace' },
      { arg: 'name', required: true, desc: 'workload name' },
      { arg: 'container', desc: 'specific container (defaults to all)' },
      { arg: 'tail_lines', desc: 'lines per pod (default 100)' },
      { arg: 'grep', desc: 'regex matches to return instead of diagnostic auto-filtering' },
      { arg: 'since', desc: 'only logs newer than this duration' },
      { arg: 'previous', desc: 'logs from the previous terminated container' },
    ],
  },
  {
    name: 'manage_workload',
    write: true,
    desc: 'Operate on a workload: restart triggers a rolling restart, scale changes the replica count, rollback reverts to a previous revision. Rollout rollback re-runs every canary step — pair it with manage_rollout promote-full, or abort instead.',
    params: [
      { arg: 'action', required: true, desc: 'restart, scale, or rollback' },
      { arg: 'kind', required: true, desc: 'deployment, statefulset, daemonset, or rollout' },
      { arg: 'namespace', required: true, desc: 'workload namespace' },
      { arg: 'name', required: true, desc: 'workload name' },
      { arg: 'replicas', desc: 'target replica count (for scale)' },
      { arg: 'revision', desc: 'target revision (for rollback); list them with get_resource include=revisions' },
    ],
  },
  {
    name: 'manage_rollout',
    write: true,
    desc: 'Control an Argo Rollout progressive delivery: abort reverts traffic to the last stable version immediately, retry clears an abort, promote advances one step, promote-full skips all remaining steps/pauses/analysis, skip-step advances exactly one canary step. A Rollout paused on an inconclusive analysis names its AnalysisRun in status — read that first.',
    params: [
      { arg: 'action', required: true, desc: 'abort, retry, promote, promote-full, or skip-step' },
      { arg: 'namespace', required: true, desc: 'rollout namespace' },
      { arg: 'name', required: true, desc: 'rollout name' },
    ],
  },
  {
    name: 'manage_cronjob',
    write: true,
    desc: 'Operate on a CronJob: trigger creates a manual Job run, suspend pauses the schedule, resume re-enables it.',
    params: [
      { arg: 'action', required: true, desc: 'trigger, suspend, or resume' },
      { arg: 'namespace', required: true, desc: 'cronjob namespace' },
      { arg: 'name', required: true, desc: 'cronjob name' },
    ],
  },
  {
    name: 'manage_gitops',
    write: true,
    desc: 'Operate on GitOps resources. ArgoCD: sync, refresh, terminate, rollback, suspend, resume. FluxCD: reconcile, sync-with-source, suspend, resume.',
    params: [
      { arg: 'action', required: true, desc: 'sync/reconcile, refresh, terminate, rollback, suspend, or resume' },
      { arg: 'tool', required: true, desc: 'argocd or fluxcd' },
      { arg: 'namespace', required: true, desc: 'resource namespace' },
      { arg: 'name', required: true, desc: 'resource name' },
      { arg: 'kind', desc: 'FluxCD resource kind (e.g. kustomization, helmrelease)' },
    ],
  },
  {
    name: 'apply_resource',
    write: true,
    desc: 'Create or update a resource from YAML. apply mode is server-side apply and reports field ownership conflicts by default; create mode fails if it exists. Multi-document YAML returns per-document status on partial failure.',
    params: [
      { arg: 'yaml', required: true, desc: 'YAML manifest (multi-document with --- supported)' },
      { arg: 'mode', desc: 'apply (default) or create' },
      { arg: 'dry_run', desc: 'validate and preview without persisting' },
      { arg: 'namespace', desc: 'override namespace for the resource' },
      { arg: 'verify', desc: 'return post-mutation state; on dry_run return preview diff (default true)' },
      { arg: 'force', desc: 'force SSA field ownership conflicts and take ownership from other managers (default false)' },
    ],
  },
  {
    name: 'patch_resource',
    write: true,
    desc: 'Patch one existing resource with JSON Patch, JSON Merge Patch, or built-in-kind strategic merge patch for precise edits without rewriting the full manifest.',
    params: [
      { arg: 'kind', required: true, desc: 'resource kind, e.g. Deployment, Service, ConfigMap' },
      { arg: 'name', required: true, desc: 'resource name' },
      { arg: 'namespace', desc: 'resource namespace; omit for cluster-scoped resources' },
      { arg: 'group', desc: 'API group when the kind is ambiguous' },
      { arg: 'patch_type', desc: 'json (default), merge, or strategic' },
      { arg: 'patch', required: true, desc: 'JSON patch body' },
      { arg: 'dry_run', desc: 'validate and preview without persisting' },
      { arg: 'verify', desc: 'return post-patch state; on dry_run return preview diff (default true)' },
    ],
  },
  {
    name: 'manage_node',
    write: true,
    desc: 'Operate on a node: cordon marks it unschedulable, uncordon reverses that, drain cordons then evicts all non-DaemonSet pods.',
    params: [
      { arg: 'action', required: true, desc: 'cordon, uncordon, or drain' },
      { arg: 'name', required: true, desc: 'node name' },
      { arg: 'delete_empty_dir_data', desc: 'evict pods with emptyDir volumes (default true)' },
      { arg: 'force', desc: 'evict pods not managed by a controller (default false)' },
      { arg: 'timeout', desc: 'drain timeout in seconds (default 60)' },
    ],
  },
]
