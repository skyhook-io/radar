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

To bind only a specific local IP, use e.g. `--listen-address=192.168.1.5` or
`--listen-address=::1`. IPv6 addresses are passed without brackets; URLs use
brackets, e.g. `http://[::1]:9280`. The address must be assigned on the machine.
Hostnames other than `localhost`, interface names, CIDRs, and IPv6 zone IDs are
not accepted. The existing `0.0.0.0` wildcard retains dual-stack behavior where
supported by the OS; `::` selects an IPv6 wildcard.

Browser launch and built-in AI investigations use the selected address.
`radar diagnose` discovers the address through `~/.radar/mcp-port`. Use
`--server http://<address>:<port>` to select an instance explicitly (with
brackets around IPv6 addresses).
External MCP clients should use that address in their configured URL too.
This flag affects Radar's HTTP server; port-forward address options remain
`127.0.0.1`/`localhost` and `0.0.0.0`.

A non-loopback listener can be reached by non-browser clients; CORS is not an
authentication boundary. Enable Radar authentication and restrict network
access whenever binding a non-loopback address. The loopback `Host` protection above does not
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

Local CLI and Desktop Radar store machine defaults (`config.json`), personal
preferences (`settings.json`), and per-context integration connections
(`clusters.json`) under `~/.radar/`.
Shared OSS installations (in-cluster or authentication-enabled) expose these
installation settings read-only; configure them through Helm values or startup
configuration instead. See [installation settings](in-cluster.md#installation-settings)
for provisioning, upgrades, personal preferences, and the separate Cloud behavior.
The local files below are not a substitute for Helm configuration.

Non-Helm shared OSS still reads `config.json` as startup defaults (including
previously saved integration endpoints); flags override them. It does not adopt
UI-saved audit policy or OCI sources from `settings.json`: move those into
`RADAR_OPERATOR_SETTINGS_FILE` before upgrading. Radar logs a warning when it
ignores those saved settings. Neither local file is rewritten by this transition.

### Config File (`~/.radar/config.json`)

Persistent defaults for CLI flags. CLI flags override these values. Managed via the Settings dialog in the UI or `PUT /api/config`.

For local CLI/Desktop, `prometheus*`, `argoCd*`, `costSource`, and `kubecost*`
fields below are **legacy import sources**, not active integration defaults.
Configure these in [local integration connections](#local-integration-connections)
instead. `opencostCurrency` remains a machine preference. Shared OSS and Cloud
retain installation-scoped configuration; the integration-field descriptions
below describe that installation-scoped behavior.

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
| `timelineStorage` | `memory`, `sqlite`, or `postgres` |
| `timelineDbPath` | Path to SQLite database (sqlite only) |
| `timelineMaxSize` | Max SQLite DB + WAL size before pruning oldest events (`0` disables; sqlite only) |
| `historyLimit` | Max timeline events to retain (memory only) |
| `prometheusUrl` | Manual PromQL-compatible query URL — works with Prometheus, VictoriaMetrics, Thanos, Mimir, and similar backends. Skips auto-discovery; useful when the backend is not in the same cluster or uses a non-standard service name. Leave it empty and Radar looks for a Prometheus-like Service on startup and after each context switch. While it looks, the Metrics status says "discovering". If it finds a backend it cannot reach, and it can see a NetworkPolicy that blocks the connection, the status names that policy. |
| `opencostCurrency` | Optional ISO 4217 override for values produced by OpenCost or Kubecost. Empty reads `currencyCode` from the pricing ConfigMap referenced by an active OpenCost/Kubecost workload, or literal `DISPLAY_CURRENCY` from an active Kubecost Deployment or StatefulSet, when the selected cost source is tied to the connected cluster; otherwise it falls back to `USD`. In Settings this preference saves through the dialog footer, independently of source testing, so it can be changed while a source is unavailable. Radar labels values but does not convert them. Equivalent CLI: `--opencost-currency`; an explicit CLI value remains authoritative while Radar runs and after restart. |
| `costSource` | `auto` (default), `prometheus`, or `kubecost`. Auto keeps working OpenCost metrics from a PromQL-compatible backend, then tries a Kubecost 3 Aggregator; if neither is present, selection remains unavailable and retries instead of reporting an absent source as active. Settings validates Auto and Kubecost before saving. An explicit `prometheus` value is a persisted preference and can be saved before its metrics are installed. |
| `kubecostUrl` | Optional Kubecost 3 Aggregator base URL. Empty discovers an active local Aggregator Service and tries its named `tcp-api` port (9004). When that port requires SAML/OIDC and no API key is configured, Radar can fall back to the same Service's exact `tcp-api-rbac` port (9008). Federated agent-only clusters need the central URL; root API URLs and URLs ending in `/model` are accepted. |
| `kubecostClusterId` | Cluster ID used to filter a central Aggregator. Empty detects one distinct literal `CLUSTER_ID` from an active FinOps Agent or Aggregator; indirect or conflicting values require an override. An override saved in Settings is bound to the active kubeconfig context so switching clusters cannot silently reuse the wrong cluster's costs. A value added directly to the config file is bound and persisted on its first startup with an available kubeconfig context. |
| `kubecostApiKey` | Optional Kubecost service-account key sent as `X-API-KEY`. Stored in the unencrypted `0600` config file and redacted from `GET /api/config`; changing the URL origin clears a stored key unless it is supplied again. With a blank URL, a key saved in Settings is bound to the active kubeconfig context because Radar will auto-discover that cluster's local Aggregator; a key added directly to the config file is bound and persisted on its first startup with an available kubeconfig context. An explicit key is never bypassed through an auto-discovered unauthenticated port: authentication failure remains visible. A key paired with an explicit central Aggregator URL can be reused across contexts. In the Helm deployment, Settings is read-only; provision a Kubernetes Secret with `cost.kubecost.existingSecret`. |
| `prometheusHeaders` | HTTP headers sent with every Prometheus request. Required for auth-protected backends — e.g. `{"X-Scope-OrgID": "my-org"}`. Equivalent CLI: `--prometheus-header Key=Value` (repeatable). Stored in plain text in `config.json` — protect the file accordingly. **Requires `prometheusUrl`**: headers carry credentials, and auto-discovery probes every Service that looks like Prometheus, so with headers configured and no URL Radar refuses to discover (the Metrics status names the rule) rather than send them to endpoints you never named. Settings rejects saving that combination. |
| `argoCdUrl` | Manual argocd-server URL for the Argo CD API integration — skips auto-discovery. |
| `argoCdToken` | Argo CD API token (get-only account recommended). Stored in plain text — the file is written `0600`; the token is redacted from `GET /api/config`. |
| `argoCdInsecureTls` | Skip TLS verification for argocd-server (self-signed default installs). Scoped to the Argo CD client only. |
| `prometheusHeadersFromEnv` | Header values read from environment variables at startup — e.g. `{"Authorization": "PROMETHEUS_TOKEN"}`. Equivalent CLI: `--prometheus-header-from-env Key=ENV_VAR` (repeatable). Use this with Kubernetes Secret-backed env vars in Helm deployments. Same rule as `prometheusHeaders`: requires `prometheusUrl`. |
| `mcp` | Enable/disable MCP server for AI tools (default: enabled) |
| `debugImage` | Image for ephemeral debug containers and node debug pods (same as `--debug-image`). Empty = `busybox:latest`; point at a mirror for air-gapped / private-registry clusters. |

The PostgreSQL DSN is **runtime-only** — set it through the `RADAR_TIMELINE_POSTGRES_DSN` environment variable. It is intentionally never written to `config.json` so credentials do not persist to disk in the settings file.

For declarative deployments, `RADAR_COST_SOURCE`, `RADAR_KUBECOST_URL`,
`RADAR_KUBECOST_CLUSTER_ID`, and `RADAR_KUBECOST_API_KEY` override these cost
source fields. When any is set, the source controls are read-only in Settings;
edit the deployment and restart Radar. `RADAR_KUBECOST_URL` does not carry an
API key over from the config file; set `RADAR_KUBECOST_API_KEY` explicitly when
the environment-managed endpoint requires one. The currency override remains separate.

For workload charts, see [Workload metrics](workload-metrics.md)
for the per-chart data requirements, automatic endpoint discovery and attribution,
optional scope overrides, and coverage limits. Finding a Prometheus endpoint does
not imply that it contains application HTTP metrics.

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
Saved namespace selections are keyed by that visible context name. Integration
credentials have separate binding rules; they are not general per-context profiles
(see [integration settings](#integration-settings-when-switching-clusters)).
If adding an earlier source causes a collision suffix to appear,
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

In shared OSS installations (in-cluster or authentication-enabled), context switching is disabled. The operator selects the connection at startup. This includes Helm installations and Pods using an explicit kubeconfig on a non-loopback listener; a personal loopback CLI in a development Pod remains local.

Switching contexts in the UI never rewrites your kubeconfig — `kubectl` keeps pointing wherever it pointed before.

### Expired credentials

If an active context's credentials expire or are rejected, Radar disconnects cluster-backed work and retries automatically. After you re-authenticate, exec-based credentials are re-probed and static credentials are reloaded from kubeconfig on disk, so Radar can reconnect without a restart. Retries start after 30 seconds and back off to 5 minutes; a credential plugin that stops responding is retried less frequently.

### Integration settings when switching clusters

| Setting | Local CLI / Desktop scope |
|---|---|
| Metrics URL, authentication and tenant headers | Saved connection explicitly assigned to this kubeconfig context |
| Argo CD URL, token and TLS verification | Saved connection explicitly assigned to this context; discovery credentials stay context-specific |
| Cost source (Auto / Prometheus / Kubecost) | Context-specific |
| Kubecost URL and API key | Saved connection explicitly assigned to this context; discovery credentials stay context-specific |
| Kubecost cluster ID | Context-specific, **never** inherited from another connection's assignments |
| Prometheus-based costs | Use this context's metrics connection |
| Workload-metrics scope evidence / assertion | Rechecked; process-local assertions are not stored in a connection |
| Namespace selection | Remembered per visible kubeconfig context in `settings.json` |
| Currency, UI preferences, OCI sources | Existing machine/personal scope; not connection assignments |

### Local integration connections

In **Settings → Metrics / Argo CD / Cost**, configure the selected context and
apply. No connection name or assignment wizard is required. Switching A → B → A
restores A's settings. A context with no saved settings uses discovery. Local CLI
and Desktop share `~/.radar/clusters.json`.

**Reuse:** on another context, choose **Use saved connection…**. This creates a
shared reference, not a copy. Radar labels it using the backend type and host;
**Rename** is optional under **Manage saved connections**. Equal URLs are not
automatically merged: different tenants and credentials can use the same server.
New contexts never inherit a default connection.

**Edit:** a shared connection offers **Edit shared connection** (shows every
affected context) or **Customize for this cluster** (creates an independent
connection, preserving unchanged credentials without returning them to the
browser). Kubecost cluster mappings remain separate from shared backend edits.

**Credentials:** Settings displays header names and whether a token/key exists,
never saved values. Keep, replace or remove each credential independently.
Changing URL origin (scheme, host or port) requires replacing or clearing every
retained credential, including when customizing. Plain HTTP is supported but
does not encrypt credentials in transit; use HTTPS outside trusted local paths.

**Connection checks:** Metrics saves first and then tests reachability; an
unreachable backend remains saved with a warning. Argo CD and explicit Kubecost
changes test an isolated candidate before saving; a failed test leaves the
previous connection active. Selecting Auto-detect without an explicit Kubecost
URL, or Prometheus-based cost mode, saves that preference without claiming a
successful backend test. Updating a shared Kubecost backend accepts a
valid empty allocation response: backend reachability/authentication is distinct
from each cluster's data readiness. Actual cost queries still require a narrow
cluster filter. Central Argo connectivity does not add cross-cluster resource
discovery.

**Cleanup:** explicitly disconnecting, replacing or forgetting the last
assignment asks to delete its now-unused connection and credentials; **Keep for
reuse** is optional. An assigned connection cannot be deleted. Missing
kubeconfigs never trigger automatic deletion. Manage saved connections includes
unavailable assignments and context-specific discovery settings.

Unsaved edits stay in the dialog when switching Settings tabs. Closing it asks
before discarding them. If another client changes the active cluster while you
are editing, Radar freezes the old draft; explicitly discard and reload to edit
the new cluster. A stale save is rejected rather than applied to a different
context or merged with a concurrent file edit.

#### File editing and refresh

Let Settings create opaque connection IDs, context keys and accepted target
fingerprints. The store separates `connections` (typed endpoint/credential
records) from `profiles[context-key].integrations` (assignments by `metrics`,
`argocd` and `cost`). For example, within an existing metrics connection:

```json
{
  "type": "metrics",
  "name": "Team metrics",
  "prometheus": {
    "url": "https://metrics.example.net/prometheus",
    "headersFromEnv": {
      "Authorization": "PROMETHEUS_TOKEN",
      "X-Scope-OrgID": "PROMETHEUS_TENANT"
    }
  }
}
```

`name` is optional. Environment variables must exist in the Radar process;
Desktop does not necessarily inherit your terminal environment. Missing variables
pause only the affected connection. Environment-backed header references are
file-edited, not editable in Settings. See the [JSON schema](schemas/clusters.schema.json);
Radar also validates cross-record references and integration-specific rules.

CLI and Desktop reread bounded file contents on the next integration operation,
including background consumers, not only when Settings opens. A content hash
avoids reparsing unchanged data. Concurrent saves use a file lock and revision
checks: stale drafts must reload rather than overwrite another process.
Malformed JSON, unknown fields or an unsupported version block the file without
overwriting it. A semantically invalid connection blocks its consumers, not
unrelated valid connections; unrelated edits preserve that invalid entry.

#### Cluster identity and recovery

The assignment key includes the kubeconfig source path and in-file context name.
Same-named contexts in different files do not share credentials. If a context is
renamed or its file moved, select its saved connection on the new context, then
forget the old assignment explicitly. Discovery-only credentials may require
reentry. An unavailable kubeconfig is distinguished from a context confirmed
removed from a readable file.

CAPI assignments use management-source/namespace/name identity, not temporary
kubeconfig paths. Changes to the Kubernetes server, CA trust, TLS settings, proxy
or user reference pause each affected integration. **Review changes** lets you
accept selected integrations together. Anonymous discovery without a saved
mapping needs no confirmation. Token rotation alone does not prompt. Replacing
a cluster behind identical endpoint, trust and user reference is not detectable
by this fingerprint.

#### Startup overrides and older settings

An explicit local `--prometheus-url` is a complete, read-only **Set for this
launch** override bound to the initial context and target. It never inherits
saved headers; local header flags require the URL flag in the same launch.
Argo CD and cost environment overrides similarly form complete, context-bound
launch configurations, not layers over saved credentials. Switching away
disables the override; returning to that target restores it. Restart without
the override to edit its saved settings. No new flags are required.

Workload-metrics scope assertions remain process-local and clear on a context
switch attempt, even an unsuccessful one. Saved connections never restore them.
Repairing a connection that was unusable at startup resumes automatic matching,
not its startup assertion.

Older global `config.json` integration settings never activate automatically in
local mode. Settings offers **Use for this cluster** to import them explicitly.
An explicit endpoint is imported once per integration, then reused through
**Use saved connection…**. Discovery-bound credentials respect their original
context binding. **Stop offering these older settings** dismisses the offer.
The old file stays as a recovery copy, never a fallback; removal does not
resurrect it. Older Radar versions still read that global file, so rolling back
does not preserve the new context-scoped behavior.

In-cluster OSS settings remain Helm/operator-controlled and read-only in the UI.
Cloud remains installation-scoped in this change; `clusters.json` is not a Hub
configuration transport.

The profile file and lock are created with Unix mode `0600`; a newly created
directory uses `0700`. Existing directory permissions are not changed. Headers
are plaintext, not encrypted: protect backups and your OS account. When creating
or replacing the file manually on Unix, set `chmod 600 ~/.radar/clusters.json`;
Radar does not change existing file permissions merely by reading it. Atomic
replacement prevents partial writes but does not guarantee the latest save
survives sudden power loss. On Windows,
protect access with user-directory ACLs; Unix modes are not an ACL guarantee.
Locking is intended for local filesystems, not validated for network homes.
On Windows, an open reader or antivirus scanner may briefly prevent atomic
replacement. A failed save leaves the previous file and running connection
unchanged; reload and retry after that reader finishes.

Shared OSS/Cloud configuration remains installation-scoped. A shared deployment's
header-only configuration may still pair with its URL flag; changing a saved
server requires replacing every inherited header source. These installations do
not read local profiles.

Cross-origin HTTP redirects are refused, including for custom auth
and tenant headers. Headers require an explicit URL and are not sent during auto-discovery.
The new workload charts recheck identity, but older name-based metrics are not
protected by that attribution contract. See [Workload metrics](workload-metrics.md).

In-cluster Radar has no context switcher. Configure the integration for that
installation, using Secret-backed environment variables for credentials where
supported; local config persistence is not a replacement for deployment configuration.

## Workload metrics overrides by run mode

Workload metrics normally use automatic identity matching. The optional
`--prometheus-single-cluster` and repeatable `--prometheus-cluster-label` flags
replace that matching for workload request/resource charts, history and Pod
comparison only. They do not scope rightsizing, node/HPA/PVC or legacy
network/storage queries. Leave both unset for automatic matching.

| Run mode | How to configure an explicit override |
|---|---|
| Local CLI / `kubectl radar` | Supply the flag on each launch that needs it. These overrides have no `config.json` key or environment-variable equivalent. |
| Desktop | Automatic matching is supported; these overrides are not exposed in Desktop's flag parser or Settings. Use standalone CLI Radar if an explicit override is required. |
| In-cluster OSS | The operator supplies `traffic.prometheusSingleCluster` or `traffic.prometheusClusterLabels` through Helm/GitOps, which applies them at process startup. |
| Radar Cloud | The installation's operator configures the same agent Helm values; this is not a viewer preference or a Hub authentication setting. |

Changing the Kubernetes connection, logical metrics backend or headers clears
an active override and resumes automatic matching. A new local port-forward to
the same discovered Service is not a different backend. Only reapply an override
after verifying the new connection's scope. See [operator override details](workload-metrics.md#optional-operator-override).

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

## Audit evidence under partial access

The unused ConfigMap/Secret check distinguishes observed use from evidence of no
use. A visible workload or supported controller reference establishes use. An
unused finding requires the relevant consumer inventories to be readable under
Radar's audit scope and initially synced for that namespace. Unknown subjects
contribute neither findings nor passing/evaluated counts. Scan-level
`missingInputs` reports `configmap-references` or `secret-references` when this
prevents an evaluation; zero findings alone do not establish a complete scan.

The namespace picker selects audit subjects. For Reflector-annotated subjects,
authorized visible consumers of remote mirrors can still establish source use.
Unreadable cross-namespace consumers can prevent proving a Secret unused even
when all local workloads are visible. ClusterIssuer credential namespaces are
resolved from an observed cert-manager controller's explicit flag, including its
literal or downward-API namespace environment value; an unresolved namespace is
unknown. Explicit flags take precedence over configuration files; no controller default or unread file value is assumed.

These checks cover Radar's supported reference fields, not arbitrary controller
behavior. Cache readiness records initial synchronization, not continuous watch
freshness; the existing scan memo can lag evidence changes by up to five seconds.
The per-resource audit endpoint remains a findings array and has no completeness
metadata. Use the scan response or AI resource context when that distinction matters.

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
