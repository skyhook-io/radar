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

## AI Investigations Agent CLI

Built-in AI investigations run an agent CLI installed on the machine Radar runs
on: Claude Code (`claude`), Codex (`codex`), Cursor (`cursor-agent`), or
OpenCode (`opencode`). Radar looks for them on the `PATH` it was started with,
then in these install directories:

| OS | Directories |
|----|-------------|
| macOS, Linux | `~/.local/bin`, `~/.claude/local`, `~/.opencode/bin`, `~/.volta/bin`, `/opt/homebrew/bin`, `/usr/local/bin`, `/home/linuxbrew/.linuxbrew/bin`, `/usr/bin` |
| Windows | `%LOCALAPPDATA%\Microsoft\WinGet\Links`, `%LOCALAPPDATA%\cursor-agent`, `%LOCALAPPDATA%\Programs\OpenAI\Codex\bin`, `%USERPROFILE%\.local\bin` |

Radar picks up a newly installed CLI automatically, or when you click **Check
again** in AI investigations. The startup output has an `AI investigations`
line that says which agent Radar will use, or why investigations are off.

If the CLI is somewhere else, for example an npm global install under nvm, start
Radar from a terminal where the CLI works, or set `RADAR_AI_CLI_BIN` to its full
path:

```bash
RADAR_AI_CLI_BIN="$(command -v claude)" radar
```

```powershell
$env:RADAR_AI_CLI_BIN = "C:\path\to\claude.exe"; radar
```

