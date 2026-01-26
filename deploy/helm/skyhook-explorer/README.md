# Skyhook Explorer Helm Chart

Deploy Skyhook Explorer to your Kubernetes cluster for web-based cluster visualization and management.

> **See also:** [In-Cluster Deployment Guide](../../../docs/in-cluster.md) for ingress and DNS setup.

## Prerequisites

- Kubernetes 1.21+
- Helm 3.0+

## Installation

### Quick Start

```bash
helm install explorer ./deploy/helm/skyhook-explorer \
  --namespace skyhook-explorer \
  --create-namespace
```

Access via port-forward:
```bash
kubectl port-forward svc/explorer-skyhook-explorer 9280:9280 -n skyhook-explorer
open http://localhost:9280
```

### With Ingress

```bash
helm install explorer ./deploy/helm/skyhook-explorer \
  --namespace skyhook-explorer \
  --create-namespace \
  --set ingress.enabled=true \
  --set ingress.className=nginx \
  --set ingress.hosts[0].host=explorer.example.com \
  --set ingress.hosts[0].paths[0].path=/ \
  --set ingress.hosts[0].paths[0].pathType=Prefix
```

### With TLS

```bash
helm install explorer ./deploy/helm/skyhook-explorer \
  --namespace skyhook-explorer \
  --create-namespace \
  --set ingress.enabled=true \
  --set ingress.className=nginx \
  --set ingress.hosts[0].host=explorer.example.com \
  --set ingress.hosts[0].paths[0].path=/ \
  --set ingress.hosts[0].paths[0].pathType=Prefix \
  --set ingress.tls[0].secretName=explorer-tls \
  --set ingress.tls[0].hosts[0]=explorer.example.com
```

## Configuration

| Parameter | Description | Default |
|-----------|-------------|---------|
| `replicaCount` | Number of replicas | `1` |
| `image.repository` | Image repository | `ghcr.io/skyhook-io/explorer` |
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

The chart creates a ClusterRole with read-only access to common Kubernetes resources:

- Core: pods, services, configmaps, secrets, events, namespaces, nodes, pvcs
- Apps: deployments, daemonsets, statefulsets, replicasets
- Networking: ingresses, networkpolicies
- Batch: jobs, cronjobs
- Autoscaling: horizontalpodautoscalers
- Pod operations: exec, logs, port-forward (create)
- CRDs: Argo, Knative, cert-manager, Gateway API

Explorer uses its ServiceAccount permissions to access the Kubernetes API.

## Upgrading

```bash
helm upgrade explorer ./deploy/helm/skyhook-explorer -n skyhook-explorer
```

## Uninstalling

```bash
helm uninstall explorer -n skyhook-explorer
kubectl delete namespace skyhook-explorer
```
