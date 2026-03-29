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
| Helm Write | `rbac.helm: true` | `false` | Install/upgrade/rollback/uninstall Helm releases (grants broad write access; auto-enables secrets) |
| Traffic TLS | `rbac.traffic: true` | `true` | Read Hubble relay TLS certs for Cilium traffic observation |

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
    karpenter: true     # karpenter.sh, karpenter.k8s.aws, karpenter.azure.com
    keda: true          # keda.sh
    knative: true       # *.knative.dev
    prometheus: true    # monitoring.coreos.com
    traefik: true       # traefik.io
    velero: true        # velero.io
    # ... and 25+ more (see values.yaml for full list)
  additionalCrdGroups: []   # Add custom API groups
  additionalRules: []       # Arbitrary extra ClusterRole rules
```

### Graceful RBAC Degradation

Radar works with whatever permissions are available — it does not require full cluster-admin access. At startup, Radar checks which resource types are accessible using `SelfSubjectAccessReview` and only starts informers for permitted resources.

**What this means in practice:**

- If your ServiceAccount can only list Pods and Services, Radar shows those — other resource types display an "Access Restricted" message
- Cluster-scoped resources (Nodes, Namespaces) require a ClusterRole; if unavailable, those sections are gracefully hidden
- For namespace-scoped ServiceAccounts (RoleBinding instead of ClusterRoleBinding), Radar automatically detects this and scopes its informers to the permitted namespace
- The UI clearly indicates which resources are restricted vs simply empty

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
| `timeline.historyLimit` | Max events to retain | `10000` |
| `traffic.prometheusUrl` | Manual Prometheus/VictoriaMetrics URL | `""` (auto-discover) |
| `persistence.enabled` | Enable PVC for SQLite storage | `false` |
| `persistence.size` | PVC size | `1Gi` |
| `rbac.podLogs` | Enable log viewer | `true` |
| `rbac.podExec` | Enable terminal feature | `false` |
| `rbac.portForward` | Enable port forwarding | `false` |
| `rbac.secrets` | Show secrets in resource list | `false` |
| `rbac.helm` | Enable Helm write operations | `false` |
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
