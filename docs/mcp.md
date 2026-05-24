# AI Integration (MCP)

> **Beta** — This feature is new and may evolve. Feedback welcome via [GitHub Issues](https://github.com/skyhook-io/radar/issues).

Radar includes a built-in [Model Context Protocol](https://modelcontextprotocol.io) (MCP) server that lets AI assistants query your Kubernetes cluster.

## Why MCP instead of raw kubectl?

Giving an AI assistant raw `kubectl` access has problems:

- **Token waste** — `kubectl get pod -o yaml` returns verbose YAML full of managed fields, status conditions, and metadata noise that burns through LLM context windows
- **No enrichment** — raw output lacks topology relationships, health assessments, or cross-resource correlation
- **Write access risk** — kubectl can modify and delete resources

Radar's MCP server solves these:

- **Token-optimized** — resources are minified, stripping noise (managed fields, internal annotations, redundant status) while preserving what matters
- **Enriched data** — topology graphs, health assessments, deduplicated events, filtered logs (prioritizing errors/warnings)
- **Safe operations** — read tools are read-only; write tools (restart, scale, sync) are clearly annotated and non-destructive
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
| `get_dashboard` | Cluster health overview — resource counts, problems, warning events, Helm status. Includes recent changes correlated with detected problems. | `namespace` (optional) |
| `list_resources` | List resources of a kind with minified summaries (pods, deployments, services, CRDs, etc.) | `kind` (required), `namespace` (optional) |
| `get_resource` | Detailed view of a single resource — minified spec + status + metadata + default-on `resourceContext` (managedBy / exposes / selectedBy / uses / runsOn / issue+audit rollups). Optionally include heavier sidecars (events / metrics / logs). | `kind` (required), `namespace` (optional — omit for cluster-scoped kinds: Node, ClusterRole, IngressClass, etc.), `name` (required), `group` (optional, for ambiguous kinds), `include` (optional: `events,metrics,logs`), `context` (optional: `basic` default, `none` for bare minified output) |
| `get_topology` | Topology graph showing resource relationships (nodes and edges). Use `summary` format for LLM-friendly text descriptions of resource chains. | `namespace` (optional), `view` (optional: `traffic` or `resources`), `format` (optional: `graph` or `summary`) |
| `get_events` | Recent Kubernetes events, deduplicated and sorted by recency. Filter by resource kind/name to scope to a specific resource. | `namespace` (optional), `limit` (optional, default 20, max 100), `kind` (optional), `name` (optional) |
| `get_pod_logs` | Filtered pod logs prioritizing errors/warnings, with secret redaction | `namespace` (required), `name` (required), `container` (optional), `tail_lines` (optional, default 200) |
| `list_namespaces` | List all namespaces with status | (none) |
| `get_changes` | Recent resource changes (creates, updates, deletes) from the cluster timeline. Use to investigate what changed before an incident. | `namespace` (optional), `kind` (optional), `name` (optional), `since` (optional, e.g. `1h`, `30m`; default `1h`), `limit` (optional, default 20, max 50) |
| `list_helm_releases` | List all Helm releases with status and health | `namespace` (optional) |
| `get_helm_release` | Detailed Helm release info with optional values, history, and manifest diff | `namespace` (required), `name` (required), `include` (optional: `values,history,diff`), `diff_revision_1` (required when `include=diff`) / `diff_revision_2` (optional) |
| `get_workload_logs` | Aggregated, AI-filtered logs from all pods of a workload (Deployment, StatefulSet, DaemonSet) | `kind` (required), `namespace` (required), `name` (required), `container` (optional), `tail_lines` (optional, default 100 per pod) |
| `get_subject_permissions` | Effective RBAC permissions of a ServiceAccount / User / Group: bindings (each with `inheritedFromGroup` set when applicable), deduplicated flat rule list, and (for SAs) the Pods running as it. Use to answer "is this SA over-privileged?" or "what's the blast radius if this Pod is compromised?" | `kind` (required: `ServiceAccount`, `User`, or `Group`), `namespace` (required for ServiceAccount; omit for User/Group), `name` (required) |

### Write Tools

| Tool | Description | Parameters |
|------|-------------|------------|
| `apply_resource` | Create or update a Kubernetes resource from YAML. Supports multi-document YAML and server-side dry run. | `yaml` (required), `mode` (optional: `apply` or `create`, default `apply`), `dry_run` (optional, default false), `namespace` (optional, override) |
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

- **Safe by design** — read tools are strictly read-only; write tools perform non-destructive operations (restart, scale, sync) and are annotated with MCP tool hints so AI clients can distinguish them
- **RBAC-aware** — every call enforces RBAC at the same boundary as the REST API:
  - **Local binary**: the cache uses your kubeconfig identity, so MCP can only see what `kubectl` can see for that user
  - **In-cluster (auth enabled)**: read tools intersect namespaced reads with the calling user's RBAC-allowed namespaces; cluster-scoped reads (Nodes, PVs, ClusterRoles, cluster-scoped CRDs) are gated per-kind via SubjectAccessReview, so cluster-wide pod visibility doesn't implicitly grant Node read; write tools, exec, and logs are fully impersonated so the apiserver enforces the user's RBAC end-to-end
  - **In-cluster (no auth)**: every MCP caller shares the pod ServiceAccount's view — only deploy this way when MCP isn't exposed beyond a trusted boundary
- **Secret redaction** — Secret `.data` and `.stringData` are never exposed; only key names are shown
- **Value redaction** — environment variable values are scrubbed for known secret patterns (API keys, tokens, passwords, base64 blocks)
- **Log redaction** — pod log output is scrubbed for secret patterns before being returned
