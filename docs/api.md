# HTTP API reference

Radar exposes the HTTP API used by its UI. It is useful for local automation and integration work, but it is not currently a versioned compatibility contract; pin the Radar version for long-lived clients. `internal/server/server.go` is the source of truth for the installed build.

The default base URL is `http://127.0.0.1:9280/api`. If Radar uses `--base-path=/radar`, the API moves to `/radar/api` as well.

## Conventions

- Responses are JSON unless the route is documented as SSE, WebSocket, a file download, or NDJSON.
- Most errors use `{"error":"message"}` with an appropriate HTTP status.
- Namespace filters generally accept `?namespace=ns` or `?namespaces=ns1,ns2`.
- Authentication and Kubernetes RBAC follow the deployment mode described in [Authentication](authentication.md). With auth disabled, requests use Radar's connected kubeconfig or ServiceAccount identity.

## Route map

This is a compact map of the operator-facing route families, not a per-field schema.

| Area | Representative routes |
|---|---|
| Health and cluster | `GET /health`, `/cluster-info`, `/capabilities`, `/connection`; `POST /connection/retry` |
| Resource discovery | `GET /namespaces`, `/api-resources`, `/resource-counts`, `/search` |
| Kubernetes resources | `GET /resources/{kind}` and `/resources/{kind}/{namespace}/{name}`; `POST /resources/preview`, `/resources/schemas`, `/resources/apply`; `PUT` or `DELETE` a resource path |
| Topology and activity | `GET /topology`, `/events`, `/events/stream` (SSE), `/changes`, `/timeline/events` (NDJSON), `/issues` |
| Network diagnosis | `GET /trace/{kind}/{namespace}/{name}`; `POST /trace/{kind}/{namespace}/{name}/in-cluster` |
| Audit and policy | `GET /audit`, `/audit/resource/...`, `/policy/resource/...`, `/policy/policies/{policy}` |
| Capacity and upgrades | `GET /capacity`, `/capacity/pools`, `/capacity/demand`, `/capacity/activity`, `/upgrade-readiness?target=1.34` |
| Pods and workloads | Logs, environment, files, exec (WebSocket), restart, scale, revisions, rollback, and run history under `/pods/...` and `/workloads/...` |
| Nodes and lifecycle | Node cordon, uncordon, drain, and debug under `/nodes/{name}/...`; CronJob trigger, suspend, and resume under `/cronjobs/{namespace}/{name}/...`; port forwards under `/portforwards` |
| Helm and images | Release lifecycle and inspection under `/helm/...`; image inspection under `/images/...` |
| GitOps and rollouts | Read models under `/gitops/...`; Argo CD, Flux, and Argo Rollouts actions under `/argo/...`, `/flux/...`, and `/rollouts/...` |
| Metrics and cost | Kubernetes metrics under `/metrics/...`; Prometheus, traffic, and OpenCost under `/prometheus/...`, `/traffic/...`, and `/opencost/...` |
| Integration lookups | RBAC under `/rbac/...`, CloudNativePG under `/cnpg/...`, and Velero under `/velero/...` |
| Contexts and settings | Kubeconfig contexts under `/contexts`; `GET`/`PUT /settings`, `/config`, and integration configuration under `/integrations/...` |

## Examples

```bash
# Server and connection health
curl http://127.0.0.1:9280/api/health

# List Deployments in one namespace
curl 'http://127.0.0.1:9280/api/resources/deployments?namespace=prod'

# Trace a Service without active probes
curl 'http://127.0.0.1:9280/api/trace/service/prod/api'
```

Long-lived streams are outside Radar's normal 60-second request timeout. Mutating routes are enforced by Kubernetes RBAC and may require user impersonation in authenticated deployments. For AI integrations, use the typed [MCP endpoint](mcp.md) instead of coupling an agent to UI-oriented HTTP routes.
