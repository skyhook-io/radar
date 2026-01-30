# Radar Helm Chart

Deploy Radar to your Kubernetes cluster for web-based cluster visualization and management.

> **See also:** [In-Cluster Deployment Guide](../../../docs/in-cluster.md) for ingress and DNS setup.

## Prerequisites

- Kubernetes 1.21+
- Helm 3.0+

## Installation

### Quick Start

```bash
helm install radar ./deploy/helm/radar \
  --namespace radar \
  --create-namespace
```

Access via port-forward:
```bash
kubectl port-forward svc/radar 9280:9280 -n radar
open http://localhost:9280
```

### With Ingress

```bash
helm install radar ./deploy/helm/radar \
  --namespace radar \
  --create-namespace \
  --set ingress.enabled=true \
  --set ingress.className=nginx \
  --set ingress.hosts[0].host=radar.example.com \
  --set ingress.hosts[0].paths[0].path=/ \
  --set ingress.hosts[0].paths[0].pathType=Prefix
```

### With TLS

```bash
helm install radar ./deploy/helm/radar \
  --namespace radar \
  --create-namespace \
  --set ingress.enabled=true \
  --set ingress.className=nginx \
  --set ingress.hosts[0].host=radar.example.com \
  --set ingress.hosts[0].paths[0].path=/ \
  --set ingress.hosts[0].paths[0].pathType=Prefix \
  --set ingress.tls[0].secretName=radar-tls \
  --set ingress.tls[0].hosts[0]=radar.example.com
```

## Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `replicaCount` | Number of replicas | `1` |
| `image.repository` | Image repository | `ghcr.io/skyhook-io/radar` |
| `image.tag` | Image tag | Chart appVersion |
| `service.type` | Service type | `ClusterIP` |
| `service.port` | Service port | `9280` |
| `ingress.enabled` | Enable ingress | `false` |
| `ingress.className` | Ingress class name | `""` |
| `timeline.storage` | Timeline storage (memory/sqlite) | `memory` |
| `persistence.enabled` | Enable PVC for SQLite | `false` |
| `resources.limits.memory` | Memory limit | `512Mi` |
| `resources.requests.memory` | Memory request | `128Mi` |

See `values.yaml` for all configuration options.

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

### CRD Access

Radar discovers CRDs in your cluster. All common CRD groups are enabled by default. Granting RBAC for CRDs that don't exist has no effect.

**Wildcard option:** Grant read access to ALL CRDs with one setting:
```bash
--set rbac.crdGroups.all=true
```
This overrides individual settings below. Simpler but broader — some orgs may not allow this.

| Option | API Groups |
|--------|------------|
| `argo` | `argoproj.io` |
| `awx` | `awx.ansible.com` |
| `certManager` | `cert-manager.io` |
| `cloudnativePg` | `cloudnative-pg.io` |
| `crossplane` | `crossplane.io`, `pkg.crossplane.io` |
| `descheduler` | `descheduler.alpha.kubernetes.io` |
| `envoyGateway` | `gateway.envoyproxy.io` |
| `externalDns` | `externaldns.k8s.io` |
| `externalSecrets` | `external-secrets.io` |
| `flux` | `*.toolkit.fluxcd.io` |
| `gatewayApi` | `gateway.networking.k8s.io` |
| `gcpMonitoring` | `monitoring.googleapis.com` |
| `grafana` | `monitoring.grafana.com`, `tempo.grafana.com`, `loki.grafana.com` |
| `istio` | `networking.istio.io`, `security.istio.io` |
| `karpenter` | `karpenter.sh`, `karpenter.k8s.aws` |
| `keda` | `keda.sh` |
| `knative` | `serving.knative.dev`, `eventing.knative.dev` |
| `kubeshark` | `kubeshark.io` |
| `kured` | `kured.io` |
| `kyverno` | `kyverno.io`, `wgpolicyk8s.io`, `reports.kyverno.io` |
| `mariadb` | `mariadb.mmontes.io` |
| `nginx` | `nginx.org` |
| `openshift` | `observability.openshift.io` |
| `opentelemetry` | `opentelemetry.io` |
| `prometheus` | `monitoring.coreos.com` |
| `reflector` | `reflector.v1.k8s.emberstack.com` |
| `reloader` | `reloader.stakater.com` |
| `sealedSecrets` | `sealed-secrets.bitnami.com` |
| `strimzi` | `strimzi.io`, `kafka.strimzi.io` |
| `tekton` | `tekton.dev` |
| `traefik` | `traefik.io`, `traefik.containo.us` |
| `velero` | `velero.io` |

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

## Upgrading

```bash
helm upgrade radar ./deploy/helm/radar -n radar
```

## Uninstalling

```bash
helm uninstall radar -n radar
kubectl delete namespace radar
```
