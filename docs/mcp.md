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
| `get_dashboard` | Cluster health overview — resource counts, problems, warning events, Helm status | `namespace` (optional) |
| `list_resources` | List resources of a kind with minified summaries (pods, deployments, services, CRDs, etc.) | `kind` (required), `namespace` (optional) |
| `get_resource` | Detailed view of a single resource — minified spec, status, metadata | `kind` (required), `namespace` (required), `name` (required) |
| `get_topology` | Topology graph showing resource relationships (nodes and edges) | `namespace` (optional), `view` (optional: `traffic` or `resources`) |
| `get_events` | Recent warning events, deduplicated and sorted by recency | `namespace` (optional), `limit` (optional, default 20) |
| `get_pod_logs` | Filtered pod logs prioritizing errors/warnings, with secret redaction | `namespace` (required), `name` (required), `container` (optional), `tail_lines` (optional) |
| `list_namespaces` | List all namespaces with status | (none) |
| `list_helm_releases` | List all Helm releases with status and health | `namespace` (optional) |
| `get_helm_release` | Detailed Helm release info with optional values, history, and manifest diff | `namespace` (required), `name` (required), `include` (optional: `values,history,diff`), `diff_revision_1` / `diff_revision_2` (optional) |
| `get_workload_logs` | Aggregated, AI-filtered logs from all pods of a workload (Deployment, StatefulSet, DaemonSet) | `kind` (required), `namespace` (required), `name` (required), `container` (optional), `tail_lines` (optional) |

### Write Tools

| Tool | Description | Parameters |
|------|-------------|------------|
| `manage_workload` | Restart, scale, or rollback a Deployment, StatefulSet, or DaemonSet | `action` (required: `restart`, `scale`, `rollback`), `kind` (required), `namespace` (required), `name` (required), `replicas` (for scale), `revision` (for rollback) |
| `manage_cronjob` | Trigger, suspend, or resume a CronJob | `action` (required: `trigger`, `suspend`, `resume`), `namespace` (required), `name` (required) |
| `manage_gitops` | Manage ArgoCD and FluxCD resources — sync, reconcile, suspend, resume | `action` (required), `tool` (required: `argocd` or `fluxcd`), `namespace` (required), `name` (required), `kind` (FluxCD only) |

## Available Resources

| URI | Description |
|-----|-------------|
| `cluster://health` | Cluster health summary (same data as `get_dashboard`) |
| `cluster://topology` | Full cluster topology graph |
| `cluster://events` | Recent warning events (up to 50) |

## Security

- **Safe by design** — read tools are strictly read-only; write tools perform non-destructive operations (restart, scale, sync) and are annotated with MCP tool hints so AI clients can distinguish them
- **Local-only** — MCP server runs on localhost alongside Radar
- **RBAC-aware** — respects your kubeconfig's RBAC permissions; returns 403 for unauthorized resources
- **Secret redaction** — Secret `.data` and `.stringData` are never exposed; only key names are shown
- **Value redaction** — environment variable values are scrubbed for known secret patterns (API keys, tokens, passwords, base64 blocks)
- **Log redaction** — pod log output is scrubbed for secret patterns before being returned
