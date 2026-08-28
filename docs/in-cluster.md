# In-Cluster Deployment

Deploy Radar to your Kubernetes cluster for shared team access.

> **Note:** This guide covers deploying Radar as a pod in your cluster. If you're running Radar locally but need to understand cluster connection behavior (e.g., using `KUBECONFIG` to override in-cluster detection), see the [Configuration Guide](configuration.md).

Choose the path that matches what you are trying to do:

- **New installation:** follow [Quick Start](#quick-start), then configure how
  your team will reach and authenticate to Radar.
- **Existing installation:** start with [Upgrading](#upgrading). The correct
  workflow depends on whether Helm, Argo CD, Flux, or another system owns the
  deployment.

## Quick Start

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

## Upgrading

Upgrade Radar through the same source that manages the current installation.
If Argo CD or Flux owns Radar, change the version in Git and let the controller
reconcile it. Running `helm upgrade` directly against a GitOps-managed release
creates drift and the controller may revert it.

Before upgrading, review the [release notes](https://github.com/skyhook-io/radar/releases)
between your current and target versions, especially for a major-version update.
The Radar version shown on the Home page matches the published Helm chart
version.

### Helm

If Radar links you to a Helm release, use that release name and namespace. You
can also find likely releases with:

```bash
helm list --all-namespaces --filter radar
```

Update the chart repository, then upgrade while preserving the values already
set on the release:

```bash
helm repo update skyhook
helm upgrade radar skyhook/radar \
  --namespace radar \
  --reuse-values \
  --wait
```

Replace `radar` with the release name and namespace used by your installation.
If the deployment is managed from a values file, use that same file with
`-f values.yaml` instead of `--reuse-values` so Git remains the reproducible
source of configuration. The command installs the latest published chart; add
`--version X.Y.Z` to pin the exact version shown in Radar (without the leading
`v`).

### Argo CD or Flux

Do not upgrade the live Helm release or Deployment directly. Update the
version in the repository that owns the installation, then reconcile its
controller:

- **Argo CD Application:** if the Application directly references the Radar
  chart, update its Helm chart `targetRevision`. Otherwise update the chart or
  image version in the Git source referenced by the Application, then sync it.
- **Flux HelmRelease:** update the Radar chart version in the HelmRelease source,
  usually `spec.chart.spec.version`, then reconcile the HelmRelease.
- **Flux Kustomization:** update the Radar chart or image version in the Git
  source referenced by the Kustomization, then reconcile it.

Radar links directly to a verified owning Application, HelmRelease, or
Kustomization when it can. Open that object to confirm its source and health
before changing Git.

### If the manager could not be identified

Inspect the Radar Deployment before choosing an upgrade method:

```bash
kubectl get deployment --all-namespaces \
  --selector app.kubernetes.io/name=radar \
  --output yaml
```

Look at the Deployment's labels and annotations:

- `meta.helm.sh/release-name` and `meta.helm.sh/release-namespace` identify the
  underlying Helm release.
- `argocd.argoproj.io/tracking-id` or `argocd.argoproj.io/instance` points to
  Argo CD ownership.
- `helm.toolkit.fluxcd.io/*` and `kustomize.toolkit.fluxcd.io/*` point to Flux
  ownership.

A GitOps-managed chart normally has both Helm and GitOps metadata. In that
case, GitOps is the source of truth: update Git rather than running Helm
directly. If none of these markers exist, update the manifests or image tag in
the system that originally deployed Radar.

### Verify or roll back

Wait for the Radar Deployment to finish rolling out, then refresh Radar and
confirm the new version on the Home page:

```bash
kubectl rollout status deployment/radar --namespace radar --timeout 5m
```

Use the actual Deployment name and namespace if they differ. For a Helm-managed
installation, inspect `helm history` and use `helm rollback` if the rollout
fails. For a GitOps-managed installation, revert the version change in Git and
reconcile the owning object.

## Exposing with Gateway API

If your cluster uses Gateway API, the chart can create an `HTTPRoute`:

```yaml
# values.yaml
httpRoute:
  enabled: true
  parentRefs:
    - name: public-gateway
  hostnames:
    - radar.your-domain.com
```

With no custom `rules`, the chart routes `/` to Radar. `httpRoute` and `ingress` are mutually exclusive, and at least one `parentRefs` entry is required. See the [chart reference](../deploy/helm/radar/README.md#with-gateway-api-httproute) for custom rules and timeouts.

## Exposing with Ingress

### Ingress without authentication

> **Warning:** Only use this behind a trusted private network boundary. Anyone
> who can reach Radar can use the permissions granted to its ServiceAccount.
> For shared or externally reachable installations, configure authentication
> before exposing the ingress.

```yaml
# values.yaml
ingress:
  enabled: true
  className: nginx
  hosts:
    - host: radar.your-domain.com
      paths:
        - path: /
          pathType: Prefix
```

```bash
helm upgrade --install radar skyhook/radar \
  -n radar -f values.yaml
```

### Subpath Ingress (No Strip-Prefix)

If your ingress forwards `/radar/...` to the Radar service as `/radar/...`, set `basePath` to the same prefix:

```yaml
# values.yaml
basePath: /radar

ingress:
  enabled: true
  className: nginx
  hosts:
    - host: tools.your-domain.com
      paths:
        - path: /radar
          pathType: Prefix
```

Then open `https://tools.your-domain.com/radar/`. Do not set `basePath` when your ingress already rewrites `/radar` to `/`.

Path segments accept letters, digits, `-`, `_`, `.` and `~`. Radar serves the app
**only** under the prefix — requests to unprefixed paths get a 404, so update any
external health checks or scrapers that hit `/api/health` or `/metrics` directly
(the chart's own probes follow `basePath` automatically). `basePath` is not
supported together with Radar Cloud (`--cloud-url`), which owns the URL path
itself.

**Every URL you hand to an external system must include the prefix.** With OIDC
that means the values you register with your identity provider:

```yaml
basePath: /radar
auth:
  oidc:
    redirectURL: https://tools.your-domain.com/radar/auth/callback           # not /auth/callback
    postLogoutRedirectURL: https://tools.your-domain.com/radar/              # not /
    # With backchannelLogout, the URI registered at the IdP is likewise
    # https://tools.your-domain.com/radar/auth/backchannel-logout
```

Radar's callback route lives at `{basePath}/auth/callback`, so a redirect URL
without the prefix sends the IdP to a path the ingress doesn't route to Radar and
login ends in a 404.

**Give each instance its own hostname.** Two Radars behind subpaths on one
hostname (`/radar-a`, `/radar-b`) are the same browser origin, so they share
browser state: the session cookie is set at `Path=/`, and `localStorage` — which
holds the theme, log-viewer preferences and similar per-instance settings — is
scoped per origin with no path-scoped equivalent in the platform at all. Logging
into one can end the other's session, and preferences set in one show up in the
other. A hostname each keeps them properly separate.

Separately from that: a Radar per cluster gives you a view per cluster. Each
watches only its own cluster and carries its own upgrades, ingress and auth
config, with no cross-cluster search or combined issue list across them. If you
want several clusters in one view, that is what
[Radar Cloud](https://radarhq.io) is for, and its agent dials out so there is no
per-cluster ingress to wire up.

### With ingress basic authentication

1. **Create the auth secret:**
   ```bash
   # Install htpasswd if needed: brew install httpd (macOS) or apt install apache2-utils (Linux)

   # Create the file and enter the password when prompted
   htpasswd -cB auth admin

   # Create the secret
   kubectl create secret generic radar-basic-auth \
     --from-file=auth \
     -n radar

   rm auth  # Clean up local file
   ```

2. **Configure ingress:**
   ```yaml
   # values.yaml
   ingress:
     enabled: true
     className: nginx
     annotations:
       nginx.ingress.kubernetes.io/auth-type: basic
       nginx.ingress.kubernetes.io/auth-secret: radar-basic-auth
       nginx.ingress.kubernetes.io/auth-realm: "Radar"
     hosts:
       - host: radar.your-domain.com
         paths:
           - path: /
             pathType: Prefix
   ```

3. **Deploy:**
   ```bash
   helm upgrade --install radar skyhook/radar \
     -n radar -f values.yaml
   ```

### With TLS (HTTPS)

Requires [cert-manager](https://cert-manager.io/) installed in your cluster.

```yaml
# values.yaml
ingress:
  enabled: true
  className: nginx
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt-prod
  hosts:
    - host: radar.your-domain.com
      paths:
        - path: /
          pathType: Prefix
  tls:
    - secretName: radar-tls
      hosts:
        - radar.your-domain.com
```

## DNS Setup

1. **Get your ingress IP:**
   ```bash
   kubectl get ingress -n radar
   ```

2. **Create a DNS A record** pointing your domain to the ingress IP.

**Multi-cluster naming convention:**
```
radar.<cluster-name>.<domain>
```
Example: `radar.prod-us-east1.example.com`

## RBAC

Radar uses its ServiceAccount to access the Kubernetes API. The Helm chart creates a ClusterRole with **read-only access** to common resources by default:

- Pods, Services, ConfigMaps, Events, Namespaces, Nodes, ServiceAccounts, Endpoints
- Deployments, DaemonSets, StatefulSets, ReplicaSets
- Ingresses, NetworkPolicies, Jobs, CronJobs, HPAs, PVCs
- Kubernetes 1.37 Workloads, PodGroups, CompositePodGroups, PodCertificateRequests, and ClusterTrustBundles when the API server advertises them; the scheduling APIs are feature-gated
- Pod logs (enabled by default)

### Opt-in Permissions

Some features require additional permissions. Most are disabled by default for security:

| Feature | Value | Default | Description |
|---------|-------|---------|-------------|
| Secrets | `rbac.secrets: true` | `false` | Show secrets in resource list |
| Terminal | `rbac.podExec: true` | `false` | Shell access to pods |
| Port Forward | `rbac.portForward: true` | `false` | Port forwarding to pods/services. Also the traffic-source fallback (Hubble/Caretta): Radar dials the relay/metrics Service directly first, so this is only needed when a NetworkPolicy or routing blocks that direct path |
| Logs | `rbac.podLogs: true` | `true` | View pod logs |
| Helm Write | `rbac.helm: true` | `false` | Install/upgrade/rollback/uninstall Helm releases (grants broad write access; auto-enables secrets). When auth or cloud is on, also emits a split helm add-on: `radar-helm` (CRDs/storage/PDBs/namespaces, bound to owner+member) and `radar-helm-admin` (RBAC/webhooks/APIServices, owner-only) — see [authentication.md](authentication.md#cloud-mode-helm-bindings) |
| RBAC view | `rbac.viewRBAC: true` | `false` | Show ClusterRoles, ClusterRoleBindings, Roles, RoleBindings in the resource browser. Off by default: cache-served reads bypass per-user RBAC, so granting this exposes the cluster's authorization graph to every authenticated Radar user |
| Webhooks view | `rbac.viewWebhooks: true` | `false` | Show admission webhook configurations. Off by default because they expose the cluster's admission-control posture; auto-enabled when auth or cloud mode enforces each user's own RBAC |
| Node runtime evidence | `rbac.viewNodeRuntime: true` | `false` | Let upgrade-impact checks inspect kubelet metrics and effective configuration through `nodes/proxy`. Enable only for a trusted no-auth audience; authenticated users need this permission on their own Kubernetes identity |
| Traffic TLS | `rbac.traffic: true` | `true` | Read Hubble relay TLS certs for Cilium traffic observation |

> **Node management** (cordon, uncordon, drain) is available via the MCP server and API. These operations require `patch` on nodes, `list` on pods, and `create` on `pods/eviction`, which are not included in the default ClusterRole. Add them via `rbac.additionalRules` or use [per-user authentication](authentication.md) so each user's own RBAC governs node operations.

Enable features as needed:

```yaml
# values.yaml
rbac:
  secrets: false      # Keep disabled unless needed
  podExec: true       # Enable terminal feature
  podLogs: true       # Enable log viewer (default)
  portForward: true   # Enable port forwarding
  helm: false         # Enable Helm write operations (broad permissions)
```

The terminal's **Debug** action launches a throwaway container (ephemeral container on a pod, or a privileged pod on a node) using `busybox:latest` by default. If the built-in restricted Pod Security Standard rejects the default pod debug container, Radar retries with a restricted-compatible Linux security context using the target/pod non-root UID, or UID `65532` by default, so custom images used in restricted namespaces must work as a non-root user. In air-gapped or private-registry clusters where the default image can't be pulled, point it at a reachable mirror:

```yaml
# values.yaml
debug:
  image: my-registry.internal/busybox:1.36
```

Radar doesn't attach image-pull secrets to debug containers or pods — ephemeral containers inherit the target pod's, and node debug pods rely on the `default` namespace's ServiceAccount / node registry config — so the image must be pullable without Radar supplying credentials.

### CRD Permissions

Radar reads CRDs from many popular tools. Each CRD group can be toggled individually:

```yaml
rbac:
  crdGroups:
    all: false          # Wildcard — grant read access to ALL API groups
    # Individual groups (all default to true):
    argo: true          # argoproj.io
    calico: true        # projectcalico.org, crd.projectcalico.org
    certManager: true   # cert-manager.io
    flux: true          # *.toolkit.fluxcd.io
    istio: true         # networking.istio.io, security.istio.io
    karpenter: true     # karpenter.sh, karpenter.k8s.aws, karpenter.azure.com, karpenter.k8s.gcp
    keda: true          # keda.sh
    knative: true       # serving, eventing, sources, messaging, flows, networking.internal (.knative.dev)
    prometheus: true    # monitoring.coreos.com
    traefik: true       # traefik.io
    velero: true        # velero.io
    # ... and 25+ more (see values.yaml for full list)
  additionalCrdGroups: []   # Add custom API groups
  additionalRules: []       # Arbitrary extra ClusterRole rules
```

Calico access is read-only (`get`, `list`, and `watch`) for both API groups and is enabled by default. Set `rbac.crdGroups.calico=false` to disable it. If chart-managed Calico access is disabled, grant only the resources you need through `rbac.additionalRules`; both groups may be required while a cluster exposes modern and legacy Calico APIs.

### Graceful RBAC Degradation

You see what you have access to — Radar doesn't require cluster-admin. Whatever your ServiceAccount (or the impersonated user, when auth is enabled) can list, Radar shows. Resource types you can't list show an actionable denied-state instead of a misleading "0 / None found": for a core cluster-scoped kind (Node, PV, StorageClass, and the like) your identity can't read, Radar shows a copyable `ClusterRole` + `ClusterRoleBinding` request to hand to a cluster admin (reason `rbac_denied`), and distinguishes that from a kind Radar's own ServiceAccount can't read, where a user-level grant wouldn't help (reason `unavailable`, no snippet). The "Your access on this cluster" dialog lists the core cluster-scoped kinds being hidden alongside your effective rules. Namespaces you can't access don't appear.

A namespace-scoped ServiceAccount (RoleBinding without a ClusterRole) is fully supported — Radar detects this at startup and works within the permitted namespace.

**RBAC granularity (auth enabled):**

- Namespaced resources (Pods, Deployments, Services, …) are filtered by namespace: read access is granted in any namespace where the user has list-pods or list-deployments. **Secrets are gated independently** — a user sees Secret objects (including Helm release storage) only in namespaces where their own RBAC grants `list secrets`, even when they have broader access in that namespace. RBAC objects and webhook configurations are likewise re-checked per user. Other cached resource types are visible within a namespace once the user has workload-read access there.
- Cluster-scoped resources (Nodes, PVs, StorageClasses, ClusterRoles, cluster-scoped CRDs, …) are gated per-kind via SubjectAccessReview. Cluster-wide pod visibility does NOT imply Node visibility — every cluster-scoped read goes through its own RBAC check, with results cached per user.

The same RBAC boundary applies to MCP — read tools intersect with each user's allowed namespaces, write tools impersonate the user against the apiserver, and cluster-scoped reads run the same per-kind SAR. The pod ServiceAccount's permissions are the upper bound for both REST and MCP; per-user RBAC narrows that to what each user can see.

**Example: Namespace-scoped deployment**

```yaml
# Custom Role granting access to a single namespace
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: radar-viewer
  namespace: my-team
rules:
  - apiGroups: ["", "apps", "batch", "networking.k8s.io"]
    resources: ["pods", "services", "deployments", "daemonsets", "statefulsets",
                "replicasets", "jobs", "cronjobs", "configmaps", "events",
                "ingresses", "persistentvolumeclaims", "resourcequotas"]
    verbs: ["get", "list", "watch"]
  - apiGroups: [""]
    resources: ["pods/log"]
    verbs: ["get"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: radar-viewer
  namespace: my-team
subjects:
  - kind: ServiceAccount
    name: radar
    namespace: radar
roleRef:
  kind: Role
  name: radar-viewer
  apiGroup: rbac.authorization.k8s.io
```

Set `rbac.create: false` in the Helm values and apply the custom Role/RoleBinding above. Radar will detect the namespace-scoped permissions and work within `my-team` only.

## Authentication

For shared team access, enable authentication so each user gets per-user permissions via Kubernetes RBAC. See the **[Authentication & Authorization Guide](authentication.md)** for full setup instructions.

**Quick start with proxy auth:**
```yaml
# values.yaml
auth:
  mode: proxy
```

Then deploy an auth proxy (e.g., oauth2-proxy) in front of Radar. Users authenticate through the proxy, and Radar uses K8s impersonation so each user's actions are governed by their own K8s RBAC bindings.

**Quick start with OIDC:**
```yaml
# values.yaml
auth:
  mode: oidc
  oidc:
    issuerURL: https://accounts.google.com
    clientID: your-client-id
    clientSecret: your-client-secret
    redirectURL: https://radar.example.com/auth/callback
```

## Security Considerations

When deploying Radar in-cluster:

The Helm chart explicitly sets `--listen-address=0.0.0.0` so its ClusterIP
Service can reach the pod. Unlike the native CLI's loopback-only default, this
makes Radar reachable on the pod network; the following controls are therefore
part of the deployment boundary.

1. **Authentication**: Always enable authentication when exposing via ingress. Use [built-in auth](authentication.md) (proxy or OIDC mode) or basic auth (shown above) at minimum.

2. **RBAC scope**: The default ClusterRole grants cluster-wide read access. For namespace-restricted access, set `rbac.create: false` and create a custom Role/RoleBinding. Radar will gracefully adapt to the available permissions.

3. **Privileged features**: Terminal (`podExec`) and port forwarding grant significant access. Only enable these in trusted environments or when using [per-user authentication](authentication.md).

4. **Network access**: Consider using NetworkPolicies to restrict which pods can reach Radar.

## Timeline Storage: memory vs sqlite

Radar's timeline records every cluster change. Two backends:

- **`memory`** (default): events live in-process, lost on pod restart. Lowest footprint; pick this if you only need recent activity (last few hours).
- **`sqlite`**: events persist to a PVC across restarts. Multi-day audit trail; pick this for long-running in-cluster deployments where you care about history surviving pod cycles.

Timeline volume depends on cluster size and controller churn. Tune `timeline.retention` (Go duration; `0` disables age cleanup), `timeline.maxSize`, and `persistence.size` together. Keep `timeline.maxSize` below the PVC size so Radar prunes oldest events before the volume fills.

Cleanup runs hourly + once at startup. Confirm it's keeping up via `/api/diagnostics` — the `timeline.maxStorageBytes`, `timeline.lastCleanupAt`, `timeline.lastCleanupDeletedRows`, `timeline.lastCleanupError`, and `timeline.storageBytes` fields surface the state without requiring `kubectl logs`.

## Configuration Reference

See [Helm Chart README](../deploy/helm/radar/README.md) for all available values.

| Parameter | Description | Default |
|-----------|-------------|---------|
| `image.repository` | Container image | `ghcr.io/skyhook-io/radar` |
| `image.tag` | Image tag | Chart appVersion |
| `ingress.enabled` | Enable ingress | `false` |
| `ingress.className` | Ingress class | `""` |
| `service.port` | Service port | `9280` |
| `basePath` | URL prefix Radar serves under, e.g. `/radar` for no-strip-prefix subpath ingress | `""` |
| `mcp.enabled` | Enable MCP server for AI tools | `true` |
| `debug.image` | Image for ephemeral debug containers and node debug pods. In built-in restricted PodSecurity namespaces, pod debug containers may retry as the target/pod non-root UID, or UID `65532` by default; point at a compatible mirror for air-gapped / private-registry clusters. | `""` (busybox:latest) |
| `listPageSize` | Paginate the initial LIST of high-cardinality kinds (Pods, ReplicaSets) on very large clusters that fail to sync; `0` = off, try `2000`. Only used when the apiserver lacks WatchList streaming. | `0` |
| `timeline.storage` | Event storage (memory/sqlite) | `memory` |
| `timeline.dbPath` | SQLite database path | `/data/timeline.db` |
| `timeline.historyLimit` | Max events to retain (memory only) | `10000` |
| `timeline.retention` | SQLite retention (Go duration; `0` disables) | `168h` |
| `timeline.maxSize` | SQLite max DB + WAL size before oldest events are pruned (`0` disables) | `800Mi` |
| `cost.source` | Cost source: `auto`, `prometheus`, or `kubecost` | `""` (editable Auto) |
| `cost.kubecost.url` | Kubecost 3 Aggregator URL; required for federated agent-only clusters | `""` (discover local) |
| `cost.kubecost.clusterId` | Cluster ID filter for a central Aggregator | `""` (detect literal `CLUSTER_ID`) |
| `cost.kubecost.existingSecret` | Secret containing an optional Kubecost API key | `""` |
| `traffic.prometheusUrl` | Manual PromQL-compatible query URL (Prometheus, VictoriaMetrics, Thanos, Mimir) | `""` (auto-discover) |
| `traffic.prometheusHeadersFromEnv` | Prometheus headers sourced from environment variables, for secret-backed auth headers | `{}` |
| `persistence.enabled` | Enable PVC for SQLite storage | `false` |
| `persistence.size` | PVC size | `1Gi` |
| `rbac.podLogs` | Enable log viewer | `true` |
| `rbac.podExec` | Enable terminal feature | `false` |
| `rbac.portForward` | Enable port forwarding | `false` |
| `rbac.secrets` | Show secrets in resource list | `false` |
| `rbac.helm` | Enable Helm write operations | `false` |
| `rbac.viewRBAC` | Show RBAC objects in resource browser | `false` |
| `rbac.viewWebhooks` | Show admission webhook configurations | `false` |
| `rbac.viewNodeRuntime` | Inspect kubelet metrics and effective configuration for upgrade impact | `false` |
| `rbac.traffic` | Read Hubble TLS certs | `true` |
| `rbac.crdGroups.all` | Wildcard CRD read access | `false` |

**Response compression:** Radar gzip-compresses HTTP responses by default (streaming endpoints like SSE are excluded). The level defaults to `1` (best speed), since on large clusters peak response size coincides with peak CPU. Set the `RADAR_COMPRESS_LEVEL` environment variable (via the chart's pod `env`) to `0` to disable, or `2`-`9` to trade CPU for smaller bodies on bandwidth-bound deployments.

## Troubleshooting

### Pod not starting

```bash
kubectl logs -n radar -l app.kubernetes.io/name=radar
kubectl describe pod -n radar -l app.kubernetes.io/name=radar
```

### Ingress not working

```bash
kubectl get ingress -n radar -o yaml
kubectl logs -n ingress-nginx -l app.kubernetes.io/name=ingress-nginx
```

If the UI loads through an ingress but pod exec does not connect, ensure the ingress forwards WebSocket upgrades and preserves the browser-facing `Host` header. Preserving `Host` is the compatibility requirement across browsers and proxies; Fetch Metadata is an additional signal only when both sides forward it. Radar logs rejected handshakes with both `Origin` and `Host`.

### Basic auth prompt not appearing

Verify the secret format:
```bash
kubectl get secret radar-basic-auth -n radar -o jsonpath='{.data.auth}' | base64 -d
# Should show: username:<password-hash>
```

## Uninstalling

```bash
helm uninstall radar -n radar
```

If `radar` is a dedicated namespace and contains nothing you need to retain,
delete it separately with `kubectl delete namespace radar`.
