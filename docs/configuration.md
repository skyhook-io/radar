# Configuration

This document covers Radar's cluster connection behavior. For CLI flags and basic usage, see the [README](../README.md#usage).

## Persistent Configuration

Radar stores configuration in two files under `~/.radar/`:

### Config File (`~/.radar/config.json`)

Persistent defaults for CLI flags. CLI flags always override these values. Managed via the Settings dialog in the UI or `PUT /api/config`.

```json
{
  "kubeconfig": "",
  "kubeconfigDirs": [],
  "namespace": "",
  "port": 9280,
  "noBrowser": false,
  "timelineStorage": "memory",
  "timelineDbPath": "~/.radar/timeline.db",
  "historyLimit": 10000,
  "prometheusUrl": "",
  "mcp": true
}
```

All fields are optional — omitted fields use built-in defaults.

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

## Cluster Connection Precedence

Radar connects to Kubernetes clusters using the same configuration sources as `kubectl`:

| Priority | Source | Description |
|----------|--------|-------------|
| 1 | `--kubeconfig` flag | Explicit path to kubeconfig file |
| 2 | `KUBECONFIG` env var / `--kubeconfig-dir` flag | Either can provide kubeconfig(s); mutually exclusive alternatives |
| 3 | In-cluster config | Automatic when running inside a Kubernetes pod (`KUBERNETES_SERVICE_HOST` is set) |
| 4 | `~/.kube/config` | Default kubeconfig location |

## KUBECONFIG vs In-Cluster Detection

When Radar runs inside a Kubernetes pod, Kubernetes automatically sets the `KUBERNETES_SERVICE_HOST` environment variable. This normally triggers in-cluster configuration using the pod's service account credentials.

However, **explicit kubeconfig takes precedence**. If you set `KUBECONFIG` or pass `--kubeconfig`, Radar uses that instead of in-cluster config. This allows you to:

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

`KUBECONFIG` can contain multiple file paths (colon-separated on Linux/macOS, semicolon-separated on Windows). Radar merges these files following Kubernetes conventions:

```bash
export KUBECONFIG=~/.kube/config:~/.kube/staging-config:~/.kube/prod-config
kubectl radar
```

Alternatively, use `--kubeconfig-dir` to load all kubeconfig files from a directory:

```bash
kubectl radar --kubeconfig-dir ~/.kube/configs/
```

## Context Switching

Radar supports switching between Kubernetes contexts at runtime through the UI. Click the context selector in the header to switch between available contexts.

When running in-cluster (using the pod's service account), context switching is disabled.

## Related Documentation

- [README](../README.md#usage) — CLI flags and basic usage
- [In-Cluster Deployment](in-cluster.md) — Deploy Radar inside your cluster with Helm
