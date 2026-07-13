# Radar Helm Chart

Deploy Radar to your Kubernetes cluster for web-based cluster visualization and management.

> **Maintainers**: this directory is the canonical source for the chart. The published copy at [`skyhook-io/helm-charts`](https://github.com/skyhook-io/helm-charts) is auto-synced by the `helm` job in [`.github/workflows/release.yml`](../../../.github/workflows/release.yml) on every Radar release — it overwrites `charts/radar/` there with the contents of this directory. Do not edit `helm-charts/charts/radar/` by hand; changes will be clobbered on the next release.

> **See also:** [In-Cluster Deployment Guide](../../../docs/in-cluster.md) for ingress and DNS setup.

## Prerequisites

- Kubernetes 1.21+
- Helm 3.0+

## Installation

### Quick Start

```bash
helm repo add skyhook https://skyhook-io.github.io/helm-charts
helm repo update skyhook
helm upgrade --install radar skyhook/radar -n radar --create-namespace
```

Access via port-forward:
```bash
kubectl port-forward svc/radar 9280:9280 -n radar
open http://localhost:9280
```

### With Ingress

```bash
helm upgrade --install radar skyhook/radar \
  -n radar --create-namespace \
  --set ingress.enabled=true \
  --set ingress.className=nginx \
  --set ingress.hosts[0].host=radar.example.com \
  --set ingress.hosts[0].paths[0].path=/ \
  --set ingress.hosts[0].paths[0].pathType=Prefix
```

### With TLS

```bash
helm upgrade --install radar skyhook/radar \
  -n radar --create-namespace \
  --set ingress.enabled=true \
  --set ingress.className=nginx \
  --set ingress.hosts[0].host=radar.example.com \
  --set ingress.hosts[0].paths[0].path=/ \
  --set ingress.hosts[0].paths[0].pathType=Prefix \
  --set ingress.tls[0].secretName=radar-tls \
  --set ingress.tls[0].hosts[0]=radar.example.com
```

### Connecting to Radar Cloud

To connect Radar to Radar Cloud (hosted SaaS), follow the install wizard at
[radarhq.io](https://radarhq.io) — it generates the full command with your
cluster's bearer token. The wizard's command follows this shape:

```bash
kubectl create namespace radar --dry-run=client -o yaml | kubectl apply -f -
kubectl create secret generic radar-cloud-config -n radar \
  --from-literal=token=$TOKEN \
  --dry-run=client -o yaml | kubectl apply -f -
helm upgrade --install radar skyhook/radar -n radar \
  --set cloud.enabled=true \
  --set cloud.url=wss://api.radarhq.io/agent \
  --set cloud.clusterName=$CLUSTER_NAME \
  --set cloud.existingSecret=radar-cloud-config
```

The `radar-cloud-config` Secret is managed independently of Helm, so token
rotation is one `kubectl apply` — no `helm upgrade` required. The same
applies to GitOps users: manage the Secret with SealedSecrets / SOPS /
External Secrets and reference it via `cloud.existingSecret`; Helm never
touches its contents.

## Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `replicaCount` | Number of replicas | `1` |
| `image.repository` | Image repository | `ghcr.io/skyhook-io/radar` |
| `image.tag` | Image tag | Chart appVersion |
| `service.type` | Service type | `ClusterIP` |
| `service.port` | Service port | `9280` |
| `debug.image` | Image for ephemeral debug containers and node debug pods. In built-in restricted PodSecurity namespaces, pod debug containers may retry as the target/pod non-root UID, or UID `65532` by default; point at a compatible mirror for air-gapped / private-registry clusters. | `""` (busybox:latest) |
| `listPageSize` | Paginate the initial LIST of high-cardinality kinds (Pods, ReplicaSets) on very large clusters; `0` = off, try `2000`. Only used when the apiserver lacks WatchList streaming. | `0` |
| `ingress.enabled` | Enable ingress | `false` |
| `ingress.className` | Ingress class name | `""` |
| `timeline.storage` | Timeline storage (memory/sqlite) | `memory` |
| `timeline.retention` | SQLite retention (Go duration; `0` disables) | `168h` |
| `timeline.maxSize` | SQLite max DB + WAL size before oldest events are pruned (`0` disables) | `800Mi` |
| `persistence.enabled` | Enable PVC for SQLite | `false` |
| `traffic.prometheusUrl` | Manual Prometheus/VictoriaMetrics URL (skips auto-discovery) | `""` |
| `traffic.prometheusHeaders` | HTTP headers sent with every Prometheus request (auth-protected backends) | `{}` |
| `traffic.prometheusHeadersFromEnv` | Prometheus headers sourced from environment variables, for secret-backed auth headers | `{}` |
| `resources.limits.memory` | Memory limit | `512Mi` |
| `resources.requests.memory` | Memory request | `128Mi` |

See `values.yaml` for all configuration options.

### Timeline storage: memory vs sqlite

Radar's timeline records every cluster change so you can scrub backwards through "what happened, when." Two backends:

- **`memory`** (default): events live in-process. Lost on pod restart. Lower memory footprint per retention window than SQLite (no indexes, no WAL). Pick this if you only need recent activity (last few hours), don't care about losing history when a pod cycles, or want the simplest setup.
- **`sqlite`**: events persist to a PVC across restarts. Pick this if you want a multi-day audit trail, need to inspect changes that happened while you weren't looking, or run Radar in-cluster long-term. Adds operational concerns: the PVC will fill if retention is unbounded; restarting on a multi-GB DB is slower (more rows to load).

**Sizing**: timeline volume depends on cluster size and controller churn. Tune `timeline.retention`, `timeline.maxSize`, and `persistence.size` together. Set `timeline.retention=0` to disable age cleanup; keep `timeline.maxSize` enabled for in-cluster deployments so Radar prunes oldest events before the PVC fills.

`/api/diagnostics` surfaces `timeline.retentionAge`, `timeline.maxStorageBytes`, `timeline.lastCleanupAt`, `timeline.lastCleanupDeletedRows`, `timeline.lastCleanupError`, and `timeline.storageBytes` so you can confirm cleanup is keeping up without tailing logs.

## RBAC

The chart creates a ClusterRole with read-only access to common Kubernetes resources.

### Default Permissions (Core K8s Resources)

Always granted (required for basic functionality):

| API Group | Resources |
|-----------|-----------|
| Core (`""`) | pods, services, configmaps, events, namespaces, nodes, pvcs, serviceaccounts, endpoints |
| `apps` | deployments, daemonsets, statefulsets, replicasets |
| `networking.k8s.io` | ingresses, networkpolicies |
| `batch` | jobs, cronjobs |
| `autoscaling` | horizontalpodautoscalers |
| `apiextensions.k8s.io` | customresourcedefinitions (for CRD discovery) |

### Privileged Permissions (Opt-in)

Disabled by default for security:

| Feature | Value | Description |
|---------|-------|-------------|
| Secrets | `rbac.secrets: true` | View secrets in resource list |
| Terminal | `rbac.podExec: true` | Shell access to pods |
| Port Forward | `rbac.portForward: true` | Port forwarding to pods |
| Logs | `rbac.podLogs: true` | View pod logs (**enabled by default**) |
| Helm Write | `rbac.helm: true` | Install/upgrade/rollback/uninstall Helm releases. Under auth or cloud-mode, also emits a split helm add-on ClusterRole — `radar-helm` (member-safe: CRDs, storage, namespaces) and `radar-helm-admin` (owner-only: RBAC, webhooks, ApiServices) |
| RBAC view | `rbac.viewRBAC: true` | Show ClusterRoles, ClusterRoleBindings, Roles, RoleBindings in the resource browser. Off by default — cache-served reads bypass per-user RBAC, so this exposes the cluster's authorization graph to every authenticated Radar user. Auto-enabled under auth or cloud mode (every read is re-checked per user there). |
| Webhooks view | `rbac.viewWebhooks: true` | Show MutatingWebhookConfigurations and ValidatingWebhookConfigurations in the resource browser. Off by default — the configurations reveal which admission controls are enforced (Gatekeeper / Kyverno policies, image scanners, DLP) and where the gaps are, which is recon value for a low-trust viewer. Auto-enabled under auth or cloud mode. |

### In-app Agent Upgrades (opt-in, for Radar Cloud users)

`rbac.selfUpgrade: true` lets Radar Cloud trigger one-click upgrades from the web UI — no terminal or cloud credentials needed. Disabled by default; only needed when connecting to Radar Cloud (the install wizard sets this automatically).

It creates a namespace-scoped Role (not a ClusterRole) with `get` + `patch` on this Deployment only, enforced via `resourceNames`. The endpoint validates that the requested image comes from `ghcr.io/skyhook-io/radar` before issuing any patch.

```bash
--set rbac.selfUpgrade=true
```

### CRD Access

Radar discovers CRDs in your cluster. All common CRD groups are enabled by default. Granting RBAC for CRDs that don't exist has no effect.

**Wildcard option:** Grant read access to ALL CRDs with one setting:
```bash
--set rbac.crdGroups.all=true
```
This overrides individual settings below. Simpler but broader — some orgs may not allow this.

| Option | API Groups |
|--------|------------|
| `apiRegistration` | `apiregistration.k8s.io` |
| `argo` | `argoproj.io` |
| `awx` | `awx.ansible.com` |
| `certManager` | `cert-manager.io`, `acme.cert-manager.io` |
| `cilium` | `cilium.io` |
| `cloudnativePg` | `postgresql.cnpg.io` |
| `clusterApi` | `cluster.x-k8s.io`, `infrastructure.cluster.x-k8s.io`, `controlplane.cluster.x-k8s.io`, `bootstrap.cluster.x-k8s.io`, `addons.cluster.x-k8s.io` |
| `contour` | `projectcontour.io` |
| `crossplane` | `crossplane.io`, `pkg.crossplane.io`, `apiextensions.crossplane.io`, `helm.crossplane.io`, `kubernetes.crossplane.io`. For Upbound provider groups (e.g. `s3.aws.upbound.io`, `compute.gcp.upbound.io`) use `additionalCrdGroups` — K8s RBAC has no apiGroup wildcards. |
| `descheduler` | `descheduler.alpha.kubernetes.io` |
| `envoyGateway` | `gateway.envoyproxy.io` |
| `externalDns` | `externaldns.k8s.io` |
| `externalSecrets` | `external-secrets.io` |
| `flux` | `*.toolkit.fluxcd.io` |
| `gatewayApi` | `gateway.networking.k8s.io` |
| `gcpMonitoring` | `monitoring.googleapis.com` |
| `grafana` | `monitoring.grafana.com`, `tempo.grafana.com`, `loki.grafana.com`, `grafana.integreatly.org` |
| `istio` | `networking.istio.io`, `security.istio.io` |
| `karpenter` | `karpenter.sh`, `karpenter.k8s.aws`, `karpenter.azure.com`, `karpenter.k8s.gcp` |
| `keda` | `keda.sh` |
| `knative` | `serving.knative.dev`, `eventing.knative.dev`, `sources.knative.dev`, `messaging.knative.dev`, `flows.knative.dev`, `networking.internal.knative.dev` |
| `kubeshark` | `kubeshark.io` |
| `kured` | `kured.io` |
| `kyverno` | `kyverno.io`, `wgpolicyk8s.io`, `reports.kyverno.io`, `openreports.io` |
| `mariadb` | `mariadb.mmontes.io` |
| `networkPolicyApi` | `policy.networking.k8s.io` |
| `nginx` | `nginx.org` |
| `openshift` | `observability.openshift.io` |
| `opentelemetry` | `opentelemetry.io` |
| `prometheus` | `monitoring.coreos.com` |
| `reflector` | `reflector.v1.k8s.emberstack.com` |
| `reloader` | `reloader.stakater.com` |
| `sealedSecrets` | `bitnami.com` |
| `strimzi` | `strimzi.io`, `kafka.strimzi.io` |
| `tekton` | `tekton.dev` |
| `traefik` | `traefik.io`, `traefik.containo.us` |
| `trivy` | `aquasecurity.github.io` |
| `velero` | `velero.io` |
| `verticalPodAutoscaler` | `autoscaling.k8s.io` |

**Disable groups:** `--set rbac.crdGroups.istio=false`

**Add unlisted CRDs:**
```yaml
rbac:
  additionalCrdGroups:
    - mycompany.io
```

### Troubleshooting: "Failed to list resource" Warnings

If you see these warnings, Radar discovered a CRD but doesn't have RBAC access. This is **not an error** — add the API group to `additionalCrdGroups` if you need it.

### Advanced: Custom Rules

For fine-grained control, use `additionalRules` to add arbitrary RBAC rules:
```yaml
rbac:
  additionalRules:
    - apiGroups: ["custom.example.com"]
      resources: ["myresources"]
      verbs: ["get", "list", "watch"]
    - apiGroups: [""]
      resources: ["pods"]
      verbs: ["delete"]  # Dangerous - use with caution
```

### Capability Detection

Radar uses its ServiceAccount permissions to access the Kubernetes API. The UI automatically detects which features are available based on RBAC and hides unavailable features (e.g., the terminal button won't appear if `podExec` is disabled).

## Uninstalling

```bash
helm uninstall radar -n radar
kubectl delete namespace radar
```
