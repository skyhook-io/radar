# CLI reference

Radar runs as either `radar` or the kubectl plugin `kubectl radar`. This page covers operator-facing commands and flags; `radar --help` is the complete reference for the installed version.

## Commands

| Command | Purpose |
|---|---|
| `radar` | Start Radar and open the UI. |
| `kubectl radar` | Start the same server through kubectl plugin discovery. |
| `radar diagnose <kind>/<name> -n <namespace>` | Run an AI investigation against a running Radar instance. Add `--standalone` to use a temporary local instance. |
| `radar cloud install` | Install or adopt an in-cluster Radar and connect it to Radar Cloud. |
| `radar cloud status` | Inspect installation ownership, readiness, and tunnel health without changing the cluster. |
| `radar --version` | Print the version and exit. |

Run `radar diagnose --help` or `radar cloud --help` for command-specific flags.

## Cluster and server

| Flag | Default | Purpose |
|---|---:|---|
| `--kubeconfig` | `~/.kube/config` | Use one kubeconfig file. |
| `--kubeconfig-dir` | | Load kubeconfig files from comma-separated directories; mutually exclusive with `--kubeconfig`. |
| `--namespace` | all | Set the initial namespace filter. |
| `--namespaces` | all | Set comma-separated initial filters for namespace-restricted identities. |
| `--namespace-scope` | `false` | Limit namespaced informer caches to one namespace. |
| `--port` | `9280` | HTTP port. |
| `--listen-address` | `127.0.0.1` | Bind address; shared listeners should use authentication and network controls. |
| `--base-path` | | Serve under a prefix such as `/radar`; not supported with Radar Cloud. |
| `--no-browser` | `false` | Do not open the UI automatically. |
| `--browser` | system default | Choose the browser used for automatic launch. |

Kubeconfig precedence, multi-file behavior, and namespace scoping are covered in [Configuration](configuration.md).

## Storage and integrations

| Flag | Default | Purpose |
|---|---:|---|
| `--timeline-storage` | `memory` | Use `memory` or `sqlite` timeline storage. |
| `--timeline-db` | `~/.radar/timeline.db` | SQLite timeline path. |
| `--timeline-retention` | `168h` | Age limit for SQLite events; `0` disables age pruning. |
| `--timeline-max-size` | `1Gi` | SQLite size limit before pruning; `0` disables size pruning. |
| `--history-limit` | `10000` | Maximum in-memory timeline events. |
| `--ai-history=false` | `true` | Disable persistence of Diagnose investigations. |
| `--prometheus-url` | auto-discover | Use a specific Prometheus-compatible endpoint. |
| `--prometheus-header` | | Add a `Key=Value` header; repeatable. |
| `--prometheus-header-from-env` | | Source a `Key=ENV_VAR` header; repeatable. |
| `--beyla-job-selector` | built-in | Override the PromQL job matcher used for Beyla traffic. |
| `--debug-image` | `busybox:latest` | Override the image used for debug containers and pods. |
| `--reachability-image` | automatic | Override the in-cluster reachability probe image. |
| `--pod-shell-default` | automatic | Override the pod exec shell command. |
| `--disable-local-terminal` | `false` | Disable the host terminal. |
| `--no-mcp` | `false` | Disable the MCP endpoint. |

## Shared deployments

`--auth-mode` accepts `none`, `proxy`, or `oidc`. Shared deployments can set `--auth-secret` for session signing and `--auth-cookie-ttl` for session lifetime. Proxy mode uses the `--auth-user-header`, `--auth-groups-header`, and optional logout URL flags. OIDC uses the `--auth-oidc-*` family for issuer, client, claim, endpoint, TLS, logout, and PKCE settings. See [Authentication](authentication.md) for deployable examples.

The `--cloud-url`, `--cloud-token`, and `--cluster-name` flags configure an in-cluster agent tunnel. Prefer `radar cloud install` or the Helm chart over assembling those flags manually.

## Large or slow clusters

| Flag | Default | Purpose |
|---|---:|---|
| `--list-page-size` | `0` | Paginate initial Pod and ReplicaSet LISTs when WatchList is unavailable. |
| `--context-switch-timeout` | `30s` | Context-switch deadline. Env: `RADAR_CONTEXT_SWITCH_TIMEOUT`. |
| `--first-paint-backstop` | `5m` | Maximum initial critical-cache wait. Env: `RADAR_FIRST_PAINT_BACKSTOP`. |
| `--namespace-list-timeout` | `5s` | Namespace discovery deadline. Env: `RADAR_NAMESPACE_LIST_TIMEOUT`. |
| `--max-scope-candidates` | `20` | Namespace fallback-probe fanout. Env: `RADAR_MAX_SCOPE_CANDIDATES`. |

Persistent defaults live in `~/.radar/config.json`; explicit CLI flags take precedence. Development and test-only switches are intentionally omitted here and remain visible in `radar --help`.
