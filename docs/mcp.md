# AI Integration (MCP)

Radar includes a built-in [Model Context Protocol](https://modelcontextprotocol.io) (MCP) server that lets AI agents query your Kubernetes cluster.

## Why MCP instead of raw kubectl?

Giving an AI agent raw `kubectl` access has problems:

- **Token waste** — `kubectl get pod -o yaml` returns verbose YAML full of managed fields, status conditions, and metadata noise that burns through LLM context windows
- **No enrichment** — raw output lacks topology relationships, health assessments, or cross-resource correlation
- **Write access risk** — kubectl can modify and delete resources

Radar's MCP server solves these:

- **Token-optimized** — resources are minified, stripping noise (managed fields, internal annotations, redundant status) while preserving what matters
- **Enriched data** — topology graphs, health assessments, deduplicated events, filtered logs (prioritizing errors/warnings)
- **Safe operations** — read tools are read-only (`readOnlyHint`); write tools (restart, scale, rollback, sync, apply, cordon/drain) are RBAC-enforced and annotated `destructiveHint` so AI clients can prompt for confirmation
- **Secret-safe** — Secret data is never exposed, environment values are redacted, log output is scrubbed for API keys and tokens
- **RBAC-aware** — respects your cluster's RBAC permissions
- **Vendor-neutral** — works with any MCP-compatible AI tool

## Enabling / Disabling

The MCP server is **enabled by default** when Radar starts. To disable it:

```bash
radar --no-mcp
```

## MCP Endpoint

```
http://localhost:9280/mcp
```

The port matches your `--port` flag (default 9280). The MCP server uses HTTP transport with JSON-RPC.

## Catalog Introspection

MCP registries and inspectors can start Radar without a Kubernetes cluster when they only need the tool and resource catalog:

```bash
radar --mcp-catalog-only --no-browser
```

This mode skips Kubernetes initialization and starts the `/mcp` endpoint for schema introspection. Cluster-backed tool calls still require a normal Radar process connected to Kubernetes.

For registries that launch MCP servers over stdio, use:

```bash
radar --mcp-catalog-stdio
```

This exposes the same tool and resource catalog over stdio without starting the HTTP UI server.
The stdio mode is intended only for catalog introspection; normal Radar sessions use the HTTP endpoint above.

## Setup Instructions

Connect your AI tool to Radar's MCP server. Radar must be running first (`radar` or `kubectl radar`).

### Claude Code

Run this command:

```bash
claude mcp add radar --transport http http://localhost:9280/mcp
```

### Claude Desktop

Add to `~/Library/Application Support/Claude/claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "radar": {
      "type": "http",
      "url": "http://localhost:9280/mcp"
    }
  }
}
```

### Cursor

Add to `~/.cursor/mcp.json`:

```json
{
  "mcpServers": {
    "radar": {
      "url": "http://localhost:9280/mcp"
    }
  }
}
```

### Windsurf

Add to `~/.codeium/windsurf/mcp_config.json`:

```json
{
  "mcpServers": {
    "radar": {
      "serverUrl": "http://localhost:9280/mcp"
    }
  }
}
```

### VS Code Copilot

Add to `.vscode/mcp.json` in your workspace:

```json
{
  "servers": {
    "radar": {
      "type": "http",
      "url": "http://localhost:9280/mcp"
    }
  }
}
```

### Cline

Add via the Cline MCP settings UI:

```json
{
  "mcpServers": {
    "radar": {
      "url": "http://localhost:9280/mcp",
      "type": "streamableHttp"
    }
  }
}
```

### JetBrains AI

Add via **Settings > Tools > AI Assistant > MCP**:

```json
{
  "mcpServers": {
    "radar": {
      "url": "http://localhost:9280/mcp"
    }
  }
}
```

### OpenAI Codex

Add to `~/.codex/config.toml`:

```toml
[mcp_servers.radar]
url = "http://localhost:9280/mcp"
```

### Gemini CLI

Add to `~/.gemini/settings.json`:

```json
{
  "mcpServers": {
    "radar": {
      "httpUrl": "http://localhost:9280/mcp"
    }
  }
}
```

## Available Tools

### Read Tools

| Tool | Description | Parameters |
|------|-------------|------------|
| `issues` | "What's broken right now?" — a ranked, curated stream of live operational failures: failing workloads/pods, active native Helm release failures or stuck pending operations (`kind=HelmRelease`, `group=helm.sh`), dangling references, pod-startup blockers (unschedulable / admission-rejected / stuck post-bind), and False CRD conditions. No source filter; each row carries a `source` label sliceable via `filter`. Recovered Helm rollbacks are deploy history, not live issues; use `get_changes` for Helm deployment history and `get_helm_release` for native Helm full revision/history/hook diagnostics. Flux `HelmRelease` rows (`group=helm.toolkit.fluxcd.io`) are GitOps reconcilers and should use `diagnose`. For static posture use `get_cluster_audit`; for raw events use `get_events`. | `namespace` (optional), `severity` (optional: `critical,warning`), `kind` (optional), `filter` (optional CEL), `limit` (optional, default 200, max 1000) |
| `diagnose` | Root-cause one workload or GitOps reconciler in a single call. Pod/Deployment/StatefulSet/DaemonSet get minified resource + `resourceContext` + current AND previous container logs across pods + filtered events + `startupBlockers`; Application/Kustomization/Flux HelmRelease get reconciler status + related parsed issues. | `kind` (required: workload or GitOps reconciler), `namespace` (required), `name` (required) |
| `get_dashboard` | Cluster/namespace health overview — resource counts, failing pods, unhealthy workloads, warning events, Helm status. Inventory-style triage before drilling in. | `namespace` (optional) |
| `top_resources` | Live metrics ranked like `kubectl top | sort`, joined with K8s context (status, restarts, owner, requests/limits). Use for CPU/memory/OOM/load symptoms. | `kind` (optional: `pods` default, `workloads`, `nodes`), `namespace` (optional), `sort` (optional: `cpu` default, `memory`), `limit` (optional, default 20, max 100) |
| `list_resources` | List resources of a kind with minified summaries + per-row `summaryContext` (managedBy / health / issueCount). | `kind` (required), `group` (optional), `namespace` (optional), `context` (optional: default / `none`) |
| `search` | Find resources by content/term match (config keys, env refs, images, label values, CRD fields, status messages). Tokens AND'd; secret values never indexed. Supports `kind:`/`ns:`/`label:`/`image:` modifiers and CEL `filter`. | `query` (required), `filter` (optional CEL), `limit` (optional) |
| `get_resource` | Detailed view of a single resource — minified spec + status + metadata + default-on `resourceContext` (managedBy / exposes / selectedBy / uses / runsOn / issue+audit rollups). Optionally include heavier supplemental data (events / metrics). For logs use `get_pod_logs` / `get_workload_logs` / `diagnose`. | `kind` (required), `namespace` (optional — omit for cluster-scoped kinds: Node, ClusterRole, IngressClass, etc.), `name` (required), `group` (optional, for ambiguous kinds), `include` (optional: `events,metrics`), `context` (optional: `basic` default, `none` for bare minified output) |
| `get_topology` | Whole-namespace/cluster topology graph (nodes + edges). Use `summary` format for LLM-friendly text chains. Once you have a suspect root, prefer `get_neighborhood`. | `namespace` (optional), `view` (optional: `traffic` or `resources`), `format` (optional: `graph` or `summary`) |
| `get_neighborhood` | BFS-expanded topology neighborhood around one known root — cheaper and clearer than `get_topology` for cross-resource failures (routing, selector/endpoint, refs, owner chains). RBAC-filtered. | `kind` (required), `namespace` (optional), `name` (required), `profile` (optional: `auto` default / `all`), `hops` (optional, default 1, max 2) |
| `get_events` | Recent Kubernetes Warning events, deduplicated and sorted by recency. Filter by resource kind/name to scope. | `namespace` (optional), `limit` (optional, default 20, max 100), `kind` (optional), `name` (optional) |
| `get_changes` | Recent meaningful changes from the Kubernetes cluster timeline plus native Helm release deployment/operation history (`source: helm`). Use to investigate what changed before an incident, including failed upgrades, rollbacks, and current Helm revisions. If the response includes `sourcesErrored`, treat it as partial data for those sources. Use `get_helm_release include=history,operations` for the full Helm revision trail. | `namespace` (optional), `kind` (optional), `name` (optional), `since` (optional, e.g. `1h`, `30m`; default `1h`), `limit` (optional, default 20, max 50) |
| `get_pod_logs` | Filtered pod logs prioritizing errors/warnings, with secret redaction. Set `grep` for server-side filtering. | `namespace` (required), `name` (required), `container` (optional), `tail_lines` (optional, default 200), `grep` (optional) |
| `get_workload_logs` | Aggregated, AI-filtered logs from all pods of a workload (Deployment, StatefulSet, DaemonSet, Job, Argo Workflow) | `kind` (required), `namespace` (required), `name` (required), `container` (optional), `tail_lines` (optional, default 100 per pod), `grep` (optional) |
| `get_cluster_audit` | Static config posture — best-practice findings (Security / Reliability / Efficiency) with remediation. INDEPENDENT of operational health; for "what's broken right now?" use `issues`. | `namespace` (optional), `category` (optional), `severity` (optional) |
| `list_packages` | Installed packages (Helm releases, label-managed workloads, CRDs, Argo Applications, Flux HelmReleases + Kustomizations) with source provenance, versions, and health, in one call. Response includes `sourceLegend` for the stable source codes. | `namespace` (optional), `source` (optional: `H`/`helm`, `L`/`labels`, `C`/`crds`, `A`/`argocd`, `F`/`fluxcd`), `chart` (optional substring) |
| `list_helm_releases` | List Helm releases with status, resource health, storage namespace, Flux ownership, current `lastOperation`, and a capped `operations` trail when Helm history indicates failed upgrades, rollback-after-failure, rollbacks, or stuck pending operations. Use this first for Helm deployment debugging. | `namespace` (optional) |
| `get_helm_release` | Detailed Helm release info with owned resources, resource health, Flux ownership, current `lastOperation`, `operationInsight` (active/recovered state, likely resource to inspect, suggested compare), hooks, and failed/running hook diagnostics with live Job/Pod/Event/redacted-log evidence when still available. Use `include=history,operations` for the full Helm revision trail; `include=values` for key-aware redacted user values; `include=diff,values_diff,notes_diff,resource_diff` for revision comparison. For releases with `storageNamespace`, pass that value as `namespace`. | `namespace` (required: Helm storage namespace), `name` (required), `include` (optional: `values,history,operations,diff,values_diff,notes_diff,resource_diff`), `diff_revision_1` (required when `include` contains a diff token) / `diff_revision_2` (optional, defaults to current) |
| `list_namespaces` | List all namespaces with status | (none) |
| `get_subject_permissions` | Effective RBAC permissions of a ServiceAccount / User / Group: bindings (each with `inheritedFromGroup` set when applicable), deduplicated flat rule list, and (for SAs) the Pods running as it. Use to answer "is this SA over-privileged?" or "what's the blast radius if this Pod is compromised?" | `kind` (required: `ServiceAccount`, `User`, or `Group`), `namespace` (required for ServiceAccount; omit for User/Group), `name` (required) |
| `query_prometheus` | Execute PromQL against the cluster's Prometheus (auto-discovered or `--prometheus-url`; works with PromQL-compatible backends: Thanos, VictoriaMetrics, Mimir). `type=instant` returns current values; `type=range` returns time-series history with automatic step adjustment. Oversized results return a label-cardinality summary + suggested `topk` rewrite instead of raw data. | `query` (required), `type` (optional: `instant` default, `range`), `since` (optional, e.g. `30m`, `1h`, `24h`, `7d`; default `1h`), `start` / `end` (optional RFC3339, override `since`), `step` (optional, auto-calculated when omitted), `max_points` (optional, default 300, max 600), `timeout` (optional seconds, default 30, max 180) |
| `discover_metrics` | Discover exact metric names (enriched with type/help from Prometheus metadata) or values of one label before writing PromQL. Lists active series from the last hour; `truncated: true` means narrow the `match` selector. | `match` (PromQL series selector; required when `label` is empty), `label` (optional: list values of this label instead of metric names), `limit` (optional, default 100, max 500) |
| `get_prometheus_rules` | List Prometheus alerting/recording rules with PromQL definitions, state, labels, annotations, and active alert instances. Alert-investigation entry point: fetch the rule definition, then run its query with `query_prometheus`. | `type` (optional: `alert`, `record`), `name` / `group` (optional substring filters), `state` (optional: `firing`, `pending`, `inactive`), `limit` (optional, default 50, max 200) |

### Write Tools

| Tool | Description | Parameters |
|------|-------------|------------|
| `apply_resource` | Create or update a Kubernetes resource from YAML. Supports multi-document YAML, per-document partial-failure results, server-side dry-run preview, and SSA ownership-conflict reporting. | `yaml` (required), `mode` (optional: `apply` or `create`, default `apply`), `dry_run` (optional, default false), `namespace` (optional, override), `verify` (optional, default true: post-mutation state, submitted-vs-live diff, dry-run preview diff, workload rollout/pods, and related issues), `force` (optional, default false: take SSA field ownership from other managers) |
| `patch_resource` | Patch one existing Kubernetes resource with JSON Patch, JSON Merge Patch, or strategic merge patch. Use for precise field/list edits when you know the exact path and do not want to rewrite the full manifest or take broad server-side-apply ownership. Strategic patch is for built-in Kubernetes kinds and name-keyed list edits, such as changing one container. | `kind` (required), `name` (required), `namespace` (required for namespaced resources), `group` (optional), `patch_type` (optional: `json` default, `merge`, or `strategic`), `patch` (required JSON string), `dry_run` (optional), `verify` (optional, default true: compact post-patch state, dry-run preview diff, and JSON Patch field checks) |
| `manage_workload` | Restart, scale, or rollback a Deployment, StatefulSet, or DaemonSet. Note: `scale` is not supported for DaemonSets. | `action` (required: `restart`, `scale`, `rollback`), `kind` (required), `namespace` (required), `name` (required), `replicas` (for scale), `revision` (for rollback) |
| `manage_cronjob` | Trigger, suspend, or resume a CronJob | `action` (required: `trigger`, `suspend`, `resume`), `namespace` (required), `name` (required) |
| `manage_gitops` | Manage ArgoCD and FluxCD resources — sync, refresh, terminate, suspend, resume, rollback (Argo), reconcile (Flux), reconcile-with-source (Flux) | `action` (required), `tool` (required: `argocd` or `fluxcd`), `namespace` (required), `name` (required), `kind` (FluxCD only). For `sync`: `revision`, `prune`, `dryRun`, `force`, `applyOnly`, `syncOptions`. For `rollback` (Argo only): `historyId` (required), `prune`, `dryRun`. Per-action input validation rejects flags that don't apply to the action (e.g. `force` on `suspend`) so callers fail loudly instead of silently. |
| `manage_node` | Cordon, uncordon, or drain a Kubernetes node | `action` (required: `cordon`, `uncordon`, `drain`), `name` (required), `delete_empty_dir_data` (optional, default true), `force` (optional), `timeout` (optional, seconds, default 60) |

## Available Resources

| URI | Description |
|-----|-------------|
| `cluster://health` | Cluster health summary (same data as `get_dashboard`) |
| `cluster://topology` | Full cluster topology graph |
| `cluster://events` | Recent warning events (up to 50) |

## Security

- **Safe by design** — read tools are strictly read-only and annotated with `readOnlyHint`; write tools (restart, scale, rollback, sync, apply, cordon/drain) are RBAC-enforced and annotated with `destructiveHint` so AI clients can prompt for confirmation. Some are genuinely destructive — `apply_resource force=true` can take field ownership from Helm/Flux, `manage_node drain` evicts pods, and `rollback`/`terminate` overwrite or abort desired state
- **RBAC-aware** — every call enforces RBAC at the same boundary as the REST API:
  - **Local binary**: the cache uses your kubeconfig identity, so MCP can only see what `kubectl` can see for that user
  - **In-cluster (auth enabled)**: read tools intersect namespaced reads with the calling user's RBAC-allowed namespaces; cluster-scoped reads (Nodes, PVs, ClusterRoles, cluster-scoped CRDs) are gated per-kind via SubjectAccessReview, so cluster-wide pod visibility doesn't implicitly grant Node read; write tools, exec, and logs are fully impersonated so the apiserver enforces the user's RBAC end-to-end
  - **In-cluster (no auth)**: every MCP caller shares the pod ServiceAccount's view — only deploy this way when MCP isn't exposed beyond a trusted boundary
- **Prometheus metric data is NOT namespace-filtered** — PromQL cannot be namespace-scoped server-side (arbitrary queries can aggregate across namespaces), so `query_prometheus` and `discover_metrics` follow the same stance as the REST `/prometheus/query` endpoint: any authenticated user may run PromQL. Deploy with auth enabled when Prometheus contains sensitive label values
- **Secret redaction** — Secret `.data` and `.stringData` are never exposed; only key names are shown
- **Value redaction** — environment variable values and Helm values returned through MCP are scrubbed for known secret patterns; Helm values also use key-aware redaction for names like `password`, `token`, `privateKey`, and `secretKey`
- **Log redaction** — pod log output and Helm hook log evidence are scrubbed for secret patterns before being returned
