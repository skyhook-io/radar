# In-Cluster Deployment

Deploy Radar to your Kubernetes cluster for shared team access.

## Quick Start

```bash
helm repo add skyhook https://skyhook-io.github.io/helm-charts
helm install radar skyhook/radar -n radar --create-namespace
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

Some features require additional permissions that are **disabled by default** for security:

| Feature | Value | Description |
|---------|-------|-------------|
| Secrets | `rbac.secrets: true` | Show secrets in resource list |
| Terminal | `rbac.podExec: true` | Shell access to pods |
| Port Forward | `rbac.portForward: true` | Port forwarding to pods/services |
| Logs | `rbac.podLogs: true` | View pod logs (enabled by default) |

Enable features as needed:

```yaml
# values.yaml
rbac:
  secrets: false      # Keep disabled unless needed
  podExec: true       # Enable terminal feature
  podLogs: true       # Enable log viewer (default)
  portForward: true   # Enable port forwarding
```

## Security Considerations

When deploying Radar in-cluster:

1. **Authentication**: Always enable authentication when exposing via ingress. Use basic auth (shown above) or an auth proxy like oauth2-proxy.

2. **RBAC scope**: The default ClusterRole grants cluster-wide read access. For namespace-restricted access, set `rbac.create: false` and create a custom Role/RoleBinding.

3. **Privileged features**: Terminal (`podExec`) and port forwarding grant significant access. Only enable these in trusted environments or when using per-user authentication.

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
| `timeline.storage` | Event storage (memory/sqlite) | `memory` |
| `rbac.podLogs` | Enable log viewer | `true` |
| `rbac.podExec` | Enable terminal feature | `false` |
| `rbac.portForward` | Enable port forwarding | `false` |
| `rbac.secrets` | Show secrets in resource list | `false` |

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
