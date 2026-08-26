# Configuration

This document covers Radar's cluster connection behavior. For commands and flags, see the [CLI reference](https://radarhq.io/docs/configuration/cli).

## HTTP Listener

Radar listens on `127.0.0.1:9280` by default, so an unauthenticated local
instance is reachable only from the same network namespace. `localhost` is
accepted as an equivalent spelling. Requests to this loopback-only,
unauthenticated listener must also use a loopback `Host`; Radar rejects other
hostnames so DNS rebinding cannot turn an untrusted site into a local client.
The reserved `*.localhost` family is accepted; arbitrary local DNS and
`/etc/hosts` aliases are not.
To put a non-loopback hostname or reverse proxy in front of Radar, enable Radar
authentication; do not switch to `0.0.0.0` merely to bypass this check.

To reach Radar through a VM, WSL, dev container, jump host, or another machine,
opt into a shared listener explicitly:

```bash
radar --listen-address=0.0.0.0
```

An all-interface listener can be reached by non-browser clients; CORS is not an
authentication boundary. Enable Radar authentication and restrict network
access whenever using `0.0.0.0`. The loopback `Host` protection above does not
apply to a shared listener, and Origin checks alone do not stop DNS rebinding
there. Treat an unauthenticated shared listener as accessible to any browser
that can reach the network and do not expose it outside a fully trusted network.
The host local terminal is unavailable on a shared listener even if a client
sends a loopback `Host` value.

The Docker image and Helm chart set `0.0.0.0` explicitly because their HTTP
listener must be reachable through a published container port or Kubernetes
Service. Desktop Radar and temporary `radar diagnose` servers remain
loopback-only.

## Desktop Window Behavior

On macOS, closing the Radar window hides the app rather than quitting it. The
Dock icon stays; a Dock click, Cmd+Tab, or Radar → Show All brings the window
back with the session intact. File → Close Window (Cmd+W) hides it the same way
the close button does. To quit, use Radar → Quit Radar (Cmd+Q).

Radar keeps running while hidden. That is the point — MCP clients stay
connected across a window close — but it means a hidden Radar still:

- serves its loopback HTTP and MCP endpoints, under your kubeconfig identity;
- holds watches open against the API server for every cached resource kind;
- holds the memory backing those caches.

None of that stops until you quit. If you close the window expecting Radar to
release its cluster access, quit explicitly.

On Linux and Windows, closing the window always quits. Neither gives Radar
anything to reopen from — no Dock, and Wails v2 ships no tray icon — so hiding
there would leave a running process with no way to reach it.

## Persistent Configuration

Radar stores configuration in two files under `~/.radar/`:

### Config File (`~/.radar/config.json`)

Persistent defaults for CLI flags. CLI flags always override these values. Managed via the Settings dialog in the UI or `PUT /api/config`.

```json
{
  "kubeconfig": "",
  "kubeconfigDirs": [],
  "namespace": "",
  "namespaces": [],
  "port": 9280,
  "noBrowser": false,
  "browser": "",
  "timelineStorage": "memory",
  "timelineDbPath": "~/.radar/timeline.db",
  "timelineMaxSize": "0",
  "historyLimit": 10000,
  "prometheusUrl": "",
  "opencostCurrency": "",
  "costSource": "auto",
  "kubecostUrl": "",
  "kubecostClusterId": "",
  "kubecostApiKey": "",
  "prometheusHeaders": {},
  "mcp": true,
  "debugImage": ""
}
```

All fields are optional — omitted fields use built-in defaults.

