# Workload request and pressure metrics

These panels extend the expanded
Metrics tab for Deployments, StatefulSets and DaemonSets. They read an existing
Prometheus-compatible backend; Radar does not install instrumentation.

## Establish cluster scope

For a backend dedicated to the connected Kubernetes cluster:

```sh
radar --prometheus-url http://localhost:9090 --prometheus-single-cluster
```

For a shared store with an exact cluster label on its metrics:

```sh
radar --prometheus-url https://metrics.example.com --prometheus-cluster-label cluster=production
```

Repeat `--prometheus-cluster-label` for additional exact label constraints. The
matchers are combined with AND, not OR. An empty value is rejected because it
would also match series without that label. A tenant header alone does not prove
single-cluster isolation. Use existing `--prometheus-header-from-env` for credentials.

These assertions are process-local, apply to the startup Kubernetes connection,
and are not saved to the config file. Changing context, endpoint or headers disables
the new panels until Radar is restarted with a fresh assertion. Existing resource
charts retain their existing query behavior; this is not a retrofit of cluster
isolation onto every metrics API.

Helm installations can provision the assertion with
`traffic.prometheusSingleCluster: true` or `traffic.prometheusClusterLabels`,
and optionally `traffic.beylaJobSelector`. Defaults do not assert trust. Use a
chart and Radar image version containing this feature together; adding these
arguments to an older image is not supported. Desktop configuration UX remains
a follow-up.

Normal startup reinitialization and unchanged settings saves preserve the assertion.
Changing the Kubernetes endpoint/auth configuration under the same context name
discards it too. New pressure queries require cAdvisor container identity labels;
they do not use the older charts' container-label-free fallback.

## What the observations mean

| Panel | Interpretation | Limits |
|---|---|---|
| Requests/sec | Rolling counter rate, summed within one observer | Not a request count or an end-to-end user transaction rate |
| HTTP 5xx | Percentage of that observer's requests with HTTP 5xx responses | Not gRPC/application success; status 0 (no HTTP response) is not counted as 5xx; idle traffic has no defined percentage; incomplete status labels withhold affected samples |
| p50 / p95 | Quantiles of aggregated histogram buckets, in seconds | Approximations; missing/partial bucket coverage or mismatched bucket populations withhold affected samples |
| CPU throttled periods | Throttled CFS periods divided by total CFS periods, per Pod | Not CPU time lost; missing CFS counters are unavailable |
| Compare current Pods | CPU, memory working set and throttling, sortable descending | Missing/stale/gap samples show a dash, not zero |

All new queries use the current ownership-resolved Pod set and explicit backend
scope. This is not historical ownership reconstruction. At most 100 current Pods
are included; a larger workload is visibly partial. The range is bounded to about
360 evaluations per series and queries run with at most three concurrent calls and
a 25-second request deadline. Raw HTTP route, method, peer, and other series labels
are not returned to the browser.

## Request sources

- **Istio destination sidecars:** `istio_requests_total` and
  `istio_request_duration_milliseconds_bucket`, `reporter="destination"`,
  `request_protocol="http"`. Requires scrape `namespace` and `pod` labels identifying
  the destination Pod, as well as `destination_workload_namespace`. This avoids
  attributing metrics solely from a workload name, which has no kind or UID.
  Waypoint reporters cannot use this mapping and are not currently included.
- **Beyla HTTP server:** `http_server_request_duration_seconds_count` and `_bucket`,
  with `k8s_namespace_name` and `k8s_pod_name`. Uses Radar's existing Beyla/Alloy job
  discriminator. `--beyla-job-selector` accepts one exact or regex `job` matcher for
  these panels. Application-only Beyla works without network metrics: verified with
  direct exposition in kind and EKS nonprod (Beyla 3.32.0). Default OTLP-to-Alloy
  conversion was also tested and lacks Pod attributes on the request series;
  that pipeline is not supported by this adapter.

When both sources have data, Istio is the default. The selector changes observers;
their rates are never added together. One source failing does not turn the other's
successful data into an error or fabricate a zero. Sources with only historical
rate evaluations are marked stale. This is evaluation freshness, not a guarantee
about the scrape timestamp of every underlying counter.

## Verification

Fast tests live in `pkg/prom/workload_*_test.go`,
`internal/prometheus/workload_metrics_test.go`, and the frontend
`workloadMetricValues.test.ts`.

The opt-in numerical suite evaluates the production expressions using Prometheus
itself, including cluster separation, observer separation, unit conversion,
quantiles and duplicate cAdvisor scrape targets:

```sh
RADAR_TEST_PROMTOOL_IMAGE=prom/prometheus:v3.5.0 go test -C pkg ./prom -run TestWorkloadPromQL -v
```

This does not replace live exporter compatibility testing or UI screenshots.
Beyla direct exposition is live-verified, including Radar auto-discovery and a
two-replica HTTP workload with bounded traffic and scheduled 503 responses.
Istio remains numerical-test validated only; its deployment variants have not
been live-certified.

The reporting-Pod count is derived from request rate series, not the Kubernetes
selection. A partial count does not prove missing instrumentation: Pods can be
idle or newly started. Panels retain useful observations but label incomplete
population coverage. Previous replicas are never silently reconstructed. Rate
windows are at least five minutes and twice the evaluation step. Long ranges
therefore cover the interval but smooth short spikes; the UI shows the actual
window. All request numerators, denominators, histograms and coverage queries use
that same window.

## Traefik feasibility notes

The Kubernetes Ingress provider in Traefik v3.5 constructs service identifiers
from namespace, Service name and port. A default backend is a separate case.
These are provider-internal identities, not Kubernetes references. See the
[pinned provider source](https://github.com/traefik/traefik/blob/v3.5.0/pkg/provider/kubernetes/ingress/kubernetes.go).

Hyphen concatenation can collide across namespaces: `team-a/api` and `team/a-api`
with the same port. Namespace-scoped Service inspection alone cannot prove that
the identifier uniquely belongs to the requested target. The spike must prove
identity and shared-Service behavior using live metrics before adding this source;
splitting the metric label on hyphens is not an acceptable mapping. No Traefik
adapter is shipped by the current worktree yet.
