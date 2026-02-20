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
- **Read-only** — all MCP tools are read-only by design, safer than giving agents kubectl write access
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

| Tool | Description | Parameters |
|------|-------------|------------|
| `get_dashboard` | Cluster health overview — resource counts, problems, warning events, Helm status | `namespace` (optional) |
| `list_resources` | List resources of a kind with minified summaries (pods, deployments, services, CRDs, etc.) | `kind` (required), `namespace` (optional) |
| `get_resource` | Detailed view of a single resource — minified spec, status, metadata | `kind` (required), `namespace` (required), `name` (required) |
| `get_topology` | Topology graph showing resource relationships (nodes and edges) | `namespace` (optional), `view` (optional: `traffic` or `resources`) |
| `get_events` | Recent warning events, deduplicated and sorted by recency | `namespace` (optional), `limit` (optional, default 20) |
| `get_pod_logs` | Filtered pod logs prioritizing errors/warnings, with secret redaction | `namespace` (required), `name` (required), `container` (optional), `tail_lines` (optional) |
| `list_namespaces` | List all namespaces with status | (none) |

## Available Resources

| URI | Description |
|-----|-------------|
| `cluster://health` | Cluster health summary (same data as `get_dashboard`) |
| `cluster://topology` | Full cluster topology graph |
| `cluster://events` | Recent warning events (up to 50) |

## Security

- **Read-only** — all tools and resources are read-only; no cluster modifications
- **Local-only** — MCP server runs on localhost alongside Radar
- **RBAC-aware** — respects your kubeconfig's RBAC permissions; returns 403 for unauthorized resources
- **Secret redaction** — Secret `.data` and `.stringData` are never exposed; only key names are shown
- **Value redaction** — environment variable values are scrubbed for known secret patterns (API keys, tokens, passwords, base64 blocks)
- **Log redaction** — pod log output is scrubbed for secret patterns before being returned