| Field | Description |
|-------|-------------|
| `kubeconfig` | Primary kubeconfig file (same as `--kubeconfig`) |
| `kubeconfigDirs` | Directories containing additional kubeconfig files (same as `--kubeconfig-dir`) |
| `restoreLastDesktopContext` | Desktop app only: reopen on the cluster last used (default: enabled). `false` always opens on the kubeconfig's `current-context` — see [Startup Context](#startup-context) |
| `namespace` | Initial namespace filter |
| `namespaces` | Initial namespace filters as a list (same as `--namespaces ns1,ns2,ns3`) |
| `port` | Server port (default 9280) |
| `noBrowser` | Don't auto-open browser |
| `browser` | Browser for automatic launch (same as `--browser`; on macOS, app names like `Google Chrome` are supported) |
| `timelineStorage` | `memory` or `sqlite` |
| `timelineDbPath` | Path to SQLite database |
| `timelineMaxSize` | Max SQLite DB + WAL size before pruning oldest events (`0` disables) |
| `historyLimit` | Max timeline events to retain |
| `prometheusUrl` | Manual Prometheus/VictoriaMetrics URL — skips auto-discovery. Useful when Prometheus is not in the same cluster or uses a non-standard service name. |
| `opencostCurrency` | Optional ISO 4217 override for values produced by OpenCost or Kubecost. Empty reads `currencyCode` from the pricing ConfigMap referenced by an active OpenCost/Kubecost workload, or literal `DISPLAY_CURRENCY` from an active Kubecost Deployment or StatefulSet, when the selected cost source is tied to the connected cluster; otherwise it falls back to `USD`. Radar labels values but does not convert them. Equivalent CLI: `--opencost-currency`; an explicit CLI value remains authoritative while Radar runs and after restart. |
| `costSource` | `auto` (default), `prometheus`, or `kubecost`. Auto keeps working OpenCost-compatible Prometheus metrics, then tries a Kubecost 3 Aggregator. |
| `kubecostUrl` | Optional Kubecost 3 Aggregator base URL. Empty discovers an active local Aggregator Service on its named `tcp-api` port (9004). Federated agent-only clusters need the central URL; root API URLs and URLs ending in `/model` are accepted. |
| `kubecostClusterId` | Cluster ID used to filter a central Aggregator. Empty detects one distinct literal `CLUSTER_ID` from an active FinOps Agent or Aggregator; indirect or conflicting values require an override. An override saved in Settings is bound to the active kubeconfig context so switching clusters cannot silently reuse the wrong cluster's costs. A value added directly to the config file is bound and persisted on its first startup with an available kubeconfig context. |
| `kubecostApiKey` | Optional Kubecost service-account key sent as `X-API-KEY`. Stored in the `0600` config file and redacted from `GET /api/config`; changing the URL origin clears a stored key unless it is supplied again. With a blank URL, a key saved in Settings is bound to the active kubeconfig context because Radar will auto-discover that cluster's local Aggregator; a key added directly to the config file is bound and persisted on its first startup with an available kubeconfig context. A key paired with an explicit central URL can be reused across contexts. Prefer a Secret-backed Helm value for in-cluster deployments. |
| `prometheusHeaders` | HTTP headers sent with every Prometheus request. Required for auth-protected backends — e.g. `{"X-Scope-OrgID": "my-org"}`. Equivalent CLI: `--prometheus-header Key=Value` (repeatable). Stored in plain text in `config.json` — protect the file accordingly. |
| `argoCdUrl` | Manual argocd-server URL for the Argo CD API integration — skips auto-discovery. |
| `argoCdToken` | Argo CD API token (get-only account recommended). Stored in plain text — the file is written `0600`; the token is redacted from `GET /api/config`. |
| `argoCdInsecureTls` | Skip TLS verification for argocd-server (self-signed default installs). Scoped to the Argo CD client only. |
| `prometheusHeadersFromEnv` | Header values read from environment variables at startup — e.g. `{"Authorization": "PROMETHEUS_TOKEN"}`. Equivalent CLI: `--prometheus-header-from-env Key=ENV_VAR` (repeatable). Use this with Kubernetes Secret-backed env vars in Helm deployments. |
| `mcp` | Enable/disable MCP server for AI tools (default: enabled) |
| `debugImage` | Image for ephemeral debug containers and node debug pods (same as `--debug-image`). Empty = `busybox:latest`; point at a mirror for air-gapped / private-registry clusters. |

For declarative deployments, `RADAR_COST_SOURCE`, `RADAR_KUBECOST_URL`,
`RADAR_KUBECOST_CLUSTER_ID`, and `RADAR_KUBECOST_API_KEY` override these cost
source fields. When any is set, the source controls are read-only in Settings;
edit the deployment and restart Radar. `RADAR_KUBECOST_URL` does not carry an
API key over from the config file; set `RADAR_KUBECOST_API_KEY` explicitly when
the environment-managed endpoint requires one. The currency override remains separate.

