# In-Cluster Deployment

Deploy Radar to your Kubernetes cluster for shared team access.

> **Note:** This guide covers deploying Radar as a pod in your cluster. If you're running Radar locally but need to understand cluster connection behavior (e.g., using `KUBECONFIG` to override in-cluster detection), see the [Configuration Guide](configuration.md).

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

## Exposing with Ingress

### Basic (No Authentication)

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

### With Basic Authentication

1. **Create the auth secret:**
   ```bash
   # Install htpasswd if needed: brew install httpd (macOS) or apt install apache2-utils (Linux)

   # Generate credentials (replace 'admin' and 'your-password')
   htpasswd -nb admin 'your-password' > auth

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
- Pod logs (enabled by default)

### Opt-in Permissions

Some features require additional permissions. Most are disabled by default for security:

| Feature | Value | Default | Description |
|---------|-------|---------|-------------|
| Secrets | `rbac.secrets: true` | `false` | Show secrets in resource list |
| Terminal | `rbac.podExec: true` | `false` | Shell access to pods |
| Port Forward | `rbac.portForward: true` | `false` | Port forwarding to pods/services |
| Logs | `rbac.podLogs: true` | `true` | View pod logs |
| Helm Write | `rbac.helm: true` | `false` | Install/upgrade/rollback/uninstall Helm releases (grants broad write access; auto-enables secrets). When auth or cloud is on, also emits a split helm add-on: `radar-helm` (CRDs/storage/PDBs/namespaces, bound to owner+member) and `radar-helm-admin` (RBAC/webhooks/APIServices, owner-only) — see [authentication.md](authentication.md#cloud-mode-helm-bindings) |
| RBAC view | `rbac.viewRBAC: true` | `false` | Show ClusterRoles, ClusterRoleBindings, Roles, RoleBindings in the resource browser. Off by default: cache-served reads bypass per-user RBAC, so granting this exposes the cluster's authorization graph to every authenticated Radar user |
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

### CRD Permissions

Radar reads CRDs from many popular tools. Each CRD group can be toggled individually:

```yaml
rbac:
  crdGroups:
    all: false          # Wildcard — grant read access to ALL API groups
    # Individual groups (all default to true):
    argo: true          # argoproj.io
    certManager: true   # cert-manager.io
    flux: true          # *.toolkit.fluxcd.io
    istio: true         # networking.istio.io, security.istio.io
    karpenter: true     # karpenter.sh, karpenter.k8s.aws, karpenter.azure.com, karpenter.gcp.compute.com
    keda: true          # keda.sh
    knative: true       # serving, eventing, sources, messaging, flows, networking.internal (.knative.dev)
    prometheus: true    # monitoring.coreos.com
    traefik: true       # traefik.io
    velero: true        # velero.io
    # ... and 25+ more (see values.yaml for full list)
  additionalCrdGroups: []   # Add custom API groups
  additionalRules: []       # Arbitrary extra ClusterRole rules
```

### Graceful RBAC Degradation

You see what you have access to — Radar doesn't require cluster-admin. Whatever your ServiceAccount (or the impersonated user, when auth is enabled) can list, Radar shows. Resource types you can't list show an "Access Restricted" message; namespaces you can't access don't appear.

A namespace-scoped ServiceAccount (RoleBinding without a ClusterRole) is fully supported — Radar detects this at startup and works within the permitted namespace.

**RBAC granularity (auth enabled):**

- Namespaced resources (Pods, Deployments, Services, …) are filtered by namespace: read access is granted in any namespace where the user has list-pods or list-deployments. Per-resource gating *within* a namespace is currently coarse — if a user has any namespace-level read access, they can see all namespaced resources Radar's pod ServiceAccount caches in that namespace. Where you need finer control (e.g. denying Secrets in a shared namespace), enforce it via the pod ServiceAccount's RBAC instead.
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
                "ingresses", "persistentvolumeclaims"]
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

1. **Authentication**: Always enable authentication when exposing via ingress. Use [built-in auth](authentication.md) (proxy or OIDC mode) or basic auth (shown above) at minimum.

2. **RBAC scope**: The default ClusterRole grants cluster-wide read access. For namespace-restricted access, set `rbac.create: false` and create a custom Role/RoleBinding. Radar will gracefully adapt to the available permissions.

3. **Privileged features**: Terminal (`podExec`) and port forwarding grant significant access. Only enable these in trusted environments or when using [per-user authentication](authentication.md).

4. **Network access**: Consider using NetworkPolicies to restrict which pods can reach Radar.

## Timeline Storage: memory vs sqlite

Radar's timeline records every cluster change. Two backends:

- **`memory`** (default): events live in-process, lost on pod restart. Lowest footprint; pick this if you only need recent activity (last few hours).
- **`sqlite`**: events persist to a PVC across restarts. Multi-day audit trail; pick this for long-running in-cluster deployments where you care about history surviving pod cycles.

A busy cluster (~5k resources, active controllers) generates ~1.5 MB/min of events. With the default 7-day retention, expect ~15 GB at steady state. Tune `timeline.retention` (Go duration; `0` disables cleanup — not recommended) and `persistence.size` together.

Cleanup runs hourly + once at startup. Confirm it's keeping up via `/api/diagnostics` — the `timeline.lastCleanupAt`, `timeline.lastCleanupDeletedRows`, `timeline.lastCleanupError`, and `timeline.storageBytes` fields surface the state without requiring `kubectl logs`.

## Configuration Reference

See [Helm Chart README](../deploy/helm/radar/README.md) for all available values.

| Parameter | Description | Default |
|-----------|-------------|---------|
| `image.repository` | Container image | `ghcr.io/skyhook-io/radar` |
| `image.tag` | Image tag | Chart appVersion |
| `ingress.enabled` | Enable ingress | `false` |
| `ingress.className` | Ingress class | `""` |
| `service.port` | Service port | `9280` |
| `mcp.enabled` | Enable MCP server for AI tools | `true` |
| `timeline.storage` | Event storage (memory/sqlite) | `memory` |
| `timeline.dbPath` | SQLite database path | `/data/timeline.db` |
| `timeline.historyLimit` | Max events to retain (memory only) | `10000` |
| `timeline.retention` | SQLite retention (Go duration; `0` disables) | `168h` |
| `traffic.prometheusUrl` | Manual Prometheus/VictoriaMetrics URL | `""` (auto-discover) |
| `persistence.enabled` | Enable PVC for SQLite storage | `false` |
| `persistence.size` | PVC size | `1Gi` |
| `rbac.podLogs` | Enable log viewer | `true` |
| `rbac.podExec` | Enable terminal feature | `false` |
| `rbac.portForward` | Enable port forwarding | `false` |
| `rbac.secrets` | Show secrets in resource list | `false` |
| `rbac.helm` | Enable Helm write operations | `false` |
| `rbac.viewRBAC` | Show RBAC objects in resource browser | `false` |
| `rbac.traffic` | Read Hubble TLS certs | `true` |
| `rbac.crdGroups.all` | Wildcard CRD read access | `false` |

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

### Basic auth prompt not appearing

Verify the secret format:
```bash
kubectl get secret radar-basic-auth -n radar -o jsonpath='{.data.auth}' | base64 -d
# Should show: username:$apr1$...
```

## Upgrading

```bash
helm repo update skyhook
helm upgrade radar skyhook/radar -n radar -f values.yaml
```

## Uninstalling

```bash
helm uninstall radar -n radar
kubectl delete namespace radar
```