With the variable set, Radar uses only that CLI and stops looking for others.
It picks the driver from the file name: a name containing `cursor` runs as
Cursor, `codex` as Codex, `opencode` as OpenCode, and anything else as Claude
Code. If the path is not an executable Radar can run, investigations stay off
until a working CLI is at that path, or the variable is changed and Radar
restarted. The startup line names the path.

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
ignores those saved settings. Radar does not rewrite `config.json` or `settings.json`
during this transition.

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
| `opencostCurrency` | Optional ISO 4217 override for values produced by OpenCost or Kubecost. Empty reads `currencyCode` from the pricing ConfigMap referenced by an active OpenCost/Kubecost workload, or literal `DISPLAY_CURRENCY` from an active Kubecost Deployment or StatefulSet, when the selected cost source is tied to the connected cluster; otherwise it falls back to `USD`. In local Settings → Cost, this preference saves automatically for all local clusters, independently of source testing, so it can be changed while a source is unavailable. Radar labels values but does not convert them. Equivalent CLI: `--opencost-currency`; an explicit CLI value remains authoritative while Radar runs and after restart. |
| `costSource` | `auto` (default), `prometheus`, or `kubecost`, shown in Settings as **Automatic**, **OpenCost metrics** and **Kubecost**. Automatic keeps working OpenCost metrics from a PromQL-compatible backend, then tries a Kubecost 3 Aggregator; if neither is present, selection remains unavailable and retries instead of reporting an absent source as active. Local Settings saves this preference per context; connection-check behavior is described below. |
| `kubecostUrl` | Optional Kubecost 3 Aggregator base URL. Empty discovers an active local Aggregator Service and tries its named `tcp-api` port (9004). When that port requires SAML/OIDC and no API key is configured, Radar can fall back to the same Service's exact `tcp-api-rbac` port (9008). Federated agent-only clusters need the central URL; root API URLs and URLs ending in `/model` are accepted. |
| `kubecostClusterId` | Cluster ID used to filter a central Aggregator. Empty detects one distinct literal `CLUSTER_ID` from an active FinOps Agent or Aggregator; indirect or conflicting values require an override. Local Settings stores the override with this context's Cost settings; copying another cluster's backend never copies its cluster ID. |
| `kubecostApiKey` | Optional Kubecost service-account key sent as `X-API-KEY`. Values are redacted from settings responses. An explicit key is never bypassed through an auto-discovered unauthenticated port: authentication failure remains visible. Local CLI/Desktop stores new keys in `clusters.json`, with explicit reuse and origin-change protection described below; this `config.json` field is only an import source locally. In Helm, provision a Kubernetes Secret with `cost.kubecost.existingSecret`; Settings is read-only. |
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
settings are keyed by kubeconfig source file and in-file context name instead
(see [cluster identity and recovery](#cluster-identity-and-recovery)).
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
| Metrics URL, authentication and tenant headers | Stored directly for this kubeconfig context |
| Argo CD URL, token and TLS verification | Stored directly for this context, including discovery credentials |
| Cost source (Automatic / OpenCost metrics / Kubecost) | Context-specific |
| Kubecost URL and API key | Stored directly for this context, including discovery credentials |
| Kubecost cluster ID | Context-specific, **never** inherited from another cluster's settings |
| Prometheus-based costs | Use this context's metrics connection |
| Workload-metrics scope evidence / assertion | Rechecked; process-local assertions are not stored in a connection |
| Namespace selection | Remembered per visible kubeconfig context in `settings.json` |
| Currency, UI preferences, OCI sources | Existing machine/personal scope; not per-cluster connections |

### Local integration connections

In **Settings → Metrics / Argo CD / Cost**, configure the selected context and
choose **Save changes**. **Discard** resets the form without changing saved settings.
Switching A → B → A restores A's settings. A context with no saved settings uses
discovery. Local CLI and Desktop share `~/.radar/clusters.json`.

**Use auto-discovery**, below the backend URL, stages an empty connection and
names what saving will remove from this context: the endpoint and credentials.
On Cost the same action is **Reset to Automatic**: it also returns the cost
source to Automatic and removes the Kubecost cluster mapping. **Save changes**
applies it; **Discard** restores the saved settings. The action is disabled when
already automatic without overrides.

**Copy:** on another context, choose **Copy settings from…** and select the
source from the searchable picker: another context, or **Previous global
settings** (see below). The current form becomes an
unsaved draft; adjust the endpoint or credentials before choosing **Save changes**.
Choosing another source replaces the draft without saving it.
**Discard** restores the previous settings. Replacing an existing connection
requires confirmation when saving. This makes an independent copy, not a shared reference. Later edits
affect only the selected cluster.
Another context appears as a source only when it has a saved explicit endpoint
(URL) for that integration; its discovery-only credentials cannot be copied, so
enter them again on each context. Previous global settings are listed separately
and can include URL-less sources.

Only copy a backend that serves the destination cluster. A reachable endpoint
does not prove it contains that cluster's data. Authentication and tenant headers
are copied server-side on Save; their values are never returned to the browser.
You can replace or remove them in the draft. If the source changes before Save,
Radar rejects the stale copy rather than silently using different credentials.
Kubecost's source cluster mapping is never copied: keep or edit the destination ID, or
allow discovery to detect it.
Environment-backed headers retain references, not resolved secret values; both
copies can still depend on the same environment variable.

**Edit:** the regular form always edits this context only. Each context stores
its own endpoint and credentials.

**Credentials:** Settings displays header names and whether a token/key exists,
never saved values. Type directly into a credential field to replace its value;
leave it untouched to keep it, or choose **Remove** to clear it when saving.
Changing URL origin (scheme, host or port) requires replacing or clearing every
retained credential, including after copying. Plain HTTP is supported but
does not encrypt credentials in transit; use HTTPS outside trusted local paths.

**Connection checks:** Metrics saves first and then tests reachability; an
unreachable backend remains saved with a warning. Saving an Argo CD endpoint or
token, Kubecost mode, or Automatic with any Kubecost override (URL, API key or
cluster ID) tests an isolated candidate first; a failed test blocks the save and
leaves the previous connection active. Switching to auto-discovery and confirming
a changed cluster save without a test. Automatic with no Kubecost overrides, or
Prometheus-based cost mode, saves that preference without claiming a
successful backend test. Explicit Kubecost connections check this cluster's
mapping and data readiness; cost queries require a narrow cluster filter. Central Argo connectivity does not add cross-cluster resource
discovery.

**Cleanup:** switching to discovery or replacing saved settings explicitly removes
this context's previous credentials.
Other contexts are unchanged. Missing kubeconfigs never trigger automatic deletion.
**Settings → Connection → Integration settings by cluster** lets you explicitly remove an
integration for a kubeconfig entry no longer loaded, including discovery
credentials and mappings. For the current context, use its integration tab.
An entry marked **Kubeconfig not loaded** comes from a kubeconfig file this Radar
run didn't load; it may still be in use by another Radar process.

Unsaved edits stay in the dialog when switching Settings tabs. Closing it asks
before discarding them. If another client changes the active cluster while you
are editing, Radar freezes the old draft; explicitly discard and reload to edit
the new cluster. A stale save is rejected rather than applied to a different
context or merged with a concurrent file edit.

#### File editing and refresh

Let Settings create context keys and accepted target fingerprints. Each entry
stores its own configuration directly under `profiles[context-key].integrations`,
keyed by `metrics`, `argocd` and `cost`. For example, within an existing
context's `integrations.metrics` settings (keep the actual `target` and
`identity` generated by Radar):

```json
{
  "target": "<existing accepted target fingerprint>",
  "prometheus": {
    "url": "https://metrics.example.net/prometheus",
    "headersFromEnv": {
      "Authorization": "PROMETHEUS_TOKEN",
      "X-Scope-OrgID": "PROMETHEUS_TENANT"
    }
  }
}
```

Environment variables must exist in the Radar process;
Desktop does not necessarily inherit your terminal environment. Missing variables
pause only the affected connection. Environment-backed header references are
file-edited, not editable in Settings. Radar rejects unknown fields and
validates accepted cluster targets, URLs, HTTP headers and integration-specific
rules when it reads the file; an invalid connection pauses only that integration.

CLI and Desktop reread bounded file contents on the next integration operation,
including background consumers, not only when Settings opens. A content hash
avoids reparsing unchanged data. Concurrent saves use a file lock and revision
checks. Draft revisions cover the selected context and integration, including
redacted credentials: saving Argo CD or another cluster does not invalidate an
unfinished Metrics draft. The exception: importing previous settings with an
explicit endpoint on any context requires open drafts for that integration on
other contexts to reload. A concurrent edit to the same settings requires reloading.
Copy also checks the source revision, so an endpoint or secret cannot change
unnoticed between selection and applying the copy.
The final write also checks the entire file, so overlapping saves during a
connection test can still conflict rather than overwrite another process.
Malformed JSON, unrecognized fields or an unsupported version block the file
without overwriting it. A recognized field used with the wrong integration, or
another semantically invalid connection setting, blocks only that integration;
unrelated edits preserve the invalid connection. Some empty JSON values accepted
by the reader are normalized when written; the schema describes the written form.

The stored format is versioned independently of Radar releases. Future format
changes must retain support for existing v1 files or provide an explicit
migration. Older readers reject unfamiliar fields and versions rather than
silently dropping data. A newer format produces upgrade guidance, not an
instruction to repair or delete the file.

#### Cluster identity and recovery

The context key includes the kubeconfig source path and in-file context name.
Same-named contexts in different files do not share credentials. If a context is
renamed or its file moved, copy its saved connection to the new context, then
remove the old connection from **Settings → Connection → Integration settings by cluster**.
Tools that write a new temporary kubeconfig for each shell produce a new key
each time; launch Radar with `--kubeconfig` pointing at the stable file instead.
That section groups saved integrations by kubeconfig entry; removal is offered
for entries no longer loaded, with a confirmation for the selected integration.
Each integration reads as saved, or as auto-discovery (Cost: Automatic) when
it was switched back and no endpoint, credential or cluster mapping is left.
Overview's collapsed **Configuration files** section explains the three local files and
credential storage; the info button beside the cluster name in each integration
tab identifies its kubeconfig entry. In-cluster installations show operator
configuration guidance instead of local-file information. Discovery-only credentials may require
reentry. An unavailable kubeconfig is distinguished from a context confirmed
removed from a readable file.

CAPI profiles use management-source/namespace/name identity, not temporary
kubeconfig paths. Changes to the Kubernetes server, CA trust, TLS settings, proxy
or user reference pause each affected integration, because a saved endpoint may
now serve a different cluster's data. **Review changes** asks whether the
settings still apply and lets you keep selected integrations together. To
decline instead, **Use auto-discovery instead** (Cost: **Reset to Automatic**)
removes that integration's saved endpoint and credentials after a confirmation,
including a saved token or key with no URL. Anonymous discovery without a saved
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
the override to edit its saved settings.

Workload-metrics scope assertions remain process-local and clear on a context
switch attempt, even an unsuccessful one. Saved settings never restore them.
Repairing a connection that was unusable at startup resumes automatic matching,
not its startup assertion.

Older global `config.json` integration settings are not applied to every
cluster in local mode. When the loaded kubeconfig has exactly one context, Radar
assumes they were meant for it and imports them at startup. This happens once,
when `clusters.json` is first created: the first such launch imports, settings
removed later are not restored, and other single-context kubeconfig files launched
afterwards use the picker below. Integrations set by startup flags or environment
are left alone. With several
contexts, **Copy settings from… → Previous global settings** in each integration
tab fills an editable draft; **Save changes** imports it for the current cluster,
while **Discard** leaves its connection unchanged. Picker entries describe
URL-less sources (for example, auto-discovery with a saved API key), and invalid
previous settings stay listed with their error. Saved credentials remain hidden.
An explicit endpoint is imported once per integration, then reused by copying
from that context; URL-less previous settings stay available to every context.
Discovery-bound credentials and Kubecost mappings respect their original context
binding, and the draft names anything left behind. Settings → Overview explains
the change and links to the affected tabs until you choose **Don’t show again**,
which silences these reminders everywhere without removing the picker entry.
The old file stays as a recovery copy, never a fallback; removal does not
resurrect it. Older Radar versions still read that global file, so rolling back
does not preserve the new context-scoped behavior.

Missing-connection hints in Metrics, Rightsizing, PVC usage, Cost (including
workload and application tabs), and Argo CD diff/health views keep their usual
**Configure** action and add a note when previous global settings can be copied
for this cluster. Metrics and Cost also note when another context's saved settings
are available to copy if that backend serves this cluster; Argo CD does not, because
the server that matters is the one owning the Applications in this cluster. The
action opens the relevant Settings tab and never imports or saves anything.
Existing errors remain visible, and a previous connection is not a guarantee
that its backend is reachable. Working views are unchanged: if auto-discovery
finds a working backend, Settings → Overview still explains the change.
These recovery hints are for local CLI/Desktop configuration, not operator-managed
or embedded Cloud installations.

In-cluster OSS settings remain Helm/operator-controlled and read-only in the UI.
Cloud remains installation-scoped; `clusters.json` is not a Hub
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

## Local terminal context

Each local terminal tab shows the context Radar put in its temporary kubeconfig,
with **Opened for** in the terminal toolbar. Hover over the label to see the full
context name. This records the terminal's startup configuration; shell settings,
`KUBECONFIG` changes, and explicit command flags can override it.

Switching clusters in Radar leaves existing local shells running. A terminal
opened for another context shows **Different context** and a **New terminal**
action in the existing toolbar. Hover over the notice to see both full context
names. The action opens a shell for the context Radar is now showing.
Each tab retains the full context selected when it was opened. Reconnecting
starts a new shell only while Radar has that context selected; otherwise it
asks you to switch back or open a new terminal for the current context. The
tab keeps its previous context label during reconnect attempts.
Reconnect and Retry are disabled while Radar is showing a different context.
When a context is selected, the toolbar says **Requested** before the first shell starts. The server checks
the requested context against the same snapshot it exports, so switching
contexts while a tab is opening cannot start that shell for a different context.
Commands supplied by actions such as **Authenticate in terminal** are sent once
per tab. Reconnecting does not repeat a command that was already sent. If the
connection closes before it is sent, the command remains pending for the next
connection.

If Radar cannot create a temporary kubeconfig, the existing original/inherited
kubeconfig fallback remains available and the tab says **Context not confirmed**.
That shell's Kubernetes target has not been established by Radar. If the selected
context differs from the tab's requested context, its notice says **Requested
context differs**, and the tooltip keeps that request distinct from a confirmed
kubeconfig.
The Desktop app can also open an unconfirmed recovery shell when no kubeconfig
is available. Its explicit empty-context intent must match the server's empty
active context; once a context is selected, open a new terminal for it.
These checks preserve context selection across opens and reconnects; they do
not enforce a shell's live command target or detect a kubeconfig context being
repointed to a different physical cluster under the same name.
If a context switch fails before Radar changes its active client, a terminal
requested for the new context is refused. Finish recovering that connection,
switch back to the previous context, or copy the recovery command into an
external terminal. Radar will not substitute a shell for the previous context.

## Namespace Picker

The header has a namespace picker on the right. Pick a single namespace to focus the view, or **All namespaces** to see everything you have access to. Cluster-scoped resources (Nodes, Namespaces, PVs, StorageClasses) appear regardless of the pick if your RBAC permits them — they have no namespace to filter on. Namespace-restricted users without their own cluster-scoped RBAC won't see cluster-scoped sections at all.

The pick is a per-user view filter — it doesn't change anything for other users sharing the same Radar instance. Locally, your pick is remembered per kubeconfig context across restarts. In shared (auth-enabled) deployments the pick lives for the session.

Until you make a pick, local sessions default to the namespace set on the kubeconfig context (kubectl parity — the same namespace `kubectl` would use, including one set via `kubectl config set-context` or `kubens`). An explicit `--namespace` / `--namespaces` flag outranks the kubeconfig value, and contexts without either default to **All namespaces**. Once you pick namespaces or explicitly choose **All namespaces**, that choice sticks for the context and the kubeconfig value is no longer consulted.

When Radar starts with `--namespace-scope`, the picker controls the process-wide cache scope instead of just a view filter. Namespaced informer caches are pinned to one namespace while cluster-scoped resources remain cluster-wide. Local/no-auth sessions can switch the scoped namespace, which rebuilds the cache in place. Auth-enabled and Radar Cloud sessions lock the picker to the startup namespace so one user cannot reshape the shared backend cache for everyone.

**Single namespace only.** `--namespace-scope` pins the cache to exactly one namespace; scoping to several namespaces at once is not supported yet. Passing more than one (e.g. `--namespace=a,b`) fails at startup with a clear error rather than silently caching nothing. When scoped, the namespace picker becomes single-select, and a switch re-points the whole cache to the new namespace rather than adding to it.

### Namespaces missing from the picker

If your account can use resources in some namespaces but isn't allowed to list namespaces cluster-wide, Radar can't discover which namespaces exist. The picker then shows only the namespaces Radar has been given: the kubeconfig context's namespace and any you configure. Add every namespace you use, then restart Radar:

```bash
kubectl radar --namespaces ns1,ns2,ns3
```

Radar Desktop doesn't take command-line flags, so set the same list in `~/.radar/config.json` (this works for the CLI too):

```json
{ "namespaces": ["ns1", "ns2", "ns3"] }
```

Radar probes each listed namespace for access and watches every namespace where access is granted — resource views then cover all of them, not just the first. The list is also each user's initial picker selection: locally via the launch URL, and in shared (auth-enabled) deployments as a per-session default seeded on first read. Clearing the picker back to **All namespaces** sticks for the rest of the session. The picker can switch between those namespaces or keep several selected at once.

This covers built-in resource types and custom resources alike: CRDs (GitOps, Gateway API, etc.) are probed per-kind across the same list and watched in every granted namespace. The list is capped by `--max-scope-candidates` (default 20) — startup fails with a clear error rather than silently probing a subset.

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

### Storage review checks

Cluster Audit includes three snapshot checks in **Efficiency**, at **Medium**
posture priority (`warning` in the raw scan):

| Check ID | Observation |
|---|---|
| `pvcNoConsumer` | A Bound PVC older than 24 hours, without a controller owner, has no consumer observed among readable Pods and built-in workload templates. Findings include requested size, storage class, PVC age, bound PV and reclaim policy when visible. |
| `pvcLongPending` | A PVC without a controller owner has been Pending since creation, more than 24 hours ago, and no Pod or built-in workload template references it. WaitForFirstConsumer claims without a consumer are included. |
| `releasedPV` | A Released PV with Retain is kept after claim deletion for more than 24 hours. With Delete, deletion has not completed after one hour, even without warning events. A Warning `VolumeFailedDelete` event for the PV's UID within the last 24 hours reports deletion failure immediately; the latest warning message is included, with its age when older. |

Consumer evidence includes Deployments, ReplicaSets, StatefulSets, DaemonSets,
Jobs and CronJobs. Templates count at zero replicas, and retained StatefulSet
claim-template ordinals count even after scaling down. Terminal Pods and Jobs
also count conservatively while their objects exist. Claims with a controller
owner reference are excluded: hibernated databases, stopped virtual machines,
and workflow controllers can intentionally retain claims without Pods. CRD
consumers (including virtual machines) and external consumers can still need a claim: **no consumer
observed is not a statement that storage is safe to delete**. The scan provides
no deletion action, cost estimate, or history of nonuse.

Unreadable or initially unsynced consumer inventories prevent absence findings
and passing counts for that namespace (`pvc-consumers` in `missingInputs`).
PVs and StorageClasses require the caller's exact cluster-scoped list grant.
Unavailable inventories appear as `persistentvolumes` or `storageclasses` only
when a scanned claim needs them. Bound no-consumer findings still appear without
PV access, with **reclaim policy not visible**. An unconsumed Pending claim with
unknown binding mode still appears, with `pvc-binding-mode` reported; consumed
Pending claims are excluded regardless of binding mode.

The namespace picker selects PVC subjects. Released PVs remain cluster-scoped
and require their own grant. All readable PVs count toward `releasedPV`, including
healthy volumes. Events are listed only when a Released Delete volume needs
deletion evidence, and only from namespaces the caller may see. Unreadable,
partial or unsynced deletion-event coverage reports `pv-deletion-events` when
supplemental warning evidence is unavailable; the delayed-deletion finding still
appears based on the PV itself. PV findings use time in Released when
`status.lastPhaseTransitionTime` is present, otherwise use and explicitly show
**PV age** for the thresholds. Neither is a history of how long the data was
unneeded.

## Radar Cloud

Radar is free and fully functional without an account. A Cloud button in the
header offers to connect the cluster to [Radar Cloud](https://app.radarhq.io) —
optional, and nothing else depends on it.

| Variable | Effect |
|---|---|
| `RADAR_CLOUD_FUNNEL=off` | Removes the Cloud button entirely. `on` forces it on. |
| `RADAR_HUB_URL` | Connect to your own Radar Cloud Self-Managed control plane instead of the hosted service. |
| `RADAR_HUB_APP_URL` | That control plane's web origin, when it differs from `RADAR_HUB_URL`. |

To connect a cluster from the command line, use `radar cloud install`
(`--hub-url` for a Self-Managed control plane).
`radar cloud install` and `radar cloud status` target one cluster, so they use
the configured primary kubeconfig and report configured directories they
ignore. With no configured source, they use the normal `KUBECONFIG` / default
kubeconfig loading rules. Directory-only configuration must add a primary
kubeconfig before these commands can run.

### What Radar sends

Until you connect a cluster to Cloud, Radar makes three kinds of outbound
request, all to Skyhook, none containing your resources, logs or events:

- **Update check** — to `releases.skyhook.io`, with the Radar version, OS/arch,
  install method, whether it is running locally or in-cluster, and the
  installation timestamp when Radar can determine it. Radar caches the release
  result for one hour. Development builds are excluded.
- **[Anonymous usage stats](usage-stats.md)**: once a day, strictly opt-in.
- **Cloud dialog copy** — only when you *open* the Cloud dialog, to fetch the
  current terms shown in it. No identifiers are sent. `RADAR_CLOUD_FUNNEL=off`
  stops this request from ever happening.

A standalone Radar sends your cluster's data nowhere: it talks to your
Kubernetes API directly and keeps everything it reads on your machine.

Connecting a cluster to Cloud is what changes that, and it is the point of
connecting — the cluster's agent opens an outbound tunnel to the Hub so the
team can reach the same views without each person holding kubeconfig access.
Deciding whether to connect is a separate question from the requests above.

## Related Documentation

- [CLI reference](https://radarhq.io/docs/configuration/cli) — Commands and operator-facing flags
- [In-Cluster Deployment](in-cluster.md) — Deploy Radar inside your cluster with Helm
- [Authentication & Authorization](authentication.md) — Proxy and OIDC auth for shared deployments