### Settings File (`~/.radar/settings.json`)

User preferences for the UI. Managed via the Settings dialog or `PUT /api/settings`.

```json
{
  "theme": "system",
  "pinnedKinds": [
    { "name": "Deployments", "kind": "Deployment", "group": "" }
  ]
}
```

| Field | Values | Description |
|-------|--------|-------------|
| `theme` | `light`, `dark`, `system` | UI theme preference |
| `pinnedKinds` | Array of `{name, kind, group}` | Resource kinds pinned to the sidebar |
| `lastDesktopContext` | `{name, sourceFile, inFileName}` | Written by the Desktop app for itself: the cluster its window last used, reopened on the next launch. Stripped from `/api/settings`, and never read by `kubectl radar` or the `radar` CLI — see [Startup Context](#startup-context) |

## Cluster Connection Precedence

Radar resolves configured sources first, then falls back to the same environment,
in-cluster, and default-file sources as `kubectl`:

| Priority | Source | Description |
|----------|--------|-------------|
| 1 | Configured kubeconfig file and directories | The primary file loads first, followed by valid files found in configured directories |
| 2 | `KUBECONFIG` env var | Used only when neither a configured primary file nor directories are present |
| 3 | In-cluster config | Tried when no configured source or `KUBECONFIG` exists |
| 4 | `~/.kube/config` | Used when the in-cluster attempt is unavailable |

The Settings values and their matching flags form one source pair. With no
explicit flags, Radar uses both saved values. Passing only `--kubeconfig`
replaces saved directories; passing only `--kubeconfig-dir` replaces the saved
primary file. Passing both flags explicitly combines both sources.

For compatibility with existing directory-mode installations, directories
configured without a primary file suppress ambient `KUBECONFIG`. Radar reports
that suppression in startup logs and diagnostics.

## KUBECONFIG vs In-Cluster Detection

When Radar runs inside a Kubernetes pod, Kubernetes automatically sets the `KUBERNETES_SERVICE_HOST` environment variable. This normally triggers in-cluster configuration using the pod's service account credentials.

However, **explicit kubeconfig takes precedence**. If you set `KUBECONFIG` or pass `--kubeconfig`, Radar uses that instead of in-cluster config. Configured directories also prevent in-cluster detection. This allows you to:

- Run Radar inside a pod but connect to a different cluster
- Use specific credentials instead of the pod's service account
- Test with a custom kubeconfig while developing inside a cluster

**Example: Override in-cluster config**
```bash
# Inside a pod, connect to a different cluster
export KUBECONFIG=/path/to/other-cluster.yaml
kubectl radar
```

This behavior matches `kubectl` and follows the [Kubernetes client-go precedence rules](https://github.com/kubernetes/kubernetes/issues/43662).

## Multiple Kubeconfig Files

`KUBECONFIG` can contain multiple file paths (colon-separated on Linux/macOS,
semicolon-separated on Windows):

```bash
export KUBECONFIG=~/.kube/config:~/.kube/staging-config:~/.kube/prod-config
kubectl radar
```

Alternatively, use `--kubeconfig-dir` to load valid kubeconfig files from one or
more directories. Discovery is non-recursive:

```bash
kubectl radar --kubeconfig-dir ~/.kube/configs/
```

The primary file and directories can be combined:

```bash
kubectl radar --kubeconfig ~/.kube/config --kubeconfig-dir ~/.kube/configs/
```

Radar keeps every file isolated rather than merging their cluster and user maps.
This prevents identical user or cluster names in different files from selecting
the wrong credentials. Context names remain unchanged unless two files use the
same name; later collisions receive a source suffix in the context switcher.
Saved namespace selections and integration credentials are keyed by that visible
context name. If adding an earlier source causes a collision suffix to appear,
the renamed context does not inherit preferences stored under its former name;
Radar reports the rename in startup logs and diagnostics so it can be reconfigured.

Files are ordered with primary paths first, followed by directory order and then
filename order. A primary file's `current-context` wins when it declares one;
otherwise Radar uses the first source in order that declares a current context.
Leading `~/` paths are expanded, and references to the same underlying file are
loaded once even when they use different absolute, relative, or symlink paths.
Directory membership is scanned at startup. Changes inside a file that was
already discovered, including added or removed contexts, are reflected while
Radar is running; deleting that file also removes its contexts. A brand-new file
added to a configured directory is discovered after Radar restarts. Directory
entries must resolve to regular files: symlinks to regular kubeconfigs are
accepted, while directories, sockets, pipes, and device files are ignored.

An unusable additional directory does not prevent a valid primary file from
loading. A configured primary source group that contains no usable contexts fails
initialization rather than silently connecting to a directory cluster. Desktop
Radar keeps its window open so the source can be repaired in Settings.

## Context Switching

Radar supports switching between Kubernetes contexts at runtime through the UI. Click the context selector in the header to switch between available contexts.

When running in-cluster (using the pod's service account), context switching is disabled.

Switching contexts in the UI never rewrites your kubeconfig — `kubectl` keeps pointing wherever it pointed before.

### Expired credentials

If an active context's credentials expire or are rejected, Radar disconnects cluster-backed work and retries automatically. After you re-authenticate, exec-based credentials are re-probed and static credentials are reloaded from kubeconfig on disk, so Radar can reconnect without a restart. Retries start after 30 seconds and back off to 5 minutes; a credential plugin that stops responding is retried less frequently.

## Startup Context

Which cluster Radar comes up on depends on how you launched it.

**The Desktop app reopens where you left off.** The context selected at startup and every successful context switch are recorded as `lastDesktopContext` in `~/.radar/settings.json`, and the next launch reconnects to it — the natural behaviour for a window you closed and reopened.

**`kubectl radar`, `radar`, and `radar diagnose --standalone` start on the kubeconfig's `current-context`**, as `kubectl` would. A command typed right after `kubectl config use-context staging` runs against staging, and a cluster picked in the Desktop app days ago never redirects it. Terminal runs don't record switches either, so nothing you do in one moves where the Desktop app reopens.

The separation is not a preference: the remembered cluster is written under a Desktop-scoped key that the CLI never reads, and there is no setting that opts the CLI in.

To stop the Desktop app reopening on the last cluster, turn off **Reopen on the last used cluster** in Settings → Connection, or set in `~/.radar/config.json`:

```json
{
  "restoreLastDesktopContext": false
}
```

Details worth knowing:

- The remembered context records the kubeconfig file it came from, not just its name — the name alone is not a stable handle. With several kubeconfigs loaded, two files can define the same context name, and which one keeps the unqualified name depends on the order the files are read, so adding a file can hand that name to a different cluster.
- Radar reopens only on an exact match: the same context, in the same file. Anything else — the context renamed or deleted, the file moved or no longer loaded — opens the kubeconfig's `current-context` instead, and says so in Diagnostics. A same-named context in another file is not treated as evidence that it is the same cluster; losing the convenience costs a click, landing on the wrong cluster costs more.
- If the remembered cluster is unreachable (VPN down, for instance), Radar reports the connection failure rather than silently connecting to a different cluster. Pick another cluster from the header.
- Clusters connected through CAPI are never remembered: their kubeconfig is a temporary file that no longer exists on the next run.
- Turning the memory off takes effect on the next Desktop start: it clears the remembered cluster as well as stopping new recording, so turning it back on later starts fresh rather than reopening a cluster you stopped using months ago.

## Namespace Picker

The header has a namespace picker on the right. Pick a single namespace to focus the view, or **All namespaces** to see everything you have access to. Cluster-scoped resources (Nodes, Namespaces, PVs, StorageClasses) appear regardless of the pick if your RBAC permits them — they have no namespace to filter on. Namespace-restricted users without their own cluster-scoped RBAC won't see cluster-scoped sections at all.

The pick is a per-user view filter — it doesn't change anything for other users sharing the same Radar instance. Locally, your pick is remembered per kubeconfig context across restarts. In shared (auth-enabled) deployments the pick lives for the session.

Until you make a pick, local sessions default to the namespace set on the kubeconfig context (kubectl parity — the same namespace `kubectl` would use, including one set via `kubectl config set-context` or `kubens`). An explicit `--namespace` / `--namespaces` flag outranks the kubeconfig value, and contexts without either default to **All namespaces**. Once you pick namespaces or explicitly choose **All namespaces**, that choice sticks for the context and the kubeconfig value is no longer consulted.

If your account can list resources inside several namespaces but cannot list namespaces cluster-wide, start Radar with an explicit list:

```bash
kubectl radar --namespaces ns1,ns2,ns3
```

Radar probes each listed namespace for access and watches every namespace where access is granted — resource views then cover all of them, not just the first. The list is also each user's initial picker selection: locally via the launch URL, and in shared (auth-enabled) deployments as a per-session default seeded on first read. Clearing the picker back to **All namespaces** sticks for the rest of the session. The picker can switch between those namespaces or keep several selected at once.

This covers built-in resource types and custom resources alike: CRDs (GitOps, Gateway API, etc.) are probed per-kind across the same list and watched in every granted namespace. The list is capped by `--max-scope-candidates` (default 20) — startup fails with a clear error rather than silently probing a subset.

When Radar starts with `--namespace-scope`, the picker controls the process-wide cache scope instead of just a view filter. Namespaced informer caches are pinned to one namespace while cluster-scoped resources remain cluster-wide. Local/no-auth sessions can switch the scoped namespace, which rebuilds the cache in place. Auth-enabled and Radar Cloud sessions lock the picker to the startup namespace so one user cannot reshape the shared backend cache for everyone.

**Single namespace only.** `--namespace-scope` pins the cache to exactly one namespace; scoping to several namespaces at once is not supported yet. Passing more than one (e.g. `--namespace=a,b`) fails at startup with a clear error rather than silently caching nothing. When scoped, the namespace picker becomes single-select, and a switch re-points the whole cache to the new namespace rather than adding to it.

## Radar Cloud

Radar is free and fully functional without an account. A Cloud button in the
header offers to connect the cluster to [Radar Cloud](https://app.radarhq.io) —
optional, and nothing else depends on it.

| Variable | Effect |
|---|---|
| `RADAR_CLOUD_FUNNEL=off` | Removes the Cloud button entirely. `on` forces it on. |
| `RADAR_HUB_URL` | Point Cloud connection at a self-hosted Radar Hub instead of the hosted service. |
| `RADAR_HUB_APP_URL` | Self-hosted Hub's web origin, when it differs from `RADAR_HUB_URL`. |

To connect a cluster from the command line, use `radar cloud install`
(`--hub-url` for a self-hosted Hub).
`radar cloud install` and `radar cloud status` target one cluster, so they use
the configured primary kubeconfig and report configured directories they
ignore. With no configured source, they use the normal `KUBECONFIG` / default
kubeconfig loading rules. Directory-only configuration must add a primary
kubeconfig before these commands can run.

### What Radar sends

Until you connect a cluster to Cloud, Radar makes two kinds of outbound
request, both to Skyhook, neither containing cluster data:

- **Update check** — to `releases.skyhook.io`, with the Radar version, OS/arch,
  install method, whether it is running locally or in-cluster, and the
  installation timestamp when Radar can determine it. Radar caches the release
  result for one hour. Development builds are excluded.
- **Cloud dialog copy** — only when you *open* the Cloud dialog, to fetch the
  current terms shown in it. No identifiers are sent. `RADAR_CLOUD_FUNNEL=off`
  stops this request from ever happening.

A standalone Radar sends your cluster's data nowhere: it talks to your
Kubernetes API directly and keeps everything it reads on your machine.

Connecting a cluster to Cloud is what changes that, and it is the point of
connecting — the cluster's agent opens an outbound tunnel to the Hub so the
team can reach the same views without each person holding kubeconfig access.
Deciding whether to connect is a separate question from the two requests
above, which happen either way.

## Related Documentation

- [CLI reference](https://radarhq.io/docs/configuration/cli) — Commands and operator-facing flags
- [In-Cluster Deployment](in-cluster.md) — Deploy Radar inside your cluster with Helm
- [Authentication & Authorization](authentication.md) — Proxy and OIDC auth for shared deployments
