# AI Integration (MCP)

Radar includes a built-in [Model Context Protocol](https://modelcontextprotocol.io) (MCP) server that lets AI agents inspect, investigate, and operate your Kubernetes cluster.

## Node drain timeouts

`manage_node` with `action: drain` waits for pod deletion by default. Its default
two-minute deadline covers eviction retries and deletion waiting together. A final
verification read can take up to five seconds more. An explicit `timeout` replaces
the operation deadline; it is not an additional deletion-wait allowance.

Configure the MCP client's tool-call timeout and every reverse proxy in the path
to exceed the selected drain deadline plus five seconds, with room for network
latency. For the default, use at least 150 seconds. Radar's drain route is outside
its normal 60-second HTTP timeout group, but that does not change external client
or proxy limits. See [ingress timeout guidance](in-cluster.md#node-drain-requests-time-out).

If a client or proxy times out, the outcome is unknown to that caller. Evictions
may already have been accepted. Inspect the node's pods before retrying or taking
it down. An accepted eviction is not proof of deletion.

Use `wait_for_deletion: false` only when you want eviction requests started without
waiting for deletion. A `started` result is not completion. `incomplete` and
`unconfirmed` results need attention even when the HTTP request succeeded.

The node page retains this tab's last UI drain result until dismissal, reload or
context switch. It is a recorded result, not live node health or a history of
drains started by other users or MCP clients.

## Why MCP instead of raw kubectl?

Giving an AI agent raw `kubectl` access has problems:

- **Token waste** — `kubectl get pod -o yaml` returns verbose YAML full of managed fields, status conditions, and metadata noise that burns through LLM context windows
- **No enrichment** — raw output lacks topology relationships, health assessments, or cross-resource correlation
- **Write access risk** — kubectl can modify and delete resources

Radar's MCP server solves these:

- **Token-optimized** — resources are minified, stripping noise (managed fields, internal annotations, redundant status) while preserving what matters
- **Enriched data** — topology graphs, health assessments, deduplicated events, filtered logs (prioritizing errors/warnings)
- **Safe operations** — write tools such as restart, scale, apply, and rollback are identified for client confirmation and enforced through Kubernetes RBAC. Diagnosis is read-only by default; optional in-cluster route probing uses short-lived, self-deleting probe pods
- **Secret-safe reads** — resource and log tools redact Secret data and sensitive values. The optional `exec_pod` tool returns raw command output, which can contain credentials
- **RBAC-aware** — respects your cluster's RBAC permissions
- **Vendor-neutral** — works with any MCP-compatible AI tool

## Enabling / Disabling

The MCP server is **enabled by default** when Radar starts. To disable it:

```bash
radar --no-mcp
```

To expose only read-only MCP tools:

```bash
radar --mcp-read-only
```

This is one server-wide mode. `/mcp-readonly` stays available, and Radar's
internal `/mcp-investigation` endpoint keeps its read-only tools and evidence
correlation. The full `/mcp` endpoint returns 404, including direct MCP calls.
Write tools cannot be listed or called on the retained endpoints. Diagnostic
options that create temporary Kubernetes resources (`diagnose`'s `in_cluster`)
are also rejected. Built-in investigations and verification remain available;
their Apply action is unavailable because it requires MCP write tools.
This setting does not restrict writes in the Radar UI or REST API.

CLI and Desktop accept both flags. You can also set
`RADAR_MCP_READ_ONLY=1` or persist `"mcpReadOnly": true` in
`~/.radar/config.json`. The existing `"mcp": false` disables all MCP endpoints.

CLI settings take priority over the environment, which takes priority over the
config file. A higher-priority mode selection replaces a lower-priority one:

| Settings | Effective mode |
| --- | --- |
| No settings | Full MCP, including `/mcp` |
| Config `mcpReadOnly: true`, CLI `--no-mcp` | Disabled |
| Config `mcp: false`, env `RADAR_MCP_READ_ONLY=1` | Read-only |
| Env `RADAR_MCP_READ_ONLY=1`, CLI `--mcp-read-only=false` | Full MCP |
| Config `mcp: false`, CLI `--no-mcp=false` | Full MCP |
| Env `RADAR_MCP_READ_ONLY=1`, CLI `--no-mcp=false` | Read-only |

An explicit false value clears only that setting. Environment booleans use
Go's `strconv.ParseBool` spelling (`1`, `0`, `true`, `false`, `T`, `F`, and
their supported case variants); an empty value is unset and an invalid value
is an error. `--no-mcp --mcp-read-only` is an error. A config file containing
both `"mcp": false` and `"mcpReadOnly": true` is also rejected; correct the
file before starting Radar, even when passing overrides.

Malformed or unreadable config files stop CLI, Desktop and standalone
investigation startup rather than falling back to full MCP. For example,
`"mcpReadOnly": "true"` is invalid: use the JSON boolean `true` without quotes.
The settings API also rejects contradictory MCP settings before saving them.

In read-only mode, use `http://localhost:9280/mcp-readonly` in client
configurations. Radar's setup dialog, settings and interactive investigation
handoffs select this endpoint automatically. Existing client configurations
using `/mcp` must be updated. Startup output and `/api/capabilities`
(`mcpEnabled`, `mcpReadOnly`) describe the effective mode.

## exec_pod

Run one non-interactive argv command in a container, like `kubectl exec`.
Enable with `--mcp-exec-tools` (CLI/Desktop), `"mcpExecTools": true` in
`~/.radar/config.json`, or Helm `mcp.execTools: true`. It defaults off.
`--disable-exec`, `--mcp-read-only` and disabled MCP override the opt-in.
It is destructive, available only on full `/mcp`, and excluded from built-in
Diagnose and Apply. `/api/capabilities.mcpExecTools` reports registration,
not the caller's permission; absent/false means unavailable. Startup logs explain
suppressed opt-in, and `/api/config.effective.mcpExecTools` reports the running state.

Pass `namespace` and `name` as strings and `command` as an argv array. There is
no implicit shell, stdin or TTY. For pipes/variables, explicitly use
`["sh", "-c", "..."]`. The binary (including `sh`) must exist in the image.
For minimal images, use Radar's **Debug container** UI, which requires separate
permission to add an ephemeral container. It does not necessarily share the
application's filesystem or environment. Reading a config file or a new
process's environment does not prove what the running app has loaded.

Name a Pod directly, or set `kind` to Deployment, StatefulSet, DaemonSet, Job,
ReplicaSet or Rollout. Workload targeting requires a Running, Ready,
non-terminating Pod of the **desired revision**, which may be a canary/preview
rather than the revision serving traffic. Job/ReplicaSet use their current
controller incarnation. There is no fallback to an older revision, including
OnDelete/partition-held updates; name a serving Pod explicitly when appropriate.
Reuse the returned `pod` for follow-up commands to avoid selecting another
replica. A named Pod needs a running container, not Pod readiness; this permits
running init containers in Pending Pods.

`container` defaults to the `kubectl.kubernetes.io/default-container` annotation
(an invalid name is an error), then the first non-mesh app container. A valid
annotation or explicit name can select a proxy, init or ephemeral container.
Without either, a proxy-only Pod requires an explicit container name.

Every Kubernetes request uses one snapshot of the caller's credentials
(impersonation or forwarded token); no-auth calls use Radar's kubeconfig or
ServiceAccount. No privileged fallback occurs. Resolution needs reads of the
Pod and, for workloads, its revision evidence. Exec uses WebSocket (`get` on
`pods/exec`) with SPDY fallback (`create`). Kubernetes 1.35's default-on
`AuthorizePodWebsocketUpgradeCreatePermission` gate additionally checks `create`
for WebSocket. Forbidden errors carry `rbac_denied` and permission guidance.
See the [Helm enablement matrix](../deploy/helm/radar/README.md#mcp-container-commands).

One deadline covers lookup and execution: zero/omitted defaults to 30 seconds;
1–45 is accepted, negative/larger values are rejected. Lookup timeout is a tool
error. Execution timeout returns partial output with `timedOut: true`.
Cancellation/broken streams return the same evidence envelope with MCP
`isError: true`; validation/setup/authorization failures return tool errors.
A nonzero command exit is a normal result.

Results are JSON in MCP text content:

| Fields | Meaning |
|---|---|
| `namespace`, `pod`, `container`, `selectedPodUID` | Selected target. UID is the Pod read before execution; exec has no UID precondition, so this does not prove the incarnation that ran. |
| `podSelection` | Workload-only explanation; count includes non-Ready Pods of the selected revision. |
| `stdout`, `stderr` | Separate UTF-8 text, each bounded to 65,536 bytes. Oversized streams retain head and tail with an omitted-middle marker. |
| `stdoutBytes`, `stderrBytes` | Raw bytes received so far, including omitted bytes; not the command's eventual total after timeout. |
| `stdoutTruncated`, `stderrTruncated` | Optional `true` when output was omitted. |
| `stdoutEncodingLoss`, `stderrEncodingLoss` | Optional `true` when retained invalid/incomplete UTF-8 sequences needed replacement with `�`. Encode binary output in-container if needed. |
| `exitCode` | Integer from the executor, or `null` when unknown. Inherited client-go behavior can report zero on a normal WebSocket close without an exit-status payload. |
| `timedOut`, `message` | Deadline/unknown-outcome guidance when applicable. |
| `durationMs` | Lookup plus execution time. |

Runtime error text and 5xx upgrade responses are conservatively treated as unknown
outcomes, even if the command may not have started. A closed stream does not
guarantee process termination. A stalled SPDY fallback
handshake can also outlive the call. Inspect effects before retrying an unknown
outcome; use command-native deadlines for long-running checks.

**Security:** Never put literal secrets in argv: Kubernetes audit can record
the full request URI, including arguments, even at Metadata level. Output is
unredacted, potentially sensitive and untrusted. Commands can change external
state and use mounted credentials. Prefer read-only checks. Opt-in applies to
all clients; Kubernetes controls caller permissions and the MCP host controls
per-call approval. With auth off, reachable clients share Radar's authority.
The namespace cache pin does not constrain named-Pod exec.

Radar's audit/tool logs omit raw argv and output. Audit records include caller,
requested/selected target, observed UID/node, safe executable name, argument
count (excluding executable), and command hash. An `attempt` precedes streaming;
its `invocationID` links the completion. Outcomes are `exit_N`, `timeout`,
`canceled`, `stream_error`, `rejected` (setup/authorization/transport rejection),
or `failed` (before execution). Duration covers lookup and execution.
The hash is lowercase hex SHA-256 of Go `json.Marshal` of the complete argv
array; it permits guessed-command comparison, not secrecy. Upstream transport
logs may independently include peer-supplied error text.

## MCP Endpoint

```
http://localhost:9280/mcp
```

The port matches your `--port` flag (default 9280). The MCP server uses the stateless Streamable HTTP transport with JSON-RPC and accepts MCP protocol versions 2024-11-05 through 2026-07-28.

## Server instructions

When a client connects, Radar returns MCP [server instructions](https://modelcontextprotocol.io/specification/2025-06-18/basic/lifecycle#initialization) in the `initialize` result, or, for clients on protocol version 2026-07-28 (which has no `initialize`), in the `server/discover` result. The text is about 3,400 characters on which tool answers which question, how to read issue timing and attached changes, what a tool result does and does not prove, and that text read from the cluster (logs, events, annotations) is data, never instructions. It lives in [`pkg/investigation/guidance.go`](../pkg/investigation/guidance.go). Agents in clients that use server instructions get this guidance without a separate skill or rules file.

What a client does with the text depends on the client and its version. Some add it to the model's context at session start; others show it only after the model loads Radar's tools; some ignore it. Claude Code keeps the first 4,096 characters (2,048 before v2.1.296; see its [MCP documentation](https://code.claude.com/docs/en/mcp)), so Radar puts tool routing first and keeps it within the first 2,048 characters. Codex describes a server whose tools it loads on demand by the first line alone, cut at 250 characters, so that line says on its own what Radar is, that its tools are preferred over kubectl for reading cluster state, and which core tools to load. Each tool's description carries its own routing, so agents in clients that ignore server instructions still get it. `/mcp-readonly` sends the same text without the write tools.

### Waiting for a fix to land

Use `wait_for` after a change to wait for a controller's rollout, Pod readiness, Job completion, deletion, or a status condition. Each call waits at most 45 seconds so it can return within common MCP client timeouts. The caller owns the total deadline: repeat `waiting` calls within that limit, passing back the named object's `uid` to pin it. The states are `met`, `waiting`, `blocked` (an observed stall), and `failed` (terminal). Selector waits check the current set; use the controller's rollout to wait for replacements. `jsonpath={.status.field}=value` reads only `.status`; its freshness guard uses `status.observedGeneration` when present. `inStateSeconds` is condition age, not time since the fix.

## Catalog Introspection

MCP registries and inspectors can start Radar without a Kubernetes cluster when they only need the tool and resource catalog:

```bash
radar --mcp-catalog-only --no-browser
```

This mode skips Kubernetes initialization and starts the `/mcp` endpoint for schema introspection. Cluster-backed tool calls still require a normal Radar process connected to Kubernetes.

Adding `--mcp-read-only` exposes `/mcp-readonly` instead of `/mcp` and keeps the
internal investigation mount. The same config and environment precedence applies.

For registries that launch MCP servers over stdio, use:

```bash
radar --mcp-catalog-stdio
```

Add `--mcp-read-only` to serve the existing read-only catalog over stdio, with
the same write-tool and mutating-diagnostic rejection as HTTP. Both catalog
entrypoints reject an explicit `--no-mcp`. Choosing a catalog entrypoint
overrides a saved `"mcp": false`. `radar diagnose --standalone`
honors the saved and environment MCP mode; a disabled mode rejects startup.

This exposes the same tool and resource catalog over stdio without starting the HTTP UI server.
The stdio mode is intended only for catalog introspection; normal Radar sessions use the HTTP endpoint above.

## Setup Instructions

Connect your AI tool to Radar's MCP server. Radar must be running first (`radar` or `kubectl radar`). The examples use the default endpoint `http://localhost:9280/mcp`; if Radar runs on another port, use the endpoint shown in Radar's MCP dialog.

### Claude Code

Run this command:

```bash
claude mcp add --transport http radar http://localhost:9280/mcp
```

### Codex

Run this command:

```bash
codex mcp add radar --url http://localhost:9280/mcp
```

Or add to `~/.codex/config.toml`:

```toml
[mcp_servers.radar]
url = "http://localhost:9280/mcp"
```

### Cursor

Add to `~/.cursor/mcp.json` (all projects) or `.cursor/mcp.json` (one project):

```json
{
  "mcpServers": {
    "radar": {
      "url": "http://localhost:9280/mcp"
    }
  }
}
```

### VS Code (GitHub Copilot)

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

Or add it to your user profile from the command line:

```bash
code --add-mcp '{"name":"radar","type":"http","url":"http://localhost:9280/mcp"}'
```

### GitHub Copilot CLI

Run this command:

```bash
copilot mcp add --transport http radar http://localhost:9280/mcp
```

This writes the server to `~/.copilot/mcp-config.json`.

### OpenCode

Add to `~/.config/opencode/opencode.json` (all projects) or `opencode.json` in your project root:

```json
{
  "mcp": {
    "radar": {
      "type": "remote",
      "url": "http://localhost:9280/mcp",
      "enabled": true
    }
  }
}
```

Radar can also run in-app investigations with an installed OpenCode CLI (tested
with 1.18.5). Install with `npm install -g opencode-ai`; authenticate with
`opencode auth login` or use your existing provider configuration, including AWS
Bedrock. Model overrides use `provider/model` names from `opencode models`.

OpenCode supports the **Your agent setup** execution profile only. Radar inherits
your provider environment and OpenCode configuration, and runs with `--auto`:
permission requests are approved automatically, including those from built-in
tools and configured MCP servers; explicit denials still apply. Radar does not
enforce a CLI sandbox. Diagnosis and follow-up use Radar's read-only investigation
MCP. A confirmed Apply starts a separate write-enabled session, then resumes the
original read-only session to verify the result. As with other agents using your
normal setup, Radar cannot authenticate write results and reports the Apply
outcome as uncertain until current-state verification. Terminal handoff and
reasoning-effort controls are not available for OpenCode.

### Google Antigravity

Run this command:

```bash
agy mcp add radar http://localhost:9280/mcp
```

Or add to `~/.gemini/config/mcp_config.json` (all workspaces) or `.agents/mcp_config.json` (one workspace). Antigravity uses `serverUrl` for remote servers:

```json
{
  "mcpServers": {
    "radar": {
      "serverUrl": "http://localhost:9280/mcp"
    }
  }
}
```

### Kiro

Add to `~/.kiro/settings/mcp.json` (all workspaces) or `.kiro/settings/mcp.json` (one workspace):

```json
{
  "mcpServers": {
    "radar": {
      "url": "http://localhost:9280/mcp"
    }
  }
}
```

### JetBrains AI Assistant / Junie

AI Assistant: add via **Settings > Tools > AI Assistant > Model Context Protocol (MCP)**. Junie: add to `~/.junie/mcp/mcp.json` (all projects) or `.junie/mcp/mcp.json` (one project). Both use the same JSON:

```json
{
  "mcpServers": {
    "radar": {
      "url": "http://localhost:9280/mcp"
    }
  }
}
```

### Cline

Add via the Cline MCP settings UI. Set `type` to `streamableHttp`; without it, Cline uses the legacy SSE transport:

```json
{
  "mcpServers": {
    "radar": {
      "type": "streamableHttp",
      "url": "http://localhost:9280/mcp"
    }
  }
}
```

### Devin Desktop (formerly Windsurf)

Add to `~/.config/devin/mcp_config.json` (macOS/Linux) or `%APPDATA%\devin\mcp_config.json` (Windows). Both the default Devin Local agent and the legacy Cascade agent read `url` from this file:

```json
{
  "mcpServers": {
    "radar": {
      "url": "http://localhost:9280/mcp"
    }
  }
}
```

### Claude Desktop

Claude Desktop's custom connectors connect from Anthropic's cloud and cannot reach `localhost`. For a local Radar, use the [`mcp-remote`](https://github.com/geelen/mcp-remote) bridge (requires Node.js). Add to `~/Library/Application Support/Claude/claude_desktop_config.json` (macOS) or `%APPDATA%\Claude\claude_desktop_config.json` (Windows):

```json
{
  "mcpServers": {
    "radar": {
      "command": "npx",
      "args": ["-y", "mcp-remote", "http://localhost:9280/mcp"]
    }
  }
}
```

`mcp-remote` accepts plain `http://` only for `localhost` and `127.0.0.1`. For any other `http://` host, add `"--allow-http"` after the URL.

### Zed

Add to your Zed `settings.json`:

```json
{
  "context_servers": {
    "radar": {
      "url": "http://localhost:9280/mcp"
    }
  }
}
```

### Gemini CLI

For Gemini Code Assist Standard and Enterprise users. Run this command:

```bash
gemini mcp add --transport http radar http://localhost:9280/mcp
```

Or add to `~/.gemini/settings.json`:

```json
{
  "mcpServers": {
    "radar": {
      "httpUrl": "http://localhost:9280/mcp"
    }
  }
}
```

Any other MCP client that supports the Streamable HTTP transport can connect to the same endpoint.

## MCP Registry / Docker

Radar is listed on the [official MCP registry](https://registry.modelcontextprotocol.io) as `io.github.skyhook-io/radar`, packaged as the `ghcr.io/skyhook-io/radar` Docker image. The registry-suggested invocation is:

```bash
docker run -p 127.0.0.1:9280:9280 \
  -v ~/.kube/config:/kubeconfig:ro \
  -e KUBECONFIG=/kubeconfig \
  ghcr.io/skyhook-io/radar:latest
```

**Prefer the native install** (`brew install skyhook-io/tap/radar`, krew, or the install script — see the [README](../README.md)) — it uses your kubeconfig exactly as kubectl does. The Docker path has real limitations because the image is distroless (no shell, no cloud CLIs):

- **Exec-plugin auth does not work.** Kubeconfigs for GKE/EKS/AKS typically call `gke-gcloud-auth-plugin`, `aws eks get-token`, or `kubelogin` — none exist inside the container. Only kubeconfigs with embedded certificates or static tokens work.
- **Local clusters are unreachable.** kind/minikube/Docker Desktop API servers listen on the host's `127.0.0.1`, which inside the container is the container itself.
- **File ownership matters.** On Linux hosts a `0600` kubeconfig owned by your user is unreadable by the container's `nonroot` (uid 65532) user.

The Docker image's primary use is [in-cluster deployment](in-cluster.md) with a ServiceAccount, where none of these apply.

## Crossplane pause context

`list_resources` summaries, list/search `summaryContext`, and `get_resource`'s `resourceContext` carry optional `crossplanePause` for CRDs (identities in Radar's built-in registry are excluded, including typed objects converted for generic reads): `requested` for the exact `crossplane.io/paused: "true"` annotation, or `acknowledged` when the stored `Synced=False/ReconcilePaused` condition corroborates it. A request alone does not establish that a controller has stopped. Acknowledgement is a recorded observation, not a fresh external check or a claim about child controllers. No timestamp is inferred. An omitted field does not rule out other pause mechanisms.

Pause is separate from recorded readiness, operational issues and audit findings. On generic CRD summary rows carrying `crossplanePause`, `ready` preserves the recorded condition status, and an independent Synced error remains in `issue` even if Ready is True. `context=none` omits enrichment but retains the summary row's `crossplanePause`. Detail keeps raw annotations/conditions; Compact keeps the exact pause annotation key. Radar's own Diagnose kickoff carries the same fact without treating pause alone as a problem. The MCP `diagnose` tool's supported kinds are unchanged; use `get_resource` for Crossplane resources. Summary rows for all terminating CRDs also retain `terminating` and `finalizers`, matching typed-resource summaries.

## `diagnose` evidence limits

For workload `diagnose` responses, `logCoverage.selectedPods` counts pods selected for log requests, not pods whose logs were successfully read. Per-container errors and `logsError` describe failed collection. `logCoverage.totalLines` counts diagnostic-filter output before the aggregate response cap (including fallback tail lines); `shownLines` counts the lines retained after that cap. Neither describes a container's complete log history. `totalPods` and `shownPods` count pods contributing at least one line before and after that cap—not all selected pods or successful empty reads. `eventsTotalGroups`, recent-change coverage/error fields, and log sampling/truncation must remain qualifications even when an agent reports no problem.

Warning events in workload `diagnose` exclude events whose `involvedObject.uid` belongs to an earlier incarnation of the resource or its current Pods. `get_resource(include=events)` applies the same check to the requested resource only. UID-less events cannot establish which incarnation they describe. Core Node events that use the Node name as their UID are treated as UID-less; mirror Pod events may use the static UID from `kubernetes.io/config.mirror`. When Warning events were excluded this way, `warnings` says how many (cached Event objects, not occurrences) and that they may still explain what happened before, so an empty list is not read as nothing ever having failed; it covers only the resource and its current Pod names, and its absence does not prove that no predecessor failed. `get_events` can include events from earlier incarnations. `get_workload_logs`, generic metric scope and the live Pod inventory and OOM evidence of single-workload rightsizing apply the same current membership rule as `diagnose`; each reads it at its own time, and rightsizing recommendations still come from retained ownership history. `get_workload_logs` reports a workload the synced cache does not hold as not found rather than as zero Pods.

`podNames` lists the Pods controlled by the observed workload, with API group, kind, name and UID matched throughout controller ownership. Names are sorted and capped at 50; `podNamesTruncated` marks the cap, and `pods` counts the full set. Scope a follow-up `query_prometheus` to the listed names and namespace (`pod=~'^(a|b)$'`); a prefix such as `pod=~'api-.*'` can include sibling workloads.

For Pod, Deployment, StatefulSet, DaemonSet and Argo Rollout targets, `metrics` is a vitals snapshot captured inside the same call: `cpu` (cores, 5m rate), `memory` (working-set bytes) and `restarts` (counter increase over the trailing 1h, rounded per pod) summed over those pods across `since` (default 1h) at a step that yields at most 60 points per series and keeps the samples within an 8 KiB budget, with sample values rounded to four significant digits. The selected names come from the Pods the observed workload controls at collection time. Names no longer in that set are excluded, but earlier incarnations with the same name can contribute samples within the window; a set larger than 50 is cut to the first 50 by name with `partial: true` and `omittedPods`. The field is omitted when no Prometheus is configured, the workload controls no pods, or the caller cannot `get` the diagnosed resource; when the availability probe does not answer within the 3 s budget the field is present with `error` saying so, rather than vanishing as if no Prometheus existed. Radar starts discovery again in the background when a Prometheus is installed or its connection is lost (see [Metrics charts show "Prometheus not connected"](in-cluster.md#metrics-charts-show-prometheus-not-connected)), so a diagnose that comes after the new backend is ready normally finds it already connected instead of spending its budget on discovery. When Prometheus is configured but unreachable, the branch exceeds its 3 s budget, or the whole field (queries included) exceeds a 24 KiB backstop, `series` is empty and `error` says why; when one category's query fails, that category is left out and named in `error` while the others stand. A workload whose pods the cluster cache cannot establish — a denied lister, an informer still filling, a namespace it does not watch — is reported as an error rather than charted as empty. Each entry carries the PromQL that produced it (`query`), so an empty `series` with no `error` means the window holds no samples, not that nothing was asked. Flat stretches are compressed: the interior of an evenly spaced run of equal values is dropped, keeping the point that opens it and the point that closes it, so an hour of a workload sitting still costs two points rather than sixty. `observedSamples` reports how many points Prometheus actually returned for that category before compression — a two-point series with `observedSamples: 56` is an hour of agreeing samples, one with `observedSamples: 2` is two lonely ones, and the window and step cannot tell them apart on their own because a range query returns nothing for an evaluation with no data. Values are compared after rounding, so movement too small to reach four significant digits is treated as flat; a run is only compressed when its samples are evenly spaced, so a gap in the series survives. The window ends at collection time and is not a shared observation time for the other sources.

Collecting vitals costs one Prometheus round trip on every `diagnose` of a pod-backed workload, and that round trip is the call's critical path: against a healthy backend a diagnose without vitals returns in roughly 130ms and one with them in roughly 650ms, because nothing else `diagnose` does takes long enough to hide it. The queries for the three categories run concurrently with each other and with the rest of the bundle, and the branch is bounded by its own 3s budget, so a slow or unreachable Prometheus delays `diagnose` but never fails it. They are collected by default because an agent that has to ask for resource data can silently never ask, which makes a healthy verdict reached without looking indistinguishable from one where the numbers were fine. If that trade stops being worth it, the whole branch is behind one switch (`DiagnoseMetricsEnabled` in `internal/mcp`): turning it off omits the field and leaves the rest of the bundle and `query_prometheus` untouched, so the data stays reachable on request.

`expectedPreviousLogAbsences` requires container status consistent with no previous instance and either an empty successful read or the specific previous-instance-not-found response. Permission failures, missing pods, unavailable servers, and interrupted reads remain collection errors.

One call combines cached Kubernetes resource state, historical events/changes, and live log requests. Its completion time records when the call finished, not a shared observation time or freshness guarantee for those sources. Event/change timestamps retain their source-specific meaning.

These public MCP fields are available to consumers running the updated Radar server. The investigation contract that turns them into Findings — the prompt suite, the verdict, its parser and the citation binder — lives in `pkg/investigation` and is shared by the OSS engine and Radar Hub; each product supplies its own evidence issuance, run orchestration and store, and declares on its `/agents` entry whether it performs Apply (`apply`) and the verification turn after it (`verification`).

The investigation's final JSON also carries the agent's case: `evidence` (up to 8 items of `ref`, `role`, optional one-sentence `claim` — required for `benign`, `demoted` and `rules_out` — and optional `subject`) and `ruled_out` (up to 5 hypotheses pointing at an evidence item by index). The server binds each item's ref against the run's retained ledger exactly like `root_cause_evidence_refs`: a ref from the current turn must match the exact private-transport payload; a ref from an earlier turn of the same run is accepted on the persisted `radarEvidence` flag its own validator wrote, so a revised assessment can keep resting on a result it can no longer re-read. A failing item is dropped on its own as `unlinked`, and binding runs for every assessment, healthy and inconclusive included. The server-authored `evidence` / `ruledOut` fields are `omitempty`, so a diagnosis with no bindable case carries neither: consumers must treat both as optional.

**The story contract.** The last fenced `json` block in the reply that carries a verdict field (`summary`, `root_cause`, `healthy`, `inconclusive`, `steps`, `evidence` or `remediation`) is the verdict and a divider, so a patch or spec quoted in a `json` fence inside the story is never mistaken for it: prose after it is the story (`report`), prose before it the agent's evidence ledger (`notes`, kept in Activity and never shown in Findings); a reply with nothing after the block keeps the older shape, where the prose before it is the story. The story may place a Radar result with `[[radar:evidence=N]]` alone on a line — N is the 0-based index into `evidence` — or mention it inline, which places the card under that paragraph when no own-line marker places it and otherwise references the placed card (with an arrow saying where it is); `[[radar:evidence=N|compact]]` places the card with its header and role label, without the agent's note; log cards also retain the last two selected log lines. Each evidence item may carry a `gap`: the agent's own statement of what that result does not cover, rendered under its note. Only a result that returned data can be cited: a call that errored, was denied, or was truncated leaves its item `unlinked`. An inline mention past the placement cap renders as a chip that reveals the card under Captured results. Findings renders the placed result's card at that position from Radar's own record; the agent never authors a card. A marker inside a code span (which may run across lines within a paragraph), a code block, an indented code line or a blockquote is literal text. The ledger's own form, `[[radar:evidence-ref=ev_…]]`, is not a placement: the parser rewrites one that names a cited item to its index, strips any marker from `summary` and `root_cause`, and the renderer shows an unresolvable one as a citation Radar cannot identify rather than as text. At most 6 results are placed; later placements become references; a marker Radar cannot resolve renders a visible "support unavailable" note at the sentence rather than vanishing. The caps are the shape of the page and nothing else: at most 8 evidence items, 5 ruled-out hypotheses, 6 steps, 3 unresolved items and 3 root-cause refs, with a 180-rune headline cut at a sentence boundary; no other text is ever cut; a steps, unresolved or ruled-out list that ran past its cap reports the valid entries it lost in `omittedEntries`, evidence losses stay in `unlinkedEvidence`, and more than three root-cause refs still invalidate that set. The verdict adds `summary` (the one-sentence takeaway, ≤ 180 runes, clamped at a sentence boundary; `root_cause` is the two-or-three-sentence paragraph under it), `certainty` (`established` | `likely` | `suspected`, rendered as the agent's word, never a Radar mark), `unresolved` (≤ 3 items, listed under *Still open*, followed by any read Radar could not complete and, on a healthy verdict, each adverse Radar card the agent explained with its note; an adverse card the verdict never addressed, or one the agent calls a cause or symptom, keeps a small amber header naming it; every other caveat stays on the claim it bounds — the gap line under the cause card, the precondition on a step — or inside Captured results), typed `steps` (`text`, `kind` of `mitigate` | `verify` | `investigate`, optional `precondition`; when present the server derives `remediation` from them so one list feeds Apply and `recommended_index`, which may only name a `mitigate` step), and on follow-up turns `revises_assessment`, which the server keeps only on a complete verdict (`summary` plus a root cause, healthy or inconclusive). Findings shows the newest assessment turn — initial, verification, or a question whose verdict revised it — and nothing else; Activity keeps every earlier assessment complete. With typed steps present, `inconclusive` is cleared only by a non-empty `root_cause` (a mitigation never establishes a cause). No role can hide, collapse, or recolor a card, and a claim that cannot be pinned to exactly one observation shows beside its source under Captured results ("Notes without a card") instead. A subject may omit `name` to cite a listing for what it does not contain (kind and namespace name the listing; uniqueness still decides placement); a subject that names an entry binds to the listing whether or not it holds the entry, and the story card then shows that row inline, or says the entry is not in the listing, or that the name matches several entries. `observation: "resource"` names anything a call returned about a resource: a single resource, a listing, a ranking or posture card, a Helm release, a permissions check or a neighborhood; the stream-shaped kinds (`logs`, `events`, `changes`, `metrics`, `alerts`, `issue`) keep their own word so one word still picks one observation of a diagnose bundle. A receipt is not a kind of its own but what a call of any kind returned when it found nothing (a metric search, a scan with nothing about the target), so a call whose only observation is a receipt is not held to the word; what the subject names still has to match. `observation` is one of `resource`, `logs`, `events`, `changes`, `metrics` (or `metrics:cpu` / `metrics:memory` / `metrics:restarts` for a diagnose bundle's charts), `alerts` (with `name` as the rule's name, since one rules result holds several rules) or `issue` (`issues` is accepted as an alias); any other word leaves the item unplaced. A metrics card placed as the cause or the symptom shows its chart inline. Only results Radar projects into cards can be placed (`diagnose`, `get_resource`, non-empty `list_resources`, `list_namespaces`, `list_helm_releases`, `list_packages` and `search`, `get_events`, `get_pod_logs`, `get_workload_logs`, `get_changes`, `get_neighborhood`, `get_topology`, `get_helm_release` with its hooks, revision history and user values, `get_subject_permissions` with its effective rules, `get_prometheus_rules`, `query_prometheus`, `discover_metrics` as a receipt, `issues`, `top_resources` as a ranking with the workload's own row marked, and `get_cluster_audit` or `get_cluster_upgrade_readiness` as a posture card holding only the findings about the investigated resource or its relations, a scan with none becoming a receipt); an empty listing or `get_dashboard` can be cited but renders a "no card" note at the sentence. A ranking or posture card placed as the cause or symptom shows the rows about the workload inline. Subject matching forgives two agent slips that name no other object: a built-in API group on a core kind (`apps` on a Pod) and a named entry on a listing (a specific ConfigMap cited against the ConfigMaps of a namespace, or a package named by the kind the agent knows it as when the listing holds that name); a vendor group on a core kind (a Knative Service) still names a different object. A question the agent answered with `revises_assessment: false` does not make the assessment "earlier": its reads join Captured results without demoting the headline or the steps. A backend that sends no `summary` and no placement markers renders in the previous shape.

Captured Crossplane resource cards keep declared pause as neutral context and retain the raw `Synced=False/ReconcilePaused` value. Corroborated pause alone does not make the card adverse; independent errors remain adverse. A visible explanation scopes the pause to that resource’s reconciler and describes readiness/errors as last reported. This presentation uses the captured resource, not live replacement data or agent-authored claims.

## Cost and rightsizing evidence limits

### `recommendationReason` — why a row carries no recommendation

`get_rightsizing` rows may set `recommendedRequest: null`. The accompanying `recommendationReason` says why, and two of the values mean Radar **deliberately declined** to recommend rather than lacking the evidence to try. Reporting those as "no recommendation available" loses the reasoning that produced them:

| Reason | Meaning |
|--------|---------|
| `hpa_managed` | An HPA manages this resource. Review its target before changing the request — the two controls interact. |
| `oom_evidence` | A lower memory request is **withheld** because the container has OOM evidence in the window. Cutting it is the wrong move even though usage looks low. |
| `oom_evidence_unavailable` | Radar could not verify recent OOM history, so it withheld a memory reduction rather than recommend one blind. |
| `hpa_evidence_unavailable` | Radar could not verify HPA ownership, so it withheld a suggested request. |
| `recommended_request_exceeds_limit` | The evidence-based request would exceed the container's current limit; the limit is the thing to review first. |
| `request_within_fit_range` | The configured request is already within 30% of the evidence-based target. Nothing to change. |
| `insufficient_history` | Fewer than six hours of samples. Not a verdict of correctly sized. |

`reductionLimited: true` marks a different case: a recommendation exists, but it is a conservative step toward the demand-based target rather than the target itself. Those rows — and only those — also carry `demandTarget: {value, basis}`, the end state the step is heading toward, with `basis` naming how it was derived (`7d Max x 1.15` for memory, `7d P95 x 1.15` for CPU, or the minimum request). It is **not** a value to apply: apply `recommendedRequest`, observe a full window, then re-check. A memory target comes from the 7-day max, so a monthly or batch peak outside the window is invisible to it. The engine computes a target before its HPA, OOM and limit checks run, so a withheld row has one internally; it is never emitted there, because it would fill the `null` those checks left on purpose. `observedStatistic` names what `observed` is (`P95` for CPU, `Max` for memory). `bursty` and `p99Usage` qualify CPU rows whose P99 ran far above the P95 the recommendation is built on.

### `managedBy` — where a request change belongs

Workloads carry `managedBy: {tool, kind, group, namespace, name, signal}` when Radar can tell what owns their spec: `controller` (a controller ownerReference, such as an operator's custom resource), `flux`, `argocd`, `helm`, or `addon-manager` (`addonmanager.kubernetes.io/mode=Reconcile` only — `EnsureExists` does not revert edits). A patch applied live to such a workload is overwritten on that tool's next reconcile or upgrade, so the change belongs at the source. `signal` names the metadata that produced the answer, because labels get copied between templates.

A missing `managedBy` does **not** mean the workload is unmanaged. Argo CD's default tracking label is also every Helm chart's `app.kubernetes.io/instance`, and Terraform or `kubectl apply` leave nothing distinguishable.

### `efficiencyPercent` is not request headroom

`get_cost` emits `efficiencyPercent` on namespace and workload rows and `efficiencyPercent` on summary totals. The denominator is **cost allocation, which is the greater of requested and observed** usage — not the request. Two consequences that the numbers alone do not reveal:

- The value is capped at 100 and can never exceed it. A container consuming well above its request reads as ~100%, which is not the same as correctly sized.
- A low value is not by itself a defect and not recoverable money. Bursty workloads, P95-sized requests and HPA headroom all produce low ratios by design.

It answers "how much of what this is costing me is being used", not "how much can I cut". Use `get_rightsizing` for the second question — it applies the OOM and HPA gating above, which a ratio cannot.

`usageUnavailable: true` on a row means the usage evidence behind it is missing or incomplete — the usage query failed, the cost source reported no usage for the row. `efficiencyPercent` is then `null` rather than a number, and `unusedRequestHourlyCost` is not reported. That is distinct from measuring efficiency as zero, which a row using nothing genuinely does and which is emitted as `0`.

### What `hourlyCost` contains, and the two kinds of idle

`view=summary` totals report idle as two figures, because they are recovered by different actions:

- **`unallocatedHourlyCost`** — node compute capacity no workload requested or used. It comes back only when nodes are removed.
- **`unusedRequestHourlyCost`** — capacity requested but not used. It comes back only after rightsizing frees enough requests for a node to be removed.

The two do not overlap. Rows carry `unusedRequestHourlyCost` only; unrequested node capacity belongs to the cluster, so `unallocatedHourlyCost` is `null` on a namespace-scoped or RBAC-narrowed summary, and also when the source did not measure it or GPU spend on the nodes keeps it from being separated — never `0`, which would claim the nodes are fully packed. `unusedRequestHourlyCost` is likewise absent when no namespace has usage evidence, and it covers CPU and memory requests only — idle GPUs are not in it.

`allocatedHourlyCost` always sums the namespace allocations; `allocatedMonthlyProjection` projects that rate over 730 hours. `nodeHourlyCost` independently measures node capacity, with `nodeMonthlyProjection` on the same basis. Node figures are explicit `null` when unmeasured, including namespace-scoped summaries and Kubecost summaries (which do not query node assets). Never add allocation and node totals together: node capacity already contains unallocated capacity. Their component coverage differs, so allocation can exceed node cost. OpenCost allocations include CPU, memory and storage, but not GPU or network.

Workload-view totals carry allocation fields; node-view totals carry node fields. Monthly projections use the same 730-hour convention as the Costs UI. Component names such as `cpuHourlyCost` and `unusedRequestHourlyCost` explicitly identify hourly rates.

### Node rows carry no CPU/memory split

`view=nodes` accepts an optional exact `name` selector. Selection happens before the limit; totals and `nodesNotInCluster` cover only the selected rows. No match returns `reason: node_not_found`. It returns `hourlyCost`, `instanceType` and `region`, and deliberately **omits** per-node CPU and memory components. The OpenCost path derives them from `node_cpu_hourly_cost` / `node_ram_hourly_cost`, which are per-vCPU-hour and per-GiB-hour **unit prices**, while Kubecost reports whole-node **totals** — one field name for two different quantities. Use `hourlyCost` with `instanceType` to compare nodes.

Rows also carry `pool: {name, source}` and `capacityType` (`spot`, `preemptible`, `on-demand`, `reserved`) from node labels, resolved with the same pool rule as the Capacity views. Each is omitted when unknown: a missing spot label only means on-demand on GKE and AKS pool nodes, EKS managed node groups and Karpenter, which label every node's capacity type. `on-demand` is not list price — committed-use and savings-plan discounts are not visible. `nodesNotInCluster` counts nodes the cost source still reports that the cluster no longer has; their rows carry neither field. Each row is a node's rate while it ran, and totals sum every row, so after node churn inside the window they overstate the current run rate. Whether a pool can shrink (autoscaler limits) is not in this response.

### Fleet scan lifecycle

Namespace and cluster rightsizing runs continue in memory after a request ends. Each run has a three-minute ceiling, with 30-second budgets for individual metrics queries. `scanStatus=running` means the rows, counts, and rankings are provisional; follow `nextCall` after `pollAfterSeconds`. Requests wait at most 20 seconds for progress. `finished` means the scan stopped working, while `state` still distinguishes complete, partial, and unavailable evidence. `timed_out` and `cancelled` retain available results.

A normal call reuses the latest matching scan, retained for up to 15 minutes. Use `scan_id` with the same scope and namespaces to inspect an exact snapshot or change presentation filters without querying Prometheus again. After changing workload requests, omit `scan_id` and set `refresh=true` to start fresh. IDs are scoped to the caller and cluster; restart, expiry, or a cluster/backend change requires a new scan. There are at most two active scans, with no queue or automatic retry. Workload scope stays synchronous.

### How scan scopes rank workloads

`scope=namespace` and `scope=cluster` apply the Rightsizing screen's ordering rule, at a coarser grain — the screen ranks each **container** as its own entry while these scopes return **workloads**, so a workload's containers are summed here and the top-N can legitimately differ from the screen's. Both surfaces retain known actionable evidence when another resource lacks data; missing evidence yields `need_data` only when no actionable classification can be established from the available evidence. The rule is:

1. **Reliability priority** — evidenced OOM signals, limit conflicts, and throttled reductions first; then `increase`, ordinary `reduction`, routine `review` (burstiness-only, HPA-only, or scaled-to-zero), `need_data`, and `in_range`. Retained risk history does not promote scaled-to-zero workloads. Priority is computed before response limits; classification/filter values remain unchanged.
2. **`requestDelta`** — the replica-weighted absolute request change (`{cpu, memory}`, formatted quantities such as `-2250m`), normalized so a CPU change and a memory change are comparable.
3. Namespace, kind and name, so repeated scans return a stable order.

Rows needing manual review — an HPA, OOM history, a limit conflict, or a reduction against bursty or throttled usage — contribute **no** impact: that saving cannot be applied unattended, so counting it would rank a workload by waste it cannot give back. Unevidenced rows also contribute no delta. Missing evidence for one resource does not hide a known increase or review signal on another resource in the same container. Classification uses evidenced rows; `need_data` applies when none is evidenced, or when the evidenced rows are in range but other rows cannot be judged. Missing CPU/memory delta fields mean zero net change or no eligible evidence; inspect rows to distinguish these. Ranking by the largest *proportional* change instead would put an unrequested 64Mi sidecar above the workload wasting two cores.

`classificationCounts` reports all five classes (`reduction`, `increase`, `review`, `need_data`, `in_range`), including zero counts, across evaluated workloads before classification/row filtering and limits. It excludes workloads not evaluated, so partial scans are not whole-cluster counts; evaluated workloads can still lack evidence. Counts expose every class even when higher-priority entries fill the returned list. Use the `classification` filter to inspect those classes.

`totalWorkloads` is always present on available scans, including zero. It counts workloads matching the selection before the limit; `coverage.workloadsEvaluated` describes the whole scan. Use `classification=reduction|increase|review|need_data|in_range` to select a class before truncation. A classification filter returns every row of matching workloads, while scan effort and coverage stay unchanged.

### Rows that are never omitted

`include_all=true` returns balanced and unevidenced rows too. Workload scope always returns all rows. `evaluatedAt` is evaluation time, not source freshness; workload and scan responses use the same field. Row statistics are `sampleCoverage` (fraction), `p99Usage` (CPU P99), and `throttlePercent` (numeric percentage). `liveInventoryUnavailable` on a workload means current pod OOM status is unknown, even if its rows lack `currentPodOOM`. Workload failure reasons use stable codes: `queries_failed`, `no_usage_samples`, `no_owner_samples`, `pod_inventory_unreadable`, and `no_containers`.

By default a scan returns only `oversized`, `under_requested` and `missing_request` rows and reports the rest in `omitted`. Four exceptions:

- **`scope=workload` returns every row.** The caller named one workload; a handful of rows is the whole answer.
- **A `scaledToZero` workload returns every row.** Unless its evidence is missing (`need_data`), it classifies as `review` — its 7-day history may not describe the next scale-up — and the Rightsizing page lists it among the actions. A DaemonSet with zero desired pods, once its controller has observed its current spec, matches no node right now, so scans skip it, count it in `coverage.daemonSetsWithoutNodes` and name it in `skippedDaemonSets` (first 50, `skippedDaemonSetsTruncated` beyond that); a node pool scaled to zero still has history for it, which `scope=workload` reads as a `scaledToZero` workload. A zero the controller has not yet re-observed (a new DaemonSet, or a changed selector) is scanned instead, as a `scaledToZero` workload. A namespace holding only such DaemonSets is never reported in `namespacesWithoutWorkloads`, and a scope holding nothing else answers `reason: only_daemonsets_without_nodes` rather than `no_workloads`.
- **A returned container keeps its unevidenced rows.** The missing row explains the partial state beside any valid recommendation on the other resource. On scans such a row's `queryError` names the warning code carrying the real error, and `omitted.queryError` counts only the rows still hidden. Known increase/review signals still contribute to the workload classification.
- **A row carrying `currentPodOOM`, `windowOomEvidence`, `limitConflict`, significant throttling (≥10%), or an autoscaler is always returned**, whatever its `fit`. `fit` is settled from the request alone, so the classic OOM shape — request fine, **limit** too low — classifies as `balanced`. Filtering on `fit` would drop exactly the row an "is this under-requested?" question is looking for.

`oomEvidenceAvailable` is emitted on **memory rows only**. It never gates a CPU recommendation, and a literal `false` there would read as an evidence gap. `throttleAvailable` is its CPU counterpart: on CPU rows only, and `false` means throttling was not measured — not that the container is unthrottled.

### `state: partial` always names its cause

A partial response carries a `reason`, and `guidance` names the causes that are actually present rather than listing every field that could explain one. Causes:

| Reason | Cause |
|--------|-------|
| `row_evidence_incomplete` | Row-level gaps: rows short of history, failed usage queries, or a recommendation withheld for missing HPA/OOM evidence. Cluster coverage is complete. |
| `requested_namespaces_excluded` | A `namespaces` list included names that were not scanned. `excludedNamespaces` lists each with `access_denied`, `outside_namespace_pin`, or `not_cached` (Radar's informer cache does not hold that namespace); report only `effectiveNamespaces`. |
| `namespace_scope_limited` | The scan reached only the namespaces in `effectiveNamespaces` — scope was resolved from this identity's per-namespace access rather than a cluster-wide grant, or radar is pinned with `--namespace-scope`. `remediation` says which. |
| `limited_scope_no_workloads` | No workloads found, and the scan did not cover everything requested. An empty result here is **not** evidence the cluster has no workloads. |

`coverage.partiallyCachedKinds` — kinds whose informer covers only some namespaces — can be the **only** reason a scan is partial, and does **not** mean the kind is unreadable. It is counted separately from `restrictedKinds` and `unavailableKinds`: only those two decide whether every kind is genuinely unreadable (`state: unavailable`, `reason: workload_kinds_unavailable`).

`remediation` accompanies the reason on **every** state, not just `unavailable`.

### The `--namespace-scope` pin is not an access denial

A pin and an RBAC restriction produce the same namespace list. A request the pin excludes returns `reason: outside_namespace_pin` and a `remediation` naming the flag — never `access_denied`, which would tell a cluster-admin their permissions are the problem. That applies to namespaces the caller can see; one they can't is `access_denied` whether or not Radar is pinned to it, so guessing names can't find the pin.

### Trend responses are pre-summarized

Raw `dataPoints` are omitted by default to keep responses small. Pass `include_points=true` with `view=trend` to include them. The default response includes this retry hint; summaries are identical either way.

`view=trend` returns each series with `startHourlyCost`, `endHourlyCost` and `changePercent`, plus `trendTotal` summed across series per timestamp. `trendTotal.from`/`to` are the first and last sample timestamps (RFC3339), possibly shorter than the requested range; `sampleCount` counts timestamps. Rates are hourly, not cumulative spend. `changePercent` is explicitly null for zero starting cost or a single sample. Trend has no monthly projection. Both the total and each series also carry `minHourlyCost`, `peakHourlyCost`, and `peakAt` (the earliest timestamp when the peak repeats). These describe observed samples, not interpolated values or peaks between samples; a series without samples omits these fields. The total peak comes from summed costs at each timestamp, not the sum of each namespace's separate peak. A small endpoint change can conceal an excursion: compare the minimum and peak, then request raw points to inspect the shape. Raw points retain the same namespace aggregation; they do not expand the remainder into individual namespaces.

Only the highest-spending namespaces get individual `type: namespace` series, each with `namespace`. The remainder is a `type: remainder` series with no namespace, distinct from a real namespace named `other`. `seriesCapped: true` reports aggregation; `truncated` is reserved for row-list limits. `namespaceCount` is the full scope's count, and `trendTotal` includes every namespace.

`trendTotal.basis` is `cpu_memory_allocation` for OpenCost via Prometheus (compare `endHourlyCost` with summary `allocatedHourlyCost` minus storage) or `namespace_allocation` for Kubecost (all allocated components, idle excluded). Differences can also come from window alignment.

`effectiveNamespaces` identifies the namespaces actually covered when scope is narrowed. Rightsizing `coverage.successfulBatches` counts batches whose queries all succeeded, not batches attempted.

Both tools return `guidance` as an array of interpretation notes; read notes about missing evidence and scope before reporting a conclusion.

## Resource reads: coverage and errors

Every `list_resources` result says what it covers. A list with no usable
inventory (nothing readable, an oversized result, or a changed connection), and
any `get_resource` failure, is an MCP tool error with a code. Workload and
GitOps `diagnose` root reads apply the same readiness checks. Codes are defined
in `pkg/readreason`, and the coverage types in `pkg/resourceapi`; REST resource
reads and search use the same codes.

### Words first

An agent reading these results has not been trained on Radar's vocabulary, and
a tool description can't carry a manual for it. So every result a model reads
says in words what the codes and coverage say as data, in two parts:

- **What happened**: `summary` for a list, `error` for a failure. What the
  answer covers, what it can't show (a namespace Radar didn't read never reads
  as "none there"), and, when only a person can change that, who: a cluster
  admin for your access, or whoever runs Radar for what it collects. Findings
  shows this to people too.
- **What the agent can do**: `next`, present only when the agent itself can
  change the answer, naming the exact tool and argument. It is absent when only
  a person can change the answer, and when the answer is complete and names
  every namespace it read.

`next` appears for three situations, because only these reliably change an
agent's next move:

| Situation | `next` |
|---|---|
| Part of the answer clears on its own: still loading, a permission check that failed, a watch that failed to start, an unexpected error | Retry, and when: "Retry in a few seconds to read namespace \"gamma\"." (a minute for a load that missed its deadline). While Radar is still connecting to the cluster, the error itself says to retry |
| The answer counts namespaces it can't name, so the agent can't tell whether the one it cares about was read | "To check one namespace, call list_resources again with namespace set to it." |
| The request can't be answered as asked: a get with no namespace, a kind Radar doesn't know, a namespace you can't see, an object that isn't there | The narrower or different call: set `namespace`; `search` for `kind:<kind> <name>` (naming the kind reaches kinds a broad search skips); `list_resources kind=customresourcedefinitions`; `list_namespaces`, when Radar can list namespaces; or the same name under another kind or namespace |

A denial (`user_forbidden`), and anything Radar's own identity or configuration
blocks, has no `next`: retrying won't change it, and its `error` names who can.
Codes and coverage stay for code to branch on and never change spelling; the
sentences may be reworded.

### Lists

`list_resources` always returns one text block holding
`{summary, next?, coverage, items}`, the summary first so it survives
truncation that keeps only the head of a long result:

```json
{
  "summary": "Partial: 1 Service from namespace \"alpha\". Unknown: namespace \"gamma\", because Radar is still loading services there. Other namespaces are unknown: your access covers only namespaces \"alpha\", \"gamma\". A cluster admin can widen your access.",
  "next": "Retry in a few seconds to read namespace \"gamma\".",
  "coverage": {
    "status": "partial",
    "scope": {"type": "namespaces", "namespaces": {"count": 2, "names": ["alpha", "gamma"]}},
    "narrowedBy": ["user_access"],
    "read": {"type": "namespaces", "namespaces": {"count": 1, "names": ["alpha"]}},
    "gaps": [{"code": "kind_sync_pending", "action": "retry", "namespaces": {"count": 1, "names": ["gamma"]}}]
  },
  "items": [{"kind": "Service", "name": "api", "namespace": "alpha"}]
}
```

- **`summary`** is always present and leads with its verdict: `Complete:` when
  Radar read the whole scope ("Complete: 2 services across every namespace.",
  or "Complete within your access: …" when the scope was narrowed), otherwise
  `Partial:`, with each part it didn't read called unknown, why, and who can
  widen it.

- **`scope`** is what the answer is about: `cluster` for a cluster-scoped kind,
  `all_namespaces`, or a namespace set. A namespace you name stays the scope
  even when Radar can't read it.
- **`narrowedBy`** says why an unscoped request covers less than every
  namespace: `user_access` (the namespaces your access allows),
  `namespace_pin` (Radar's `--namespace-scope`), or `namespace_collection`
  (the namespaces Radar collects the kind in, when its identity can't list it
  cluster-wide). It is absent when nothing narrowed the request.
- **`status`** is relative to `scope`: `complete` means Radar's watch of every
  namespace in it has loaded, so missing rows are absent from what Radar
  observes *within that scope*; `partial` means `gaps` names what it did not
  read. A complete list with `narrowedBy` is still not the cluster. `status`
  keeps this meaning: watch freshness or paging, if added, get fields of their
  own.
- **`read`** is what Radar read within `scope`. `read` and the gaps don't
  overlap and together make up `scope`, so on a complete answer `read` equals
  `scope`.
- **`gaps[]`** are the reasons the answer isn't complete. Each has a `code`,
  the `action` that code calls for (the same table as below, so a client that
  doesn't know a newer code still knows whether to retry), and where it
  applies. For a namespaced kind every gap states its `namespaces`: a set, or
  `{"extent": "all"}` for every namespace in scope (the kind is still loading
  everywhere, say), or `{"extent": "unknown"}` when Radar can't say which. A
  cluster-scoped kind's gaps have no `namespaces`. A namespace is in at most
  one gap.

Namespace sets are `{count, names}`. `count` is exact. `names`, when present,
lists every member, sorted; it is absent when there are more than the answer
can carry (sixteen on MCP, fewer on REST when one response carries many sets),
and then only `count` says how many. A reader knows a namespace's membership
from `names` or not at all. The same holds for a gap's `kinds` and `objects`.
`items` is `[]` when the namespaces read hold nothing.

What Radar doesn't collect shows up in one of two ways. When it knows the
namespaces it collects a kind in, an unscoped request is narrowed to them
(`narrowedBy: namespace_collection`) and can be complete within them. A
namespace you name that Radar doesn't collect stays in scope as a
`radar_namespace_not_collected` gap.

A client meeting a value it doesn't know treats it conservatively: an unknown
`code` as an incomplete answer, with no remedy beyond its `action`; an unknown
`narrowedBy` as narrowed; an unknown `scope` type as grounds for no
completeness or absence claim.

Each namespace is read on its own. A namespace whose informer is still loading
becomes a gap, and the other namespaces' rows are still returned. This holds
for built-in kinds too: when Radar watches a kind namespace by namespace, a
namespace whose informer has synced is read even while another is loading. A
namespace you name that Radar doesn't collect, or was refused in, is a gap too;
on an unscoped request those namespaces narrow it (`namespace_collection`) instead.

Secrets, Roles, RoleBindings, LimitRanges and Trivy Operator reports need your exact permission
on that kind, not just namespace visibility: namespaces you can see but can't
list them in are a `user_forbidden` gap (or `user_permission_check_failed`
when the check didn't complete), and a get needs `get` in the namespace. A
caller whose access reaches every namespace is checked once, cluster-wide: an
unscoped list of one of these kinds fails with `user_forbidden` unless they
can list it everywhere, and the error says to name a namespace they can list
it in. Cluster-scoped kinds keep exact per-kind permission
checks and fail with `user_forbidden`. Namespace lists use every namespace Radar
knows of, after the list-namespaces permission check.

The browser namespace picker does not scope MCP requests.

### Errors

A list fails only when it returns no usable inventory: Radar read nothing in
its scope, its cluster connection changed mid-read, or the answer was over its
size budget. A failed read's `structuredContent` and its only text block are
the same body, `{error, error_code, next?, coverage?}`. A list's error carries
the `coverage` Radar assessed, without `read`: an error returns no rows. A
request Radar couldn't turn into a scope has none (a namespace you can't see,
or one outside the `--namespace-scope` pin). A get's error has no coverage.

```json
{
  "error": "Radar doesn't collect services in namespace \"read-c\", so it can't say what exists there. It collects services in namespace \"read-a\". Whoever runs Radar can add namespaces with --namespaces, if its kubeconfig identity can list services there.",
  "error_code": "radar_namespace_not_collected",
  "coverage": {
    "status": "partial",
    "scope": {"type": "namespaces", "namespaces": {"count": 1, "names": ["read-c"]}},
    "gaps": [{"code": "radar_namespace_not_collected", "action": "radar_config",
              "namespaces": {"count": 1, "names": ["read-c"]}}]
  }
}
```

Branch on `error_code`. A failure the agent can act on itself carries `next`,
as on lists (`namespace_required`: set `namespace`, or `search` for
`kind:<kind> <name>`; `object_not_found`: the same name under another kind or
namespace, else that `search`). A list that read nothing takes its `next` from
every gap, so a retry that would recover one namespace is offered even when
the code names a denial. `error_code` is the most actionable cause; when
namespaces failed for different reasons, `coverage` has a gap for each, and the
sentence names every cause and the namespaces each affects. When something
other than your own access narrowed an unscoped request (what Radar collects,
or its pin), the sentence says that too. A get of an object that isn't there is
`object_not_found`, and only once Radar has confirmed it read that namespace;
otherwise the code says why it couldn't look. A get that finds its object
returns it even while that kind is still loading: the object exists, and only
a miss needs a complete read.

| Code | Meaning | What resolves it |
|---|---|---|
| `user_forbidden` | Your own RBAC denies the exact operation. | Your access |
| `user_no_namespace_access` | Your access reaches no namespace in the request's scope (Radar checks list Pods or Deployments there). | Your access |
| `user_namespace_not_visible` | A namespace you named is not among those you can see, or does not exist; the code doesn't say which. | Check the name, or your access |
| `namespace_not_found` | A namespace you named doesn't exist. Only a caller who can see every namespace is told this, and only when Radar collects Namespaces. It comes before `outside_namespace_pin`: a mistyped name is the likelier cause. REST lists and gets answer the same way, and a missing namespace beside existing ones is a gap while the others are read. | Check the name; `list_namespaces` lists them |
| `user_permission_check_failed` | Radar could not complete your permission check. | Retry |
| `radar_forbidden` | Kubernetes refused Radar's service account or kubeconfig identity. | Radar's access — granting yours does not help |
| `radar_credentials_rejected` | Kubernetes rejected Radar's credentials, on this read or when Radar connected and checked its access. If a read's rejection persists, Radar's connection goes down and reads report `cluster_disconnected`. One recorded at connect leaves that namespace unread, beside the others, until Radar reconnects. | Refresh Radar's credentials; for one recorded at connect, then reconnect Radar (`POST /api/connection/retry`) |
| `radar_namespace_not_collected` | The namespace is outside what Radar collects. | Radar's collection (`--namespaces`, `--max-scope-candidates`, or its RBAC) |
| `kind_sync_pending` | The kind is still loading. | Retry shortly |
| `kind_sync_timed_out` | The kind's initial load missed its deadline; Radar keeps trying. | Retry later; if it persists, Radar's connection or list/watch access |
| `radar_kind_not_collected` | Radar's setup leaves the kind out. | Radar's collection configuration |
| `kind_not_watchable` | The API can't be list/watched. | Use another tool; retrying won't help |
| `kind_unknown` | The kind isn't in the catalog or discovery. Before answering this, Radar refreshes discovery (at most every 30 seconds), so a custom resource installed since the last refresh resolves. When discovery failed for the group the request named, or Radar's last discovery refresh failed outright, the answer is `read_error` instead: the kind may exist behind an API that isn't answering. | Check spelling, `group`, and installation |
| `outside_namespace_pin` | The namespace, one you can see, is outside Radar's `--namespace-scope` pin. A namespace you can't see is `user_namespace_not_visible` whether or not Radar is pinned to it, so guessing names can't find the pin. | Radar's `--namespace-scope` setting |
| `namespace_required` | A namespaced `get_resource` needs a namespace. | Pass `namespace` |
| `object_not_found` | The object is absent from a namespace Radar read in full. | — it is an answer |
| `cluster_connecting` | Radar is still connecting. | Retry shortly |
| `cluster_disconnected` | Radar has no working cluster connection; the explanation says why. | Radar's connection |
| `connection_changed` | Radar's cluster connection changed during the read. | Confirm which cluster Radar is on, then retry |
| `read_error` | An unexpected failure; the explanation says what. | Retry, or investigate |

The same table, as the action each code calls for, is
`pkg/readreason/testdata/actions.json`.

Under Radar Cloud, a denial of a viewer or member also says an owner of their
organization can raise their role, which may grant the permission: the chart
binds each Radar Cloud role to Kubernetes permissions by default, and a cluster
can bind its own. Radar doesn't know which, so a cluster admin stays the first
remedy.

Errors about Radar's own access omit the API server's response, which may name
Radar's identity; the raw cause is logged server-side. Errors, gaps and
summaries name only namespaces the user may read or named themselves.

Findings shows a list's summary as its coverage caveat, including for an empty
partial or narrowed list. A placed story card distinguishes an unread subject namespace
from a missing object in a namespace that was read; when a listing read more
namespaces than it names, the card says how many it read and that the subject's
namespace may not have been one. A bare array comes from a
Radar older than this contract and is never read as complete.

## Available Tools

### Read Tools

For interpreting workload bundles, see [`diagnose` evidence limits](#diagnose-evidence-limits), including collection failures, sampling, source timestamps, and the public MCP versus local investigation boundary.

For `get_cost` and `get_rightsizing`, see [Cost and rightsizing evidence limits](#cost-and-rightsizing-evidence-limits) — what each `recommendationReason` means (two of them are deliberate withholdings) and why `efficiencyPercent` is not request headroom.

| Tool | Description | Parameters |
|------|-------------|------------|
| `issues` | "What's broken right now?" — a ranked, curated stream of live operational failures: failing workloads/pods, active native Helm release failures or stuck pending operations (`kind=HelmRelease`, `group=helm.sh`), dangling references, NetworkPolicies that leave a Service's pods with no allowed inbound traffic, pod-startup blockers (unschedulable / admission-rejected / stuck post-bind), and False CRD conditions. No source filter; each row carries a `source` label sliceable via `filter`. Recovered Helm rollbacks are deploy history, not live issues; use `get_changes` for Helm deployment history and `get_helm_release` for native Helm full revision/history/hook diagnostics. Flux `HelmRelease` rows (`group=helm.toolkit.fluxcd.io`) are GitOps reconcilers and should use `diagnose`. For static posture use `get_cluster_audit`; for raw events use `get_events`. | `namespace` (optional), `severity` (optional: `critical,warning`), `kind` (optional), `filter` (optional CEL), `limit` (optional, default 200, max 1000) |
| `diagnose` | Collect a bounded, point-in-time evidence bundle for one narrowed workload, GitOps reconciler, or network entry kind in a single call. It supports diagnosis but does not run an agent, provide an authoritative root-cause verdict, or exhaustively read every source. Pod/Deployment/StatefulSet/DaemonSet and Argo Rollout (`kind=Rollout`, `group=argoproj.io`) get minified resource + `resourceContext` + selected, capped current and previous container logs across pods + `crashCause` evidence that pairs active crashloop status with one highest-signal filtered log line (`logLineSelection` states how the line was chosen, in descending confidence: `fatal_pattern`, `traceback_header_only`, `last_matched_line`, `log_tail`) + filtered events + `startupBlockers`; Application/Kustomization/Flux HelmRelease get reconciler status + related parsed issues; Service/Ingress/HTTPRoute/GRPCRoute/Gateway get coverage-honest [reachability evidence](reachability.md): a `summary` (headline + tested/passed/failed/skipped counts over the intended routes), `routes` (per-route outcome + `confidence` - `indirect` = reached only via the API-server proxy, not the live-traffic path), `notTested` (with why + a copyable command), an optional named `brokenRoute` when established, and `path` (hops with static findings). Optional active reachability test (`probe: true`, bounded to a 3s total budget; a timeout returns an honest partial). The returned bundle is frozen from one call, but its sources are not read atomically; preserve its limits, errors, timestamps, and confidence. In `resourceContext`, `issueSummary` uses live-operational `critical`/`warning` and, when the top issue is about another resource (for example the NetworkPolicy isolating a Service), names it in `topSubject`, while `auditSummary` uses the Checks posture-remediation ladder `critical`/`high`/`medium`/`low` (current built-ins are `high`/`medium`) and is not evidence of an active outage. | `kind` (required: workload, GitOps reconciler, or network entry kind), `group` (optional API group; set it for CRDs or same-kind collisions, for example `argoproj.io` for `Rollout`; built-in groups are inferred when omitted), `namespace` (required), `name` (required), `probe` (optional: network kinds only - run DNS/TCP/TLS/HTTP probes as applicable; explicitly non-HTTP Service ports stop at TCP), `in_cluster` (optional on the full `/mcp` endpoint only, **MUTATING** - network kinds only: runs the applicable probe layers from inside the cluster by creating up to 5 short-lived, self-deleting probe pods under your RBAC; the only diagnose option with a side effect, requires `create jobs`, `list pods`, and `get pods/log`; forces `probe` on; omitted from and rejected by `/mcp-readonly`) |
| `get_dashboard` | Cluster/namespace health overview — resource counts, failing pods, unhealthy workloads, warning-event groups (`warningGroups`, up to 20 recency-ordered; `totalWarningGroups`/`warningGroupsTruncated` signal when more exist), Helm status. Inventory-style triage before drilling in. | `namespace` (optional) |
| `top_resources` | Live metrics ranked like `kubectl top | sort`, joined with K8s context (status, restarts, owner, requests/limits). Use for CPU/memory/OOM/load symptoms. | `kind` (optional: `pods` default, `workloads`, `nodes`), `namespace` (optional), `sort` (optional: `cpu` default, `memory`), `limit` (optional, default 20, max 100) |
| `list_resources` | List resources of a kind with minified summaries + per-row `summaryContext` (managedBy / health / issueCount). Returns `{summary, next?, coverage, items}`: a sentence saying what the rows cover and what Radar did not read, what you can do to complete it when you can, then the same as data ([lists](#lists)). | `kind` (required), `group` (optional), `namespace` (optional), `context` (optional: default / `none`) |
| `search` | Find resources by content/term match (config keys, env refs, images, label values, CRD fields, webhook configs, status messages). Tokens AND'd; secret values and CA bundles never indexed. Supports `kind:`/`ns:`/`label:`/`image:` modifiers (a Kind, its plural, or a kubectl short name of a built-in such as `netpol`, `ing`, `sa`, `quota`) and CEL `filter`. Searches workloads, Pods, Services, Ingresses, IngressClasses, NetworkPolicies, ConfigMaps, Secrets (names only), PVCs, PVs, StorageClasses, HPAs, PDBs, ResourceQuotas, LimitRanges, ServiceAccounts, Roles, RoleBindings, ClusterRoles, ClusterRoleBindings, Nodes, Namespaces, Events (with `kind:Event`) and watched CRDs; a `kind:` term also searches a large CRD Radar watches only on demand. Endpoints, EndpointSlices and Leases are not searched; use `list_resources`. Trivy Operator reports match by CVE ID with package and installed/fixed versions, SBOM component, failed check ID and scanned image (`image:`); a secret finding's matched text is never indexed or reachable. Free tokens can land on different findings of one report, so use the CEL `report` binding (`report.vulnerabilities.exists(v, …)`) to match a single finding. A report is scan-time evidence: `summaryContext.managedBy` names the workload and `summaryContext.scanned` the scanned object (`missing` means Radar can't find it). By default Trivy Operator deletes an old ReplicaSet's reports after a rollout; with `vulnerabilityScannerScanOnlyCurrentRevisions: false` they stay, and `scanned.replicas: 0` usually marks one (a Deployment scaled to zero also reads 0). Trivy reports are returned only where the caller may list that report kind. Every result carries `coverage` (`status`, `narrowedBy`, `gaps`), and a partial result, an empty narrowed one, or one with hits that Radar's namespace collection or pin limited adds `coverageHint`; see [Search coverage](#search-coverage-and-cel-object-filters). | `query` (required), `filter` (optional CEL), `limit` (optional) |
| `get_resource` | Inspect one known resource: minified spec, status, and metadata, plus default-on `resourceContext` with relationships and available resource-specific summaries. Operational issues and static audit posture remain distinct; audit findings are not evidence of an active outage. See [integration documentation](integrations.md) for controller-specific context and limits. Optional supplemental data adds events, metrics, recent changes, rollback revisions, detailed issue context, or a Node drain estimate. `include=drain-plan` reuses the read-only REST drain estimator with the caller’s credentials and the removal drawer’s explicit options; it returns up to 100 current pods, full summary counts, `drainPlanTotalPods` and `drainPlanPodsTruncated`. It evaluates Eviction API/PDB eligibility, not controller-specific policy or eviction history. `include=issues` returns up to three `relatedIssues`, with `relatedIssuesTotal` and `relatedIssuesTruncated`; this is independent of `context=none`. `staleEnv` lists env vars that running containers read from a Secret or ConfigMap key and that started before that key's last data write, so they likely still hold the earlier value until restart: on a Secret or ConfigMap it names the consuming Pods, on a Pod or workload its env vars. It is attached with `context=none` too, shows up to 5 rows with `total`, needs `get` on Pods and on the source's kind, and skips volume mounts, which refresh in place. For a Secret write made before Radar started, the row comes from the Secret's managedFields (`evidenceSource: managed_fields_mtime`), names no key, and says the write may not have touched data; ConfigMap rows come only from changes Radar observed. For logs use `get_pod_logs`, `get_workload_logs`, or `diagnose` for supported kinds. | `kind` (required), `name` (required), `namespace` (optional — omit for cluster-scoped kinds), `group` (optional, for ambiguous kinds), `include` (optional, comma-separated: `events,metrics,changes,revisions,issues,drain-plan`; `revisions` lists rollback targets for Deployment/StatefulSet/DaemonSet/Rollout), `context` (optional: `basic` default, `none` to omit `resourceContext`; a Pod/template divergence note can still appear in `warnings`, and `staleEnv` still attaches) |
| `get_topology` | Whole-namespace/cluster topology graph (nodes + edges). Use `summary` format for LLM-friendly text chains. Once you have a suspect root, prefer `get_neighborhood`. | `namespace` (optional), `view` (optional: `traffic` or `resources`), `format` (optional: `graph` or `summary`) |
| `get_neighborhood` | BFS-expanded topology neighborhood around one known root — cheaper and clearer than `get_topology` for cross-resource failures (routing, selector/endpoint, refs, owner chains). RBAC-filtered. | `kind` (required), `namespace` (optional), `name` (required), `profile` (optional: `auto` default / `all`), `hops` (optional, default 1, max 2) |
| `get_events` | Recent Kubernetes events, deduplicated and sorted **Warning-groups-first then by recency** — all types by default, so warnings lead and lifecycle events follow as timeline evidence. Filter by resource kind/name to scope; `type=Warning` for warnings only, `type=Normal` for lifecycle only. A namespace the caller cannot read returns an empty list with `accessDenied: true`, so an empty result without the marker means nothing is recorded for that scope. A request with no `namespace` means cluster-wide; when the caller may read only some namespaces the answer covers those alone and says so with `partialScope: true` and `scopeNamespaces`, so a narrowed empty result is never read as a clean cluster. | `namespace` (optional), `limit` (optional, default 20, max 100), `kind` (optional), `name` (optional), `type` (optional: `all` default, `Warning`, `Normal`) |
| `get_changes` | Recent meaningful changes from the Kubernetes cluster timeline plus native Helm release deployment/operation history (`source: helm`). Use to investigate what changed before an incident, including failed upgrades, rollbacks, and current Helm revisions. If the response includes `sourcesErrored`, treat it as partial data for those sources. A namespace query without `kind` or `name` adds `clusterScopedChanges` (see [below](#cluster-scoped-admission-changes-in-a-namespace-view)). Until Radar has connected to the cluster it returns a `cluster_connecting` error rather than an empty list, as the REST API does. Use `get_helm_release include=history,operations` for the full Helm revision trail. For Deployments, StatefulSets, DaemonSets, CronJobs, Jobs, PodDisruptionBudgets, HPAs, Services, Ingresses, Gateway API objects, Argo Rollouts, admission webhook configurations, ResourceQuotas and LimitRanges, a changed desired-state field without its own row appears in the summary as `other spec changes: <fields>` (webhook configurations: `other changes`), with full paths in `fields`; a block added or removed as a whole is named once, and past 25 such fields one `spec (more fields)` row lists the rest by path. In those `other spec changes` rows, values Radar keeps out of the feed (free-form strings such as annotations, expressions and header values, anything under a container spec, and large structures) read `(value hidden)`, or `(set)` beside `null` when the field was added or removed. Dedicated rows show their own values with key-aware redaction: env values, command arguments, labels and selector values under secret-looking keys read `[REDACTED]`, and exec probe commands are hidden. A run of 5 or more plain creates in one namespace (rows with no diff or message, each within 60 s of the previous one, such as an install) is listed under `created` instead of one row each: `first` (timestamp of the earliest create), `count`, `apiVersions` (kind → apiVersion) and `objects`, one `+<seconds after first>s Kind/name` line per object, with ` (used by Kind/name, …)` for a consumed ConfigMap. Each line carries everything the row it replaces did. Lone creates, creates of NetworkPolicies, ResourceQuotas, LimitRanges, HPAs, PodDisruptionBudgets, ReferenceGrants, webhook configurations and GitOps Applications, Kustomizations and HelmReleases, and every delete and non-Secret update stay full rows; a `name=` query returns only full rows. `created` objects count toward `limit`. Each query reads at most the newest 1000 matching rows per change type (100 with `name`); when a window holds more, `narrowHint` says relevant changes may be missing. When the window starts before the history Radar holds (Radar started observing later, or deleted older events to stay within its history size or retention limit), the response carries `historyCoverage` with `incomplete`, `reasons` (`observation_started_late`, `events_evicted`) and, for callers who can read every namespace, `complete_since`; an empty answer is then not evidence that nothing changed. | `namespace` (optional), `kind` (optional), `name` (optional), `since` (optional, e.g. `1h`, `30m`; default `1h`), `limit` (optional, default 20, max 50) |
| `get_pod_logs` | Pod logs with secret redaction. Without `grep`, prioritizes errors/warnings and falls back to recent tail lines; with `grep`, returns only regex-matching lines instead of applying the diagnostic filter. Returns `container` when explicitly requested or resolved from a readable single-container Pod. If Pod metadata is unavailable, the log read still proceeds, but an unspecified container remains unknown. | `namespace` (required), `name` (required), `container` (optional; specify for multi-container Pods), `tail_lines` (optional, default 200), `grep` (optional), `previous` (optional), `since` (optional) |
| `get_workload_logs` | Aggregated logs from all pods of a workload (Deployment, StatefulSet, DaemonSet, Job, Argo Workflow). Without `grep`, auto-filters for diagnostic relevance; with `grep`, returns only matching timestamp-prefixed lines. | `kind` (required), `namespace` (required), `name` (required), `container` (optional), `tail_lines` (optional, default 100 per pod), `grep` (optional) |
| `get_cluster_audit` | `missingInputs`, when present, names unavailable inputs in the scan; zero findings do not establish that all checks passed. Static config posture — best-practice findings (Security / Reliability / Efficiency) with remediation. INDEPENDENT of operational health. Finding severity and the explicit `summary.critical`/`high`/`medium`/`low` counts are posture remediation priority; current built-in checks emit `high` or `medium`. For "what's broken right now?" use `issues`. | `namespace` (optional), `category` (optional), `severity` (optional: `critical`, `high`, `medium`, or `low`) |
| `get_cluster_upgrade_readiness` | Upgrade impact analysis for a target Kubernetes minor — the evidenced check catalog (version skew, removed/deprecated API usage across live manifests / Helm sources / API-server metrics, node runtime + cgroup evidence, drain feasibility, webhook readiness, release-specific config checks). The verdict is not a readiness guarantee: `unknown` checks had incomplete evidence, `coverage` states what the caller's RBAC could actually inspect, and `no_access` returns no verdict at all. Tier 1 is one row per check; `check=<id>` expands into findings with evidence + remediation, paged 25 at a time (`offset` + `scan_id` bind pages to one scan snapshot). Results are memoized ~60s per caller; `refresh=true` re-scans after a fix. Fix findings at the source `evidence.source` names — live objects via `patch_resource`/`apply_resource`, Helm findings in the chart, GitOps-managed resources in Git. | `target` (optional, default next minor), `check` (optional), `level` (optional: `blocker`, `warning`, `review`), `offset` (optional), `scan_id` (required when `offset` > 0), `refresh` (optional) |
| `get_runtime_versions` | Which running workloads use which runtime version, for runtime CVEs (PHP2Shell and the like). For each image a visible Pod runs, Radar reads the image config for the digest the Pod reports and the node's platform (manifest and config only, never layers) and returns the versions it declares: curated keys Docker official images set and images built `FROM` them inherit (`PHP_VERSION`, `NODE_VERSION`, `PYTHON_VERSION`, `GOLANG_VERSION`, `RUBY_VERSION`, `JAVA_VERSION`, `HTTPD_VERSION`, `NGINX_VERSION`, `TOMCAT_VERSION`, `PG_VERSION`, `REDIS_VERSION`, `MONGO_VERSION`, `MARIADB_VERSION`, `MYSQL_VERSION`, `RUST_VERSION`) plus the OCI `version` and `base.name` labels; no other env value is ever read. A declared version is what the image says, not proof of the binary, and runtimes installed from distro packages declare nothing here (Trivy reports cover those; use `search`). `fixed_in` classifies each version against the first fix on its branch: all but the last component for most runtimes (`8.3.36` covers 8.3.x, `17.6` covers 17.x), the major for Node (`22.23.4` covers 22.x), and a single branch for Rust (`1.82.0` covers every earlier version). Prereleases such as `22.23.4-rc.1` count as below their release; a prerelease of a later Node minor or Rust release is `unparsed`, since it may have been cut before the fix landed. Classifications: `declared_below_fix`, `declared_at_or_above_fix`, `branch_not_listed` (no fix was given for that branch, such as an end-of-life line; not a sign it is unaffected), `unparsed`. An image `status` other than `ok` (`no_declaration`, `auth_required`, `not_found`, `rate_limited`, `blocked`, `error`, `not_checked`) means unknown, not absent. `resolvedFrom: spec` or `tag` means the kubelet reported no registry digest, so the image named in the Pod spec is shown without a fix verdict; `platformAssumed` means the node's platform wasn't readable and linux/amd64 was used, so no fix verdict is emitted. Missing runtime declarations stay visible as `no_declaration`, counted as unknown; unsupported or distro version formats remain `unparsed`. Only Pods the caller may list are read. Each image is looked up with its Pods' own pull secrets one at a time, then anonymously; node registry credentials are not used, so images pulled only with node identity may remain unreadable; Radar's own registry credentials are used only when Radar runs locally with auth off. A result read with a credential is cached for that credential only, an anonymous one for everyone. Credentialed lookups require HTTPS, including token exchanges and redirects. The call returns within a 40-second lookup budget. Registry use is bounded: at most 4 lookups run at once across callers and 200 attempts per call, 429s are not retried and pause that registry for 5 minutes, configs over 1 MiB are refused, and only the registry a Pod names may sit on a private or (outside the cluster) loopback address; link-local addresses, and redirects or token endpoints elsewhere on internal addresses, are refused. Behind an HTTP proxy, HTTPS goes through a CONNECT tunnel to the checked address; requests that can't be pinned that way are refused when Radar runs in-cluster. `fixed_in` needs `runtime`. Unknown runtime names and unsupported fixed-release formats return input errors. Manifest GETs can consume registry pull quotas shared with workloads, including Docker Hub account or egress-IP limits; a cold inventory can spend multiple manifest requests per image. Transient lookup failures are cached for 30 seconds. | `namespace` (optional), `runtime` (optional, e.g. `php`), `fixed_in` (optional, comma-separated, e.g. `8.2.35,8.3.36,8.4.27,8.5.12`), `limit` (optional, default 50, max 200) |
| `list_packages` | Installed packages (Helm releases, label-managed workloads, CRDs, Argo Applications, Flux HelmReleases + Kustomizations) with source provenance, versions, and health, in one call. Response includes `sourceLegend` for the stable source codes. | `namespace` (optional), `source` (optional: `H`/`helm`, `L`/`labels`, `C`/`crds`, `A`/`argocd`, `F`/`fluxcd`), `chart` (optional substring) |
| `list_helm_releases` | List Helm releases with status, resource health, storage namespace, Flux ownership, current `lastOperation`, and a capped `operations` trail when Helm history indicates failed upgrades, rollback-after-failure, rollbacks, or stuck pending operations. Use this first for Helm deployment debugging. | `namespace` (optional) |
| `get_helm_release` | Returns the release with its owned resources, `hooks` with their outcome, `history` (the newest ten stored revisions; `include=history` returns them all) and the user-supplied `values` (redacted). Detailed Helm release info with owned resources, resource health, Flux ownership, current `lastOperation`, `operationInsight` (active/recovered state, likely resource to inspect, suggested compare), hooks, and failed/running hook diagnostics with live Job/Pod/Event/redacted-log evidence when still available. Use `include=history,operations` for the full Helm revision trail; `include=values` for key-aware redacted user values; `include=diff,values_diff,notes_diff,resource_diff` for revision comparison. Manifest `diff` redacts Secret values while preserving key changes; `notes_diff` uses pattern-based redaction. For releases with `storageNamespace`, pass that value as `namespace`. | `namespace` (required: Helm storage namespace), `name` (required), `include` (optional: `values,history,operations,diff,values_diff,notes_diff,resource_diff`), `diff_revision_1` (required when `include` contains a diff token) / `diff_revision_2` (optional, defaults to current) |
| `list_namespaces` | List all namespaces with status | (none) |
| `get_subject_permissions` | Effective RBAC permissions of a ServiceAccount / User / Group. Without `verb`/`resource`, returns the existing bindings, deduplicated flat rules, and (for SAs) Pods using it. With both fields, runs an authoritative SubjectAccessReview for a ServiceAccount and returns a focused allow/deny result; this requires the caller itself to have `create subjectaccessreviews.authorization.k8s.io`. | `kind` (required: `ServiceAccount`, `User`, or `Group`), `namespace` (required for ServiceAccount; omit for User/Group), `name` (required), `verb` + `resource` (optional pair; ServiceAccount only), `group`, `resource_namespace` (defaults to subject namespace; explicitly empty for a cluster-scoped or cluster-wide API request, not aggregation of namespace-local grants), `subresource`, `resource_name` (optional check refinements) |
| `query_prometheus` | Execute PromQL against the cluster's Prometheus (auto-discovered or `--prometheus-url`; works with PromQL-compatible backends: Thanos, VictoriaMetrics, Mimir). `type=instant` returns current values; `type=range` returns time-series history with automatic step adjustment. Empty results include a bounded list of related active metric names when a metric family can be inferred. Oversized results return a label-cardinality summary + suggested `topk` rewrite instead of raw data. Backend qualifications appear in bounded `warnings` and `infos`, with `annotationsOmitted` for overflow and `partial` when stores did not all answer; common PromQL errors include corrective hints. Every response carries `selectors`, the vector selectors the query reads (metric + label matchers), with `selectorsUnknown: true` when the query could not be tokenized and its scope must be treated as unbounded. | `query` (required), `type` (optional: `instant` default, `range`), `since` (optional, e.g. `30m`, `1h`, `24h`, `7d`; default `1h`), `start` / `end` (optional RFC3339, override `since`), `step` (optional, auto-calculated when omitted), `max_points` (optional, default 300, max 600), `timeout` (optional seconds, default 30, max 180) |
| `discover_metrics` | Discover exact metric names (enriched with type/help from Prometheus metadata) or values of one label before writing PromQL. Lists active series from the last hour; `truncated: true` means narrow the `match` selector. Scoped metric-name regex searches may include bounded `nearMisses` that the fully anchored pattern excluded, with an anchoring hint; these examples are not a complete list. | `match` (PromQL series selector; required when `label` is empty), `label` (optional: list values of this label instead of metric names), `limit` (optional, default 100, max 500) |
| `get_prometheus_rules` | List Prometheus alerting/recording rules with PromQL definitions, state, labels, annotations, and active alert instances. Alert-investigation entry point: fetch the rule definition, then run its query with `query_prometheus`. | `type` (optional: `alert`, `record`), `name` / `group` (optional substring filters), `state` (optional: `firing`, `pending`, `inactive`), `limit` (optional, default 50, max 200) |
| `get_cost` | Estimated costs from OpenCost or Kubecost: namespace summary, workload/node breakdown, or summarized trend. Hourly rates and 730-hour projections; availability, scope and missing evidence are explicit. See [evidence limits](#cost-and-rightsizing-evidence-limits). | `view` (summary default, workloads, nodes, trend), `namespace` (required for workloads; filters summary/trend), `kind` + `name` (one workload), `name` (one node), `range` (trend: 6h, 24h default, 7d), `include_points` (trend only, default false), `limit` (row views: 1–100, default 20) |
| `get_rightsizing` | CPU/memory request recommendations from 7 days of usage. Workload scope is cheap; namespace/cluster scans run in the background for up to three minutes. Running responses include `scanId`, `scanStatus`, and `nextCall`; follow that call to retrieve progress without restarting completed batches. Scans are retained up to 15 minutes; use `refresh=true` after changes. Results prioritize evidenced safety risks, then increases, reductions, and routine reviews; ties use replica-weighted request delta. Check confidence and manual-review reasons; reductions do not directly imply bill savings. | `scope` (required: workload, namespace, cluster), `kind` + `name` (workload), `namespace` (workload or namespace scope), `namespaces` (namespace scope only, nonempty array; mutually exclusive with namespace), `include_all` (default false; workload always returns all), `classification` (scan only: reduction, increase, review, need_data, in_range), `limit` (scan only: 1–100, default 20), `scan_id` (retrieve one scan with the same scope/namespaces), `refresh` (scan only; start fresh, cannot combine with scan_id) |

API group is part of a resource's identity. Radar infers the canonical group for built-in kinds, but callers should pass `group` for a supported CRD or whenever a Kind can exist in more than one group. For example, diagnose an Argo Rollout with:

```json
{"kind":"Rollout","group":"argoproj.io","namespace":"prod","name":"checkout"}
```

This selects the Argo Rollout workload path; it does not imply that `diagnose` supports arbitrary custom resource shapes.

Running, non-terminating Pods blocked by a declared readiness gate appear as `readiness_failed`
after five minutes with `Ready=False/ReadinessGatesNotReady` and
`ContainersReady=True`. Each transition gets that grace, so a long startup does
not immediately become a gate failure. False, Unknown or absent declared gate conditions
qualify because the kubelet explicitly reports blocked readiness. Onset uses the later transition only when both
are known. A missing transition uses creation time only for grace; with neither
time available, no grace is assumed. Up to five minutes of future clock skew
gets grace; farther-future times establish neither grace nor onset. A separate sane recent
transition still receives its grace. The same state appears in Pod health and
summary rows.

Watched, synchronized custom resources that remain deleting appear in `issues`
as `termination_stuck`, timed from the deletion request. Radar starts no new
watches for this scan; an empty result does not establish that unwatched kinds
are healthy. These findings also feed Active Issues and AI resource/search
summaries, but not dashboard health summaries or `cluster://health`.

A `Ready=False/Deleting` condition is expected during the first 10 minutes of
an actual deletion. This follows Radar's termination warning window, not a
Kubernetes deadline. Other failure conditions remain eligible under the existing
condition selector, including its suspension and whole-object generation gates.
The same window tolerates an API-server deletion timestamp slightly ahead of
Radar's clock; a farther-future timestamp does not establish expected progress.
After that window, or when deletion timing is unavailable, the condition remains
a fallback when no stuck-deletion finding is available. It never takes priority
over an independent error, including a qualified older-generation observation.
Deletion itself advances a custom resource's generation. If its top-level
`status.observedGeneration` then lags, the existing generation gate can withhold
even a pre-deletion error; only a readable stuck-deletion finding covers the
prolonged deletion until that gate clears.

Findings start at warning after 10 minutes. At 30 minutes, controller-owned
finalizers follow this policy:

| Controller evidence | Severity | Next step |
|---|---|---|
| Recognized controller is fully ready | Warning | Check logs, permissions and deletion-blocking dependencies. No removal. |
| Recognized controller is installed but not fully ready | Critical | Repair the controller so it can resume cleanup. No removal. |
| No recognized controller, or all recognized copies are scaled to zero | Warning | Restore it if it should be running. Removal may be offered conditionally. |
| Owner or controller state cannot be established | Warning | Identify and check the controller. No removal. |

A fully ready controller for any finalizer keeps a mixed-finalizer finding at
warning; repair advice for other controllers remains. In-use guards and
GC-only finalizers can reach critical at 30 minutes. Resolve their dependencies
rather than removing them; orphaning preserves dependents.
REST/UI deletes preserve a policy recorded by a `foregroundDeletion` or `orphan` finalizer; otherwise they explicitly request Background propagation, even for terminating resources or when the pre-delete read fails.

The catalog recognizes Argo CD, Flux, VictoriaMetrics and Karpenter through
known finalizer keys and workload identities. Name-only guesses for uncataloged
finalizers never establish ownership. Renamed installs and mirrors can be
recognized through published images; controller-specific identity rules are in
[`pkg/finalizers/catalog.go`](../pkg/finalizers/catalog.go).

Ordinary namespaced subjects require namespace access; Secrets also require
Secret access, and cluster-scoped subjects require list on their exact kind.
Controller evidence follows namespace access for namespaced reads and exact-kind
list for cluster-wide searches. A missing or down controller requires complete
cluster-wide Pod, Deployment,
StatefulSet and DaemonSet visibility. Incomplete inventory cannot enable removal.
A negative search is not proof of an uninstall; a controller may run elsewhere
or use an identity Radar does not recognize.

Only Flux and VictoriaMetrics currently opt into `conditional_cleanup`, separate
from `action`. It contains `condition`, `agent_guidance`, `caution`, and `removals`
with one entry per finalizer (`finalizer`, `patch_resource`, `kubectl`). An agent
may propose removal only after the user confirms intentional uninstall or a
permanent stop. The UI keeps these commands in a collapsed conditional section.

Each command defaults to a server dry run and tests the object's UID and the
finalizer at its index. Review the preview before applying; re-read after every
removal because indices change. Removal bypasses controller cleanup and can
leave Kubernetes resources, infrastructure or storage behind. Dependents with
their own finalizers can remain stuck even after the controller is restored;
older VictoriaMetrics operators place such finalizers on managed objects.

### Search coverage and CEL object filters

`search` scans typed resources (including RBAC, ServiceAccounts, NetworkPolicies,
IngressClasses, LimitRanges and ResourceQuotas) and discovered dynamic kinds,
including admission webhook configurations. A broad search covers the kinds Radar
watches. Large custom kinds that Radar doesn't watch until someone asks are searched
only when a `kind:` term names them: an explicit `kind:` query starts the existing
bounded on-demand watch and waits briefly for initial sync once CRD discovery is
ready. Leases, Endpoints and EndpointSlices, which Radar reads directly instead of
watching, are never warmed; use `list_resources` for them. Alternate built-in groups
of typed kinds are skipped, so `kind:Event` does not duplicate core Events through
`events.k8s.io`. Third-party CRDs named Event are searched normally; only built-in
Events are excluded from broad searches.

REST `GET /api/search` accepts `namespaces` (comma-separated) or `namespace`
(single); the plural parameter takes precedence. These are explicit namespace
names, combined as a union with any `ns:` terms in `q`. Hidden, missing and
outside-pin names produce the same coverage gaps as their `ns:` spelling. An
empty parsed list adds no names.

Every response includes `coverage`: `{status, narrowedBy?, gaps}`, built from the
same [gaps and codes](#resource-reads-coverage-and-errors) as resource lists. A
gap is either about kinds or about namespaces, and states both axes: `kinds`
(`{count, names}` of `{kind, group}`, an omitted group being core) and
`namespaces`, each listing every member, past its limit only counting them, or
giving an extent: `{"extent": "all"}` for every one in the search's scope, or
`{"extent": "unknown"}` when Radar can't say which. A kind gap reaches all
namespaces when the kind failed everywhere and an unknown part when only some
namespaces failed; a namespace gap reaches all kinds only when every searched
kind was excluded in those namespaces, and otherwise an unknown subset of kinds.
A gap from an unreadable catalog reaches an unknown set of kinds.
`filter_eval_failed` names `objects` whose CEL filter evaluation failed.
`findings_scan_incomplete` lists report `kinds` with
`namespaces: {"extent": "unknown"}` when the findings scan runs out of time,
because findings in unscanned reports may still match.

**`status: "partial"` means this result can't rule out a match for what you
asked: something was loading or failed, a kind or namespace you named couldn't
be searched, or the filter couldn't be evaluated on some objects. `complete`
means no such problem was reported. A broad query searches only what your
access and Radar's collection allow without listing each kind it skipped, so
`narrowedBy` says when that happened (`user_access`, `namespace_pin`,
`namespace_collection`, `kind_collection`): even a complete
search is then no proof that nothing exists outside it.** `kind_collection` means
kinds Radar isn't watching were skipped; naming one with `kind:` searches it.
`namespace_collection` means namespaces Radar doesn't collect were skipped. Result limits (`total` vs `total_matched`, MCP
`narrowHint`) are separate.

What gets reported follows from that:

- **Your access and Radar's setup are the scope of a search, not gaps in it.** A
  kind-level access, setup or query reason (`user_forbidden`, `radar_forbidden`,
  `kind_unknown`) is a gap only for a kind a `kind:` term names; on a broad query
  it adds to `narrowedBy` instead. A namespace reason is a gap only for a
  namespace an `ns:` term, a REST namespace parameter, or the MCP `namespace`
  argument names. So `ns:` alone never yields per-kind gaps, and `kind:` alone
  never yields namespace or collection-scope gaps. A named kind Radar's setup leaves out is
  `radar_kind_not_collected`. Kinds search never covers are outside every search,
  so on a broad query they don't add to `narrowedBy`: Endpoints, EndpointSlices and
  Leases, which Radar reads directly instead of watching, are `kind_not_searchable`
  (read them with `list_resources`), and APIs that can't be listed and watched are
  `kind_not_watchable`.
- **Loading and failures are always reported, folded.** All `kind_sync_pending`
  kinds become one gap whose `kinds` counts distinct kinds and names none. Each
  failure reason (`kind_sync_timed_out`, `read_error`,
  `user_permission_check_failed`, `radar_credentials_rejected`) names its kinds when
  there are at most five, and otherwise only counts them. A query with `kind:` terms
  names every kind it reports, exactly.
- **Only kinds you may read count, and your access comes first.** Your own access is
  checked before a kind's loading, failed or collection state is reported, so a kind
  you can't list never shows up as loading for you, and a named kind that neither
  you nor Radar can read is `user_forbidden` (yours to fix). Radar's own denial
  (`radar_forbidden`) is reported only when you could read the kind. A kind
  search never covers (`kind_not_searchable`, `kind_not_watchable`) is reported
  without a permission check.
- **While discovery is loading** (API discovery, or the CRD discovery that decides
  which custom kinds Radar watches), the response carries a `kind_sync_pending`
  gap whose kinds are `{"extent": "unknown"}` (the set isn't known yet) and no `kind_unknown`: a `kind:`
  term that matched nothing may simply not be discovered yet. When API discovery
  failed for some groups, or its last refresh failed and retained an older snapshot,
  an unmatched `kind:` term is still `kind_unknown`, beside a `read_error` gap whose
  kinds are unknown: the kind may live in a group Radar couldn't discover. For broad searches, incomplete discovery adds `kind_collection`
  to `narrowedBy`. Search reads the in-memory discovery snapshot without refreshing it.
- **A kind Radar doesn't watch yet narrows a broad search** (`kind_collection`),
  for example one too large to watch eagerly; a `kind:` term warms it and reports
  what happens. An access check Radar is still retrying is `kind_sync_pending`;
  one that failed for a reason other than a refusal, or never answered once its
  retries ran out, is `read_error`, which naming the kind retries. Neither is read
  as a deliberate limit. A watch that started but hasn't finished loading by its
  deadline is `kind_sync_timed_out`, which naming the kind can't restart. A
  namespace Radar's own credentials can't list stays outside its collection: on a
  broad search it adds `namespace_collection` to `narrowedBy`, and it is
  `radar_namespace_not_collected` when an `ns:` term names it.
- **Named kinds may warm within one shared two-second deadline.** Broad searches
  do not wait for warm-up. Kinds still loading are reported as `kind_sync_pending`.
  The per-hit `summaryContext` (health, issue count, managedBy) is left out when it
  can't be built within about three seconds, as right after a restart while
  custom-resource discovery runs.
- **A kind Radar watches namespace by namespace is searched where it has
  loaded.** A namespace whose informer is still loading is a `kind_sync_pending`
  gap for the kind, and the namespaces that are ready still contribute hits.
- **When your namespace access couldn't be determined**, namespaced kinds are not
  searched (fail closed) and the result reports `user_permission_check_failed` (per named
  kind, or one gap whose kinds are unknown), never a namespace code.

| Code | Reported | Meaning | Next step |
|---|---|---|---|
| `kind_sync_pending` | always (folded) | The kind or discovery is still loading, or warming ran out of time. | Retry in a few seconds. |
| `kind_sync_timed_out` | always (folded) | The kind's initial load missed its deadline; Radar keeps trying. | Retry later; if it persists, inspect Radar's connection and list/watch access. |
| `read_error` | always (folded) | A list or discovery failure, or an access check that failed or never answered. | Retry; on a broad search, `kind:` re-checks the kind. |
| `user_permission_check_failed` | always (folded) | Radar could not complete your permission check and failed closed. This is not a denial; authorized namespaces still contribute hits. | Retry. |
| `radar_credentials_rejected` | always (folded) | Kubernetes rejected Radar's own credentials for the kind. | Whoever runs Radar checks its credentials. |
| `user_forbidden` | named `kind:` | Your role cannot list the kind in some or all of the searched namespaces. | Ask for access; authorized namespaces still contribute hits. |
| `radar_forbidden` | named `kind:` | Kubernetes refused Radar's own identity for the kind. | Fix Radar's access; granting the caller access does not help. |
| `radar_kind_not_collected` | named `kind:` | Radar's setup leaves the kind out. | Use `list_resources`, or change Radar's collection. |
| `kind_not_searchable` | named `kind:` | Search doesn't cover the kind: Radar reads Endpoints, EndpointSlices and Leases directly instead of watching them. | Use `list_resources` or `get_resource`. |
| `kind_not_watchable` | named `kind:` | The API can't be listed and watched, so Radar can't collect it. | Read it from the Kubernetes API directly; warming will not help. |
| `kind_unknown` | named `kind:` | A `kind:` term matched no kind in the discovered catalog: it isn't installed on this cluster, or it is misspelled. Reported in your spelling. | Check the spelling, and whether the API is installed here. |
| `namespace_not_found` | named `ns:` | The namespace doesn't exist on this cluster. Only a caller who can see every namespace is told this. | Check the name. |
| `user_namespace_not_visible` | named `ns:` | The namespace isn't among the namespaces you can see, or doesn't exist; the code doesn't say which. | Use a namespace you can see, or ask for access. |
| `outside_namespace_pin` | named `ns:` | Radar's `--namespace-scope` pin leaves the namespace out. | Radar's `--namespace-scope` setting. |
| `radar_namespace_not_collected` | named `ns:` | Radar's cache left kinds unsearched there: `kinds.extent` is `all` only if every searched kind was excluded, otherwise `unknown`. | Widen Radar's collection. |
| `filter_eval_failed` | when the filter fails | The `filter=` expression couldn't be evaluated on some objects Radar read. | Guard missing fields with `has()`. |
| `findings_scan_incomplete` | when the findings search ran out of time | Radar stopped searching Trivy report findings when its time budget ran out, so a CVE or package may be in a report it left out. | Narrow with `kind:`, `ns:` or a more specific term. |

A namespace gap lists the namespaces you named where kinds were missed for that
reason, once per code, never per kind. Every combination of its axes was missed;
`kinds: {"extent": "unknown"}` does not qualify kinds successfully searched there.
A named namespace that doesn't exist is
`namespace_not_found` for a caller whose RBAC covers every namespace, never
searched to a silent empty answer or blamed on the pin (Radar watches
Namespaces cluster-wide, even under the pin; when it can't list them, existence is
unknown and the namespace is treated as existing). For anyone else existence is
never checked, so a namespace outside your RBAC is `user_namespace_not_visible`
whether or not it exists. Kubernetes refusing Radar's own identity is
`radar_forbidden` or `radar_credentials_rejected`: either way it is Radar's access
to fix, not yours.

On a broad search, read-error hints suggest a `kind:` search to re-check; a search that already names the kind reports the failure without
suggesting the same query.

On MCP, a partial result also carries `coverageHint`: an explanation per reason
present, most actionable first (a typo, an unsearchable kind, a failing filter,
loading), at most three sentences and then `+N more reasons.`. A narrowed broad
search with no hits carries one too, saying it doesn't rule out matches outside
your access and Radar's collection. A search with hits says so when Radar
collects some kinds only in some namespaces, or is pinned to one, since those
limits come from how Radar is installed; the caller's own access is not
repeated. A named kind denied across the whole
namespace scope suggests trying `ns:<namespace>`; namespace access may differ,
so this is a suggestion, not a promise of access. REST never carries it.
The response is one JSON value, with human-readable `coverageHint` and
`narrowHint` (when present) before the hits, as with `list_resources`.

```json
{"coverageHint": "37 kinds were still loading; retry in a few seconds before concluding absence.",
 "coverage": {"status": "partial", "gaps": [{"code": "kind_sync_pending", "action": "retry", "kinds": {"count": 37}, "namespaces": {"extent": "all"}}]}, …}
{"coverageHint": "Your access doesn't include all Secrets; this result says nothing about the rest. Try narrowing with ns:<namespace>.",
 "coverage": {"status": "partial", "gaps": [{"code": "user_forbidden", "action": "user_access", "kinds": {"count": 1, "names": [{"kind": "Secret"}]}, "namespaces": {"extent": "all"}}]}, …}
{"coverageHint": "Namespace 'payments-prd' doesn't exist on this cluster.",
 "coverage": {"status": "partial", "gaps": [{"code": "namespace_not_found", "action": "change_request", "kinds": {"extent": "all"}, "namespaces": {"count": 1, "names": ["payments-prd"]}}]}, …}
{"coverageHint": "No matches within what your access and Radar's collection cover; that does not rule out matches outside them.",
 "coverage": {"status": "complete", "narrowedBy": ["user_access"], "gaps": []}, …}
{"coverageHint": "For some kinds, Radar searches only the namespaces it collects; matches for them elsewhere aren't included.",
 "coverage": {"status": "complete", "narrowedBy": ["namespace_collection"], "gaps": []}, "hits": [ … ]}
```

`ns:` (and the MCP `namespace` argument) restricts namespaced kinds. A broad `ns:`
query covers namespaced objects only. A cluster-scoped kind named with `kind:`
ignores `ns:`, as kubectl ignores `-n` for it. RBAC namespace limits and
`--namespace-scope` never hide cluster-scoped objects: each cluster-scoped
kind is gated only by the caller's own list permission for it.
Coverage is bounded by the discovered catalog; it cannot describe undiscovered APIs.
Explicit searches share one two-second warmup deadline across matching dynamic
kinds and namespaces, including permission probes and initial sync. The deadline
bounds only how long the search waits: a permission probe other requests share
keeps running, and the watch it grants still starts. A requested namespace can start
its own watch even when another namespace is already cached. Unfinished warming
reports `kind_sync_pending` and preserves cached hits from covered namespaces.
Readiness is judged per namespace: a namespace whose watch is still loading reports
`kind_sync_pending` (`kind_sync_timed_out` once it misses its deadline) without hiding
hits from namespaces that are ready.

Ordinary namespaced kinds, including dynamic CRDs, use the same namespace visibility
policy as resource lists. Secrets, Roles, RoleBindings, LimitRanges and Trivy reports
use exact caller list gates. These gates try one cluster-wide check, then bounded parallel namespace
checks only after a denial. Cluster-scoped kinds use an exact cluster-scoped check.

`features.searchCoverage` advertises the coverage response and `object` binding so
embedding hosts can gate them when connected to an older Radar.

CEL exposes `object` (the cached object with Secret data/stringData removed)
alongside the existing `kind`,
`apiVersion`, `metadata`, `spec`, `status`, `labels` and `annotations` shortcuts,
and `report`: a Trivy report's findings, empty for every other object.
Shortcuts project from that same object, preserving fields such as node placement,
Pod IPs, UIDs, generations, finalizers and environment values. Core Secret
`data`/`stringData` are structurally absent; CEL does not apply Detail pruning or
environment-value redaction.
`metadata.namespace` is `""` for cluster-scoped objects. Other missing fields retain
CEL's normal error semantics; no optional-type extensions are enabled.

Examples:

- RBAC top-level fields: `has(object.subjects) && object.subjects.exists(s, s.kind == "ServiceAccount")`
- Label membership: `"app" in labels && labels["app"] == "cart"`
- Multi-source Argo: `kind == "Application" && has(spec.sources) && spec.sources.exists(s, has(s.repoURL) && s.repoURL.contains("github.com"))`

`has()` takes a field selection (`has(object.subjects)`), not a bare variable
(`has(subjects)`). Use map membership for label keys. Kubernetes omits fields that
are zero or empty (`status.readyReplicas` on a Deployment with none ready), so guard
them with `has()`, and match a zero with `!has(x) || x == 0` under a kind check, since
the bare form also matches every object without that field. Deployments with zero or
unreported ready replicas:
`kind == "Deployment" && (!has(status.readyReplicas) || status.readyReplicas == 0)`. Evaluation/activation errors make the result partial, preserve
`filter_errors` and `filter_error_sample`, and add a `filter_eval_failed` gap whose
`objects` name the failing `{kind, group, namespace, name}` refs (no namespace at
cluster scope) when there are at most 20, and otherwise only count them.
Surviving hits remain available. A valid false predicate is an ordinary non-match.

For `issues`, `unchecked_sources` lists the issue sources Radar couldn't check for this answer, so their issues may be missing; don't read the response as healthy for them. Sources: `helm` (failed or stuck Helm releases), `resources` (built-in kinds such as Pods, workloads, PVCs, HPAs and Nodes), `custom_resources` (failing conditions on custom resources) and `karpenter` (NodePools, NodeClasses, NodeClaims). Reasons: `read_failed`, `kind_sync_pending` (still loading, for example right after Radar starts; ask again shortly), `kind_sync_failed` (loading stalled), and `collector_access_denied` (Kubernetes refused Radar's own identity, so the caller's permissions can't fix it). An entry may carry `kinds` (only those kinds went unchecked) and `namespaces` (only those namespaces failed; the rest are complete). Treat any other source or reason as unchecked too. Read `timing_summary` when present; it explains timing combinations that are easy to misread without schema context. The raw provenance fields remain available for filtering. `first_seen` is an evidence-backed lower bound, `onset_unknown` means no contributing signal has a known onset, and `resource_created_at` is resource-age context rather than issue age. A missing `first_seen` is exposed to CEL as `0`; require `first_seen != 0` for any age filter, and also require `onset_coverage_unknown == 0` when the whole row must have exact timing.

Resource context distinguishes observed dependency references (`dependencies` and
`dependents`) from autoscalers (`scaledBy`). A Certificate depends on its issuer;
it does not scale it. These references carry API group and namespace identity,
are filtered by the caller's reference permissions, and do not establish that
a controller has successfully reconciled the dependency.

A KEDA ScaledObject summary's `target` includes its API group, for example `Deployment (apps)/web`. Missing or empty target kind and API version default to `Deployment` and `apps/v1`; a `Rollout` target without an API version is reported as `Rollout (apps)/worker`.

HPA `hpaSummary.metrics` matches current values to targets by source and selector. Targets without a current value have `status: missing`; unmatched observations have `status: status_only`. Core Namespace metric names use the HPA's namespace.

Resource context exposes routing direction through `exposedBy` (upstream
resources routing to this resource, including entrypoints reached through a
selecting Service) and `backends` (resources this routes to). The compatibility
field `exposes` is their union and does not imply a direction. Pod containment
shortcuts on Deployments and Nodes are excluded from routing backends. All three
fields carry exact group/kind/namespace/name references and apply the caller's
reference permissions independently. Monitoring uses `monitors` and
`monitorTargets`; policy selections use `protects`, while non-enforcing staged
policies use `stagedPolicies` rather than `selectedBy`. Cluster API Clusters
expose their `healthChecks`; a MachineHealthCheck's Cluster is a dependency.

Resource context's `uses` includes declared PodSpec references from regular,
init and ephemeral containers, projected volumes, image-pull credentials and
volume-driver credentials. `referencedBy` on core ConfigMaps, Secrets, PVCs and
ServiceAccounts includes both workload templates and actual Pods, including
controlled Pods whose admitted spec may differ from the current template.
ReplicaSet templates remain separate evidence. References are scoped and
permission-filtered, with field paths and a bounded visible-only count; they
describe declarations rather than proving a successful mount or active read.
Capped results round-robin readable resource kinds, preserving examples of
workload templates, revision templates and admitted Pods even with many replicas.
If any consumer inventory fails, `omitted` marks `referencedBy` as `unavailable`.
References from inventories that loaded remain available; an empty result with
this omission does not establish that no consumers exist.
Only named PodSpec declarations are included: implicit workload defaults and
template-generated PVC names are not inferred. An absent omission does not by
itself establish cache freshness or complete namespace coverage.

Resource context's `ingressSummary.tlsSecrets` lists named TLS Secrets in the
Ingress's namespace. A Secret's `referencedBy` includes readable Ingresses with
the path `spec.tls[].secretName`. Resource-view topology links only observed,
visible TLS Secrets. Empty Secret names do not resolve controller defaults.

When a Service's selected Pods come from more than one workload, resource
context's `serviceSummary.workloads` lists each workload with its Pod and ready
counts, and `diagnose` adds the same breakdown to the Pods hop as an info finding,
for example "Selects 2 pods from 2 workloads: frontend (1), search (1)". This is
context only. Workloads the caller can't read are withheld, and `omitted` says so.

Radar raises a `service_mixed_backends` warning, never critical, and resource
context adds the `mixed_backends` warning with the same sentence in
`serviceSummary.mixedBackends`, only when the workloads that receive traffic don't
all serve the Service:

- **Different applications**: they share no image, compared by name without
  registry, tag or digest. "Service X routes to N different applications (…)".
- **A port some don't declare**: some declare the numeric targetPort while others
  declare other ports but not it. "Service X routes to N workloads; Y's pods don't
  declare port P".

Deliberate setups stay quiet: stable and canary Deployments, blue/green cutovers,
Argo Rollouts, versions of one app (a shared `app` label and a `version` label on
every Pod), mesh sidecars and headless Services. A new tag or registry is the
same image, and a workload that declares no ports is not evidence. In the MCP `issues` tool
the warning's `correlated_changes` show recent edits to the Service, such as a
selector change.

### Write Tools

| Tool | Description | Parameters |
|------|-------------|------------|
| `apply_resource` | Create or update a Kubernetes resource from YAML. Supports multi-document YAML, per-document partial-failure results, server-side dry-run preview, and SSA ownership-conflict reporting. | `yaml` (required), `mode` (optional: `apply` or `create`, default `apply`), `dry_run` (optional, default false), `namespace` (optional, override), `verify` (optional, default true: real mutations return post-mutation state, submitted-vs-live diff, workload rollout/pods, and related issues; dry runs return a preview diff), `force` (optional, default false: take SSA field ownership from other managers) |
| `patch_resource` | Patch one existing Kubernetes resource with JSON Patch, JSON Merge Patch, or strategic merge patch. Use for precise field/list edits when you know the exact path and do not want to rewrite the full manifest or take broad server-side-apply ownership. Strategic patch is for built-in Kubernetes kinds and name-keyed list edits, such as changing one container. | `kind` (required), `name` (required), `namespace` (required for namespaced resources), `group` (optional), `patch_type` (optional: `json` default, `merge`, or `strategic`), `patch` (required JSON string), `dry_run` (optional), `verify` (optional, default true: real mutations return compact post-patch state and JSON Patch field checks; dry runs return a preview diff) |
| `manage_workload` | Restart, scale, or rollback a Deployment, StatefulSet, DaemonSet, or Argo Rollout. Note: `scale` is not supported for DaemonSets. On a Rollout, `rollback` rewrites the pod template and therefore starts a **new** rollout that re-runs every canary step, pause, and analysis — use `manage_rollout` `abort` to revert traffic immediately, or follow the rollback with `promote-full`. | `action` (required: `restart`, `scale`, `rollback`), `kind` (required), `namespace` (required), `name` (required), `replicas` (for scale), `revision` (for rollback — enumerate with `get_resource include=revisions`) |
| `manage_rollout` | Drive an Argo Rollout's progressive delivery — the verbs with no Deployment equivalent. `abort` reverts traffic to the last stable version at once without touching spec and stays aborted until retried (the incident-response verb); `retry` clears an abort; `promote` clears the current pause to advance one step; `promote-full` skips all remaining steps, pauses, and analysis (emergency hotfix) — it waits for the controller to observe the current pod template first, so straight after a rollback it may report that the controller has not caught up, which is safe to retry; `skip-step` advances exactly one canary step (canary only). Rollback / restart / scale go through `manage_workload` with `kind=rollout`. | `action` (required: `abort`, `retry`, `promote`, `promote-full`, `skip-step`), `namespace` (required), `name` (required) |
| `manage_cronjob` | Trigger, suspend, or resume a CronJob | `action` (required: `trigger`, `suspend`, `resume`), `namespace` (required), `name` (required) |
| `manage_gitops` | Manage ArgoCD and FluxCD resources — sync, refresh, terminate, suspend, resume, rollback (Argo), reconcile (Flux), reconcile-with-source (Flux) | `action` (required), `tool` (required: `argocd` or `fluxcd`), `namespace` (required), `name` (required), `kind` (FluxCD only). For `sync`: `revision`, `prune`, `dry_run`, `force`, `apply_only`, `sync_options`. For `rollback` (Argo only): `history_id` (required), `prune`, `dry_run`. Per-action input validation rejects flags that don't apply to the action (e.g. `force` on `suspend`) so callers fail loudly instead of silently. |
| `manage_node` | Cordon, uncordon, or drain a Kubernetes node. A drain waits by default until the evicted pods are deleted, and reports `outcome` as `complete`, `incomplete`, `unconfirmed` or `started`. Incomplete/unconfirmed results return `partial`: check `errors`, observed `pendingPods`, `unconfirmedPods` and `verificationError`. Expected DaemonSet, static and terminal exclusions are listed in `skippedPods` with `expectedSkip: true`; they do not prevent completion. Completion confirms required workload removal, not overall shutdown safety | `action` (required: `cordon`, `uncordon`, `drain`), `name` (required), `delete_empty_dir_data` (optional, default true), `force` (optional), `wait_for_deletion` (optional, default true), `timeout` (optional, seconds; default 2 minutes when waiting, 60 otherwise) |

Optional write tool (off by default):

| Tool | Description | Parameters |
| --- | --- | --- |
| `exec_pod` | [One non-interactive container command](#exec_pod), enabled by `--mcp-exec-tools` | `namespace`, `name`, `command` (required argv array); `kind`, `container`, `timeout_seconds` (optional) |

## Strimzi connector evidence

See [Strimzi Kafka connector evidence](integrations.md#strimzi-kafka-connectors) for the Issues/API/MCP coverage, required read access, and operator-snapshot limitations.

## Available Resources

| URI | Description |
|-----|-------------|
| `cluster://health` | Cluster health summary (same data as `get_dashboard`) |
| `cluster://topology` | Full cluster topology graph |
| `cluster://events` | Recent warning events (up to 50) |

## Security

- **Safe by design** — most read tools do not change cluster state and are annotated with `readOnlyHint`. On the full `/mcp` endpoint, the exception is `diagnose(in_cluster=true)` on network entry resources, which creates up to five self-deleting probe pods; that full diagnose tool is annotated `readOnlyHint: false` and `destructiveHint: false`, so clients that prompt only on `destructiveHint` will not prompt for it. On `/mcp-readonly`, diagnose omits and rejects `in_cluster` and carries `readOnlyHint`. Write tools (restart, scale, rollback, sync, apply, cordon/drain) are RBAC-enforced and annotated with `destructiveHint` so AI clients can prompt for confirmation. Some are genuinely destructive — `apply_resource force=true` can take field ownership from Helm/Flux, `manage_node drain` evicts pods, and `rollback`/`terminate` overwrite or abort desired state
- **RBAC-aware** — every call enforces RBAC at the same boundary as the REST API:
  - **Local binary**: the cache uses your kubeconfig identity, so MCP can only see what `kubectl` can see for that user
  - **In-cluster (auth enabled)**: read tools intersect namespaced reads with the calling user's RBAC-allowed namespaces; cluster-scoped reads (Nodes, PVs, ClusterRoles, cluster-scoped CRDs) are gated per-kind via SubjectAccessReview, so cluster-wide pod visibility doesn't implicitly grant Node read; write tools, exec, and logs are fully impersonated so the apiserver enforces the user's RBAC end-to-end
  - **In-cluster (no auth)**: every MCP caller shares the pod ServiceAccount's view — only deploy this way when MCP isn't exposed beyond a trusted boundary
- **Audit subject access** — audit findings and counts, including resource summaries, require namespace visibility plus an explicit Secret list grant for Secret subjects or the exact resource list grant for cluster-scoped subjects, including PersistentVolumes. Namespace-local Secret grants are honored even when cluster-wide Secret listing is denied. The HTTP `raw=true` option bypasses local audit settings, never authorization. Unused ConfigMap/Secret and no-consumer PVC findings additionally require adequate reference evidence. `missingInputs` lists only inputs whose absence left a subject unevaluated, such as `configmap-references`, `secret-references` or `pvc-consumers`; a gap that still lets a finding fire is stated in the finding. See [audit evidence under partial access](configuration.md#audit-evidence-under-partial-access) and [storage review checks](configuration.md#storage-review-checks).
- **Prometheus access has two tiers** — PromQL cannot be namespace-scoped server-side (arbitrary queries can aggregate across namespaces), so with auth enabled the unbounded surfaces — `query_prometheus`, `discover_metrics`, `get_prometheus_rules`, and the REST `/prometheus/query` and `/prometheus/cluster` routes — require a SubjectAccessReview for `list pods` across all namespaces, and the denial says so. The curated REST charts (per-resource, per-namespace, HPA) require only reading the resource they chart. Local no-auth mode is unchanged: every caller shares the kubeconfig identity's view
- **Secret redaction** — Secret `.data` and `.stringData` are never exposed; only key names are shown
- **Value redaction** — environment variable values and Helm values returned through MCP are scrubbed for known secret patterns; Helm values also use key-aware redaction for names like `password`, `token`, `privateKey`, and `secretKey`
- **Log redaction** — pod log output and Helm hook log evidence are scrubbed for secret patterns before being returned

### `resourceContext.manager` — which GitOps manager to look at

`get_resource` and `diagnose` add `resourceContext.manager`: which Argo CD or Flux manager to look at, without picking
a winner the evidence doesn't support ([full rules](ownership.md#what-manages-an-object)).

- `listedBy`: managers whose status lists name the resource. `claims`: managers its labels and annotations name, each
  with a `check` (`recorded`, `omitted`, `missing`, `withheld`, …).
- `state`: `agree` (a list and the metadata name the same manager — the strongest answer), `listed` (a list alone,
  which isn't proof the manager applies it), `disagree` (look at both), `shared` (several lists), `claimed` (metadata
  only), `unknown` (Radar couldn't look everywhere) or `none` (nothing Radar can see — **not** "unmanaged").
- `via`: the controller ancestor whose evidence answers (a Pod's Deployment); `listedBy`/`claims` are then its.
  `alsoClaimed` lists other claims on the way, each `on` its object.
- `lead` is where to look first, not a verdict. `narrowed: true` means you may read only part of the evidence.

Helm annotations beside an Argo CD or Kustomization manager mean an adopted Helm install: its next upgrade can still
overwrite a change. With this answer present, the metadata-only manager warning is left out for the claims it weighs.
With `manager` present, `resourceContext.managedBy` names only managers it doesn't weigh: the topmost controller or
another managing controller (a cert-manager Certificate for its Secret), never a second Argo CD, Flux or Helm answer.
A manager you can't read is left out without a note.

### Node placement context

For a core Kubernetes Node, `get_resource` includes `resourceContext.referencedBy`
with cached, nonterminal Pods assigned through `spec.nodeName`. Controlled Pods
and assigned Pending Pods are included; unassigned Pods, Pods on other nodes and
completed Pods are excluded. References are permission-filtered per namespace,
sorted deterministically and capped at 20. `total` counts only readable Pods;
`truncated` marks the item cap. This is observed placement in Radar's cache scope,
not a cluster-wide workload count or a list of potential scheduling candidates.
When Relationships already collected placement, context reuses that result.
Unavailable Pod data, including a missing placement provider, records
`omitted: [{field: "referencedBy", reason:
"unavailable"}]`, distinguishing a failed lookup from an observed empty result.

ConfigMap and Secret reverse-reference context uses the same omission when a
scanned workload or Pod source cannot be read. References from available sources
remain visible. This projection scans selected built-in workload templates and
controllerless Pods; absence of an omission does not establish complete consumer
coverage or prove that a resource is unused. Storage audit uses its own consumer
inventory and coverage rules.

### Pod/template differences in issue context

Existing OOM/restart, unschedulable, and image-pull issues can carry a neutral
`pod_template_divergence` fact. The UI labels it **Pod/template differences** in
the existing Context section, including resource drawers opened from topology.
It adds no issue, severity, health status, topology edge, or audit finding. MCP
`issues` and `diagnose` expose the same fact; `get_resource` requires
`include=issues` for detailed rows and keeps its default issue summary compact.

The baseline is the current template of the Pod's immediate ReplicaSet or Job,
matched by controller reference, API group, and UID. StatefulSet/DaemonSet
revision reconstruction, Pod-level resource budgets, and RuntimeClass overhead
are outside this comparison. OOM context compares lower
memory limits on status-identified OOM containers; scheduling context compares
higher requests, priority class, node selectors, required affinity, and lost or
changed tolerations; image-pull context compares the failing container's image.
Injected containers, equivalent quantities/image spellings, ordinary
limit-to-request defaulting, added tolerations, and preferred affinity stay quiet.
Environment values, commands, and arbitrary spec diffs are excluded.

One fact shows at most three fields, and its candidates are capped; both disclose
truncation, and candidates are found from every difference, not only the shown
ones. Candidate display order does not imply likelihood. A grouped issue
compares at most ten distinct affected Pods in representative order and labels
its first matching witness as an example. A specific Pod lookup compares only
that Pod, including members beyond the group's inline evidence cap. Existing
diagnostic facts take precedence when the four-fact budget is full.

Candidates are current configuration to inspect, **not attribution**: matching
LimitRange defaults for omitted resource keys, the observed global-default
PriorityClass when the template omitted it, and mutating webhooks whose current
core/v1 Pod CREATE rules and selectors match (UPDATE candidates apply only to
image differences). Pod resize subresource rules are not inspected. Namespace selectors use
namespace labels. CEL match conditions are not evaluated; such webhooks are
listed as not evaluated. Fully described webhook candidates show their
configuration's creation time; those past the line cap are named only. CREATE-only configurations newer than the Pod are excluded. Validating
webhooks and Pod Security Admission are not mutation candidates. All named
evidence is permission-filtered before producing prose; unreadable or unsynced
candidate configuration does not suppress an observable Pod/template difference,
and the fact says which candidate sources it could not inspect.
The existing OOM diagnosis also records its template source internally so an
unreadable ReplicaSet limit cannot reappear through the issue cause text.

Comparisons use cached current objects, not an admission-time snapshot. Resizing,
other writers, and later template/configuration edits can explain differences.
Candidate inspection is non-exhaustive and does not perform live admission calls
or start watches. Verify provenance with configuration history or
[Kubernetes admission audit records](https://kubernetes.io/docs/reference/access-authn-authz/extensible-admission-controllers/#monitoring-admission-webhooks).

### Pod/template differences without an issue

`get_resource` (default context) adds `resourceContext.templateDivergence` for
a Pod, ReplicaSet, Deployment or Job whose live Pods differ from the current
template of their own ReplicaSet or Job, so a rolling update is not a
difference; `diagnose` adds it for a Pod or Deployment. It is context, not
proof: a `summary`, the `differences` (for a workload, `podsCompared`,
`podsDiffering` and an `examplePod`), and `sources`, the same candidate list as
the issue fact. StatefulSet and DaemonSet Pods are not compared.

- Reported: resource requests and limits, images, node selector, required
  affinity, removed or changed tolerations, a template-set priority class, and
  the runtime class.
- Quiet: what admission adds routinely (injected containers, volumes and env,
  added tolerations, extended resources, limit-to-request copies, a defaulted
  priority class, a digest pinned onto the template's tag, Istio's `auto`
  image), and resource values while a resize is in progress.

With `context=none`, `get_resource` returns a one-line divergence note in
top-level `warnings`, outside the returned object.

### Cluster-scoped admission changes in a namespace view

Secret data updates enter the default `get_changes` and issues `recent_changes` feed when referenced by a workload template or current Pod spec at query time; `kind=Secret` lists all updates. `consumed_by` means referenced by, and `running_previous_value` names Pods likely retaining earlier env values after an observed write; its absence means not established, rather than no stale Pods. Both lists name only workloads and Pods the caller may read: `consumed_by_withheld` and `running_previous_value.withheld` count the rest as `{count, reason: "user_forbidden"}`, so a filtered list is not read as complete. `running_previous_value` appears only for callers who may read Pods in the namespace, and its `pods` count covers only those Pods.

Repeated Secret updates collapse into the newest row with merged changed keys, `updates_in_window` and `first_update_at`; rows carrying stale evidence for different keys stay separate. `kind=Secret` and `name=` queries retain every update.

A namespace `get_changes` (no `kind` or `name`) adds `clusterScopedChanges`; a
single-namespace `issues` answer adds `cluster_scoped_changes` when it has rows
or a coverage gap (notes that only say Radar started inside the window are left
out there). It lists MutatingWebhookConfiguration and
ValidatingWebhookConfiguration changes in the window, capped with truncation
disclosed, each with `applicability` and `applies_because`:

- `applies`: a changed webhook's current selectors and rules reach the namespace.
- `may_apply`: Radar cannot establish that, for example because of unevaluated
  object selectors or CEL, rules of unknown scope, unreadable labels, or a
  deleted or re-scoped webhook.

An empty list is a complete negative only without `error`, `incomplete` or
`truncated`.
Webhooks unchanged in the window are not listed; the Pod/template candidates
above cover them. List order does not indicate likelihood.

### Shared deletion preview

`GET /api/resources/{kind}/{namespace}/{name}/cascade-preview` reads the root
and ownership data as the caller. `basis: ownerReferences` identifies the
ownership projection: `dependents` are observed objects expected to be garbage
collected; `possibleDependents` have unresolved owners or ownership chains.
A live owner outside the deletion closure preserves its dependents.

`coverage` is limited to readable, collected kinds: `narrowed` means restricted
visibility, `notChecked` means cache or discovery gaps, and `truncated` means the
1,000-object cap or deadline was reached. If the root cannot be verified, the
preview returns `rootResolved: false` with an `unavailableReason`.

The dependent lists describe cascading collection. Pending foreground/orphan
propagation is preserved; orphan deletion keeps dependents, so the dialog hides
those lists until force is selected. Force strips finalizers and requests
Background propagation. It can skip remaining cleanup, but cannot undo cleanup
already started.

`controllerTeardown` separately describes recognized Argo CD pruning or Flux
pruning/Helm uninstall on the root. Recorded local members are caller-filtered
and include membership coverage; remote or unknown destinations list no local
inventory. Controller opt-outs and Helm manifests may change the actual cleanup.
Namespace contents, CRD instances and other controller effects are not enumerated;
storage/data removal also depends on reclaim policy and controllers.

The REST preview is advisory: it does not bind a later DELETE to the object
shown in the dialog. Finalizers, admission and reconciliation can change or delay
the result, so the preview does not guarantee completion.

### MCP deletion preview and confirmation

External writable MCP exposes `delete_resource`. It is absent from read-only
mounts and from the built-in `/mcp-apply` transport used by Radar OSS's built-in CLI agents. Hub's sidecar independently enforces its read-only gate; it does not use this new mount.
Built-in deletion stays deferred until the approval flow presents this preview.
There is no new configuration switch.

The default call performs a caller-authenticated server dry-run and returns a
summary, finalizers, propagation, the shared observed-impact preview, and a
five-minute signed confirmation. `dry_run: false` requires that confirmation.
It binds the caller and groups, captured cluster context, root UID,
generation/finalizers (resourceVersion for kinds without generation), and propagation policy.
Root spec/finalizer/incarnation or policy changes require another preview. For roots
with generation, status-only changes before confirmation can proceed with fresh
resourceVersion preconditions. Kinds without generation require another preview
after any update. An API-server conflict returns an error requiring another preview;
the tool does not retry DELETE automatically.

Dependent lists are advisory observations. Confirmation does not compare a child
fingerprint or freeze the cascade: dependents and controller inventory can change
independently of the root, including after preview or confirmation.

GC dependents, possible dependents and controller teardown stay separate.
Counts cover the observed traversal before each 100-reference MCP list cap;
`ownershipCoverage` carries source gaps, caller narrowing and traversal limits.
Namespace contents and CRD instances are never interpreted as an empty list.
A completely unavailable or timed-out impact calculation returns no usable confirmation token. Inspect `cascade.unavailableReason` and `recoveryAction`: connection/cache loading can recover, but access or collection limits require a change. Use the UI or kubectl if Radar cannot provide caller-readable impact. Partial observed projections retain explicit coverage limits. One 40s
budget covers the call, with at most 5s each for enumeration and post-delete
observation. A stuck kubeconfig exec credential refresh can outlive the request
budget, as with other client-go operations.

Accepted deletion is progress even while finalizers hold it. Failed post-delete
observation cannot turn acceptance into failure. An interrupted DELETE with an
uncertain result says to inspect the resource before retrying. The tool never
patches finalizers. Explicit finalizer intervention remains a separate
`patch_resource` action after inspecting cleanup. A webhook whose side-effects declaration prohibits server dry-run prevents this tool from issuing confirmation; the API error identifies the rejecting webhook. The token checks reviewed state, not proof of a human decision: the external MCP host owns per-call approval. Audit records identify caller,
kind/group/target and preview/accepted/failed/unknown outcomes; confirmation
values are omitted from ordinary tool-argument logs.


Confirmation tokens are process-local. Use a single Radar replica or session affinity so preview and confirmation reach the same process; a restart invalidates outstanding tokens. `/mcp-apply` is an authenticated subset of writable `/mcp`, intended for built-in agents, not a private network endpoint.
