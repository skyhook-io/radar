# In-Cluster Deployment

Deploy Skyhook Explorer to your Kubernetes cluster for shared team access.

## Quick Start

```bash
# Add the Helm repository (coming soon)
# helm repo add skyhook https://skyhook-io.github.io/helm-charts

# For now, clone and install from source
git clone https://github.com/skyhook-io/explorer.git
cd explorer

helm install explorer ./deploy/helm/skyhook-explorer \
  --namespace skyhook-explorer \
  --create-namespace
```

Access via port-forward:
```bash
kubectl port-forward svc/explorer-skyhook-explorer 9280:9280 -n skyhook-explorer
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
    - host: explorer.your-domain.com
      paths:
        - path: /
          pathType: Prefix
```

```bash
helm upgrade --install explorer ./deploy/helm/skyhook-explorer \
  -n skyhook-explorer -f values.yaml
```

### With Basic Authentication

1. **Create the auth secret:**
   ```bash
   # Install htpasswd if needed: brew install httpd (macOS) or apt install apache2-utils (Linux)

   # Generate credentials (replace 'admin' and 'your-password')
   htpasswd -nb admin 'your-password' > auth

   # Create the secret
   kubectl create secret generic explorer-basic-auth \
     --from-file=auth \
     -n skyhook-explorer

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
       nginx.ingress.kubernetes.io/auth-secret: explorer-basic-auth
       nginx.ingress.kubernetes.io/auth-realm: "Skyhook Explorer"
     hosts:
       - host: explorer.your-domain.com
         paths:
           - path: /
             pathType: Prefix
   ```

3. **Deploy:**
   ```bash
   helm upgrade --install explorer ./deploy/helm/skyhook-explorer \
     -n skyhook-explorer -f values.yaml
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
    - host: explorer.your-domain.com
      paths:
        - path: /
          pathType: Prefix
  tls:
    - secretName: explorer-tls
      hosts:
        - explorer.your-domain.com
```

## DNS Setup

1. **Get your ingress IP:**
   ```bash
   kubectl get ingress -n skyhook-explorer
   ```

2. **Create a DNS A record** pointing your domain to the ingress IP.

**Multi-cluster naming convention:**
```
explorer.<cluster-name>.<domain>
```
Example: `explorer.prod-us-east1.example.com`

## RBAC

Explorer uses its ServiceAccount to access the Kubernetes API. The Helm chart creates a ClusterRole with read-only access to common resources:

- Pods, Services, ConfigMaps, Secrets, Events, Namespaces, Nodes
- Deployments, DaemonSets, StatefulSets, ReplicaSets
- Ingresses, Jobs, CronJobs, HPAs, PVCs
- Pod logs and exec (for terminal access)

To customize permissions, override the RBAC in your values:

```yaml
# values.yaml
rbac:
  create: true
  # Add custom rules if needed
```

## Configuration Reference

See [Helm Chart README](../deploy/helm/skyhook-explorer/README.md) for all available values.

| Parameter | Description | Default |
|-----------|-------------|---------|
| `image.repository` | Container image | `ghcr.io/skyhook-io/explorer` |
| `image.tag` | Image tag | Chart appVersion |
| `ingress.enabled` | Enable ingress | `false` |
| `ingress.className` | Ingress class | `""` |
| `service.port` | Service port | `9280` |
| `timeline.storage` | Event storage (memory/sqlite) | `memory` |

## Troubleshooting

### Pod not starting

```bash
kubectl logs -n skyhook-explorer -l app.kubernetes.io/name=skyhook-explorer
kubectl describe pod -n skyhook-explorer -l app.kubernetes.io/name=skyhook-explorer
```

### Ingress not working

```bash
kubectl get ingress -n skyhook-explorer -o yaml
kubectl logs -n ingress-nginx -l app.kubernetes.io/name=ingress-nginx
```

### Basic auth prompt not appearing

Verify the secret format:
```bash
kubectl get secret explorer-basic-auth -n skyhook-explorer -o jsonpath='{.data.auth}' | base64 -d
# Should show: username:$apr1$...
```

## Upgrading

```bash
helm upgrade explorer ./deploy/helm/skyhook-explorer -n skyhook-explorer -f values.yaml
```

## Uninstalling

```bash
helm uninstall explorer -n skyhook-explorer
kubectl delete namespace skyhook-explorer
```
