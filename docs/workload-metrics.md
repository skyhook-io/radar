# Workload request and pressure metrics

These panels extend the expanded
Metrics tab for Deployments, StatefulSets and DaemonSets. They read an existing
Prometheus-compatible backend; Radar does not install instrumentation.

## Which workloads benefit?

The resource panels apply to service processes and background workers alike.
HTTP request panels are conditional on observed metrics, not declared container
ports, Services, or Ingresses. Radar does not scan application ports to generate RED
(request rate, errors, duration) data.

| Workload | Current behavior |
|---|---|
| HTTP server with no `containerPort` declarations | Can show RED if the supported collector actually observes and exports its HTTP traffic. Missing declarations do not gate these panels. |
| No inbound HTTP traffic or usable request series | No large empty RED grid: a compact status message or disclosure remains, followed by any available resource panels. Missing series are not a measured zero. |
| Several HTTP ports/processes in the selected Pods | Matching server observations within the selected source are aggregated across them. There is no port/Service/route selector. Error percentage uses the combined numerator and denominator; latency uses combined histogram buckets, not an average of per-port percentiles. |
| Mixed HTTP and other protocols | Only the supported HTTP observations enter these panels. Their rate is not total activity across protocols. |
| Queue-pulling worker | CPU, memory, throttling, restarts and existing resource views remain useful. Queue depth, consumer lag, work completion, retries and processing duration are not implemented by these HTTP panels. |
| Worker with only an HTTP health/admin endpoint | Its HTTP traffic can appear. That is health/admin traffic, not evidence of queue-processing throughput or success. |

Radar does not universally exclude health checks, metrics scrapes or admin routes.
If the exporter includes them, the workload aggregate can include them. Multiple
ports do not themselves mean duplicate scrapes, but multiple observation jobs or
known HA populations can trigger the ambiguity guard. Incompatible histogram
populations can withhold latency while request rate remains useful. A future
port/route breakdown requires labels the source actually retains; Kubernetes port
declarations cannot recover dimensions already aggregated away by the exporter.

## What each chart needs

All new panels require a reachable Prometheus-compatible query API and readable
current workload/Pod membership. They then require either verified source identity
or an explicit operator scope assertion. KSM below means **kube-state-metrics**;
it exports Kubernetes object state, not application request telemetry.

| Chart/data | Required metric data | Identity / interpretation |
|---|---|---|
| CPU usage | `container_cpu_usage_seconds_total` | cAdvisor `namespace`, `pod`, nonempty application `container`; UID in `id`, or verified cluster partition / override. Rate in cores, per Pod. |
| Memory working set | `container_memory_working_set_bytes` | Same resource identity contract; gauge in bytes, per Pod. |
| CPU throttled periods | `container_cpu_cfs_throttled_periods_total` and `container_cpu_cfs_periods_total` | Both counters must describe the same population. Missing CFS metrics mean unavailable, not zero throttling. |
| Beyla HTTP requests/sec | `http_server_request_duration_seconds_count` | `k8s_namespace_name`, `k8s_pod_name`, attributable identity and a resolved job. Direct UID path uses `k8s_pod_uid`. |
| Beyla HTTP 5xx | The same count, split by `http_response_status_code` | Needs usable status coverage. Idle traffic does not have a defined error percentage. |
| Beyla p50/p95 | `http_server_request_duration_seconds_bucket` with `le`, including `+Inf`, plus the count above | Classic histogram buckets must cover the request population; seconds. |
| Istio HTTP requests / 5xx | `istio_requests_total`, including `response_code` for errors | `reporter="destination"`, `request_protocol="http"`, destination workload namespace and scrape `namespace`/`pod`; verified partition or override. |
| Istio p50/p95 | `istio_request_duration_milliseconds_bucket` with `le`, plus the request counter | Same identity and population as request count; Radar converts milliseconds to seconds. |
| Reporting Pods | Matching request rate series | Distinct observed Pods, not the number selected from Kubernetes; idle/new Pods can explain a shortfall. |
| Pod comparison | The same CPU/memory/throttling panels above | No independent, weaker query for comparison values. |
| Request/limit overlays | Current Kubernetes container resource settings | No Prometheus/KSM series needed for the lines themselves. Not historical settings; unlimited containers suppress the whole-Pod limit line. |
| Existing restart lane | `kube_pod_container_status_restarts_total` | KSM; existing name-based scope, not the new UID attribution path. |
| Existing network / filesystem I/O | `container_network_receive_bytes_total`, `container_network_transmit_bytes_total`; `container_fs_reads_bytes_total` and `container_fs_writes_bytes_total` | Existing namespace + exact selected Pod-name matching. These are traffic/I/O rates, not PVC capacity or application requests. |

Metrics Server alone does not provide this historical Prometheus data. A
Prometheus installation scraping kubelet/cAdvisor can support resource panels
without Beyla or Istio. KSM can supply inventory for fallback attribution, but does
not create missing cAdvisor counters or HTTP histograms. Retention, scrape gaps,
collector filters and relabeling still determine which panels are available.

## What Radar discovers automatically

There are three separate steps; success at one does not imply success at the next:

1. **Find a query endpoint.** At cluster startup/reconnect, probe known Service
   locations and ranked dynamic candidates. These include common Prometheus and
   VictoriaMetrics installations; dynamic discovery recognizes query services
   such as Thanos Query. Radar tries direct connectivity and, from a laptop,
   Kubernetes port-forwarding when needed and permitted. A manual URL overrides
   discovery. Hosted endpoints, tenants and credentials are not guessed. Configured
   HTTP headers require an explicit URL so credentials do not reach discovered
   candidates. One selected backend supplies these panels; Radar does not combine
   data across every Prometheus it finds.
2. **Attribute a workload's sources.** On opening Metrics, inspect metric families
   for current Pod identities. CPU, memory, throttling, Beyla and Istio are checked
   independently. Matching identities can reveal a custom Beyla scrape job;
   automatic matching does not require its name to contain `beyla`.
3. **Query usable observations.** Fetch the time window and check counter, status,
   histogram and observation-population coverage. Reachability and identity are
   not guarantees that a requested chart has usable data.

Radar does not install Prometheus, enable kubelet scraping, configure
ServiceMonitors/PodMonitors, deploy Beyla/sidecars, repair missing resource labels,
or infer worker semantics from HTTP health endpoints.

## Automatic attribution

Radar checks each source independently when the workload Metrics tab opens. No
scope flag is needed when the evidence is sufficient:

- Beyla: match application `k8s_pod_name` and `k8s_pod_uid` to current Kubernetes Pods.
- cAdvisor: match Pod name and an exact Pod UID segment in the `id` cgroup path
  (cgroupfs or systemd). CPU, memory and throttling are checked independently.
- Otherwise, use `kube_pod_info` name/UID pairs to establish a unique one- or
  two-label cluster partition, then require those same labels on the source.
  Recognized labels are `cluster`, `cluster_name`, `k8s_cluster_name`,
  `kubernetes_cluster`, and `cluster_id`. Istio uses this path, not a guessed UID.

Unlabeled KSM inventory alone does not prove a single-cluster backend. Missing
identity, conflicting populations, partial query responses and ambiguous scrape
jobs leave only the affected source unavailable. Matching does not install
anything or require new Kubernetes permissions. Known HA labels (`replica`,
`prometheus_replica`, `__replica__`) with multiple values are rejected; backends
using other replica labels must deduplicate upstream. No arbitrary label stripping
is attempted.

Evidence probes run asynchronously with an eight-second deadline after shared
Prometheus discovery finishes (discovery has its own 60-second bound), two active workloads
at most, a bounded 128-entry memo and a 30-second per-workload churn guard.
Evidence is bounded to 1,024 identities per metric family, 256 KSM rows and a
4 MiB response. Positive results expire after five minutes and refresh ahead of
expiry; negative results retry after 30 seconds. During Pod churn, unexpired
evidence is retained only for unchanged name/UID pairs while replacement Pods
are checked. Its original expiry is not extended. Connection changes invalidate
all attribution and their old churn timers. Sources covering a subset report partial coverage.

UID-filtered charts exclude previous incarnations of same-name Pods. A
partition-only query cannot distinguish same-name Pod incarnations in history;
it proves the cluster, not historical Pod lifetime. The expanded workload CPU
and memory charts use the same attributed samples as Pod comparison. Network,
storage and other existing metrics surfaces retain their previous query behavior.
Template request/limit overlays describe current per-Pod settings, not history.

## Optional operator override

If automatic matching cannot establish identity, an operator can explicitly
assert the backend's scope. These assertions take precedence over probing.

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
and are not saved to the config file. Changing context, endpoint or headers discards
the assertion and returns to automatic matching. Restart to supply a fresh assertion. Existing resource
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
Reconstructing a kubeconfig `proxy-url` callback on laptop reconnect also discards
the assertion conservatively. The UI reports this loss; restart to reassert it.
Explicit assertions are labeled in the UI and do not bypass the request charts'
per-evaluation checks for multiple known replica/job populations.

## What the observations mean

| Panel | Interpretation | Limits |
|---|---|---|
| Requests/sec | Rolling counter rate, summed within one observer | Not a request count or an end-to-end user transaction rate |
| HTTP 5xx | Percentage of that observer's requests with HTTP 5xx responses | Not gRPC/application success; status 0 (no HTTP response) is not counted as 5xx; idle traffic has no defined percentage; incomplete status labels withhold affected samples |
| p50 / p95 | Quantiles of aggregated histogram buckets, in seconds | Approximations; missing/partial bucket coverage or mismatched bucket populations withhold affected samples |
| CPU throttled periods | Throttled CFS periods divided by total CFS periods, per Pod | Not CPU time lost; missing CFS counters are unavailable |
| Compare current Pods | CPU, memory working set and throttling, sortable descending | Missing/stale/gap samples show a dash, not zero |

All new queries use the current ownership-resolved Pod set and either verified
source identity or explicit backend scope. This is not historical ownership reconstruction. At most 100 current Pods
are included; a larger workload is visibly partial. The range is bounded to about
360 evaluations per series and queries run with at most three concurrent calls and
a 25-second request deadline. Raw HTTP route, method, peer, and other series labels
are not returned to the browser.

Workload range queries and attribution probes use POST form bodies and a 16,000-byte decoded query
limit. Long Pod names or large identity sets can reach this limit before 100
Pods; affected panels explain the limit instead of silently dropping Pods.
HTTP error and coverage ratios are calculated from matching timestamps in the
returned counters; histogram quantiles remain calculated by the metrics backend.
Warnings or `isPartial: true` withhold the response; informational annotations
alone do not. Unknown HA label conventions still require upstream deduplication.

### Why the population and query bounds exist

Radar still enumerates the current workload's Pods from the local informer cache,
by controller ownership, and embeds their exact names and (where used) UIDs in
batched queries. It does not make one Kubernetes API or Prometheus request per Pod.
The compact identity expressions replaced repeated per-Pod query branches; they
did not remove identity enumeration or make query size independent of Pod count.

The 100-Pod selection cap is a Radar implementation guardrail, not a Prometheus
limit or a measured performance cliff. It bounds attribution evidence, query size,
returned per-Pod series and browser work together with the independent limits
above. Larger populations need a designed query path, not an arbitrary increase
to one constant: the identity builder and evidence limits must agree. Chunking
must merge counters and histogram buckets before deriving percentages/quantiles,
preserving cross-chunk observation checks; averaging chunk p95s is incorrect.

### Historical Pods and existing resource queries

"Current" means Pods still present in the cache and owned by the workload; it
does not mean only Ready Pods. Old-revision Pods still present during a rollout
can be included. Deleted Pods are not reconstructed, and scaling to zero produces
no historical request/resource panels through this endpoint.

Radar's rightsizing engine already has KSM owner joins (`kube_pod_owner`, plus
`kube_replicaset_owner` for Deployments) evaluated over history. Those are a useful
starting point, not a drop-in replacement for this endpoint's identity checks.
A historical path must scope both the ownership and metric sides to the right
cluster, handle reused names/UIDs and missing historical ownership, and explain
which deleted Pods are covered. A partition-only match does not establish Pod
incarnation identity. This remains a separate follow-up rather than weakening
current UID matching to a workload-name prefix.

The existing network, filesystem and restart queries use exact current Pod names
and namespace, not fuzzy workload-name prefixes. They still lack the new UID and
verified cluster-partition matching: a same-name Pod in a shared store or a prior
incarnation can affect historical values. They are separate context, not proof of
the new panels' population. Network/storage stay in the lower, explicitly labeled
section for this release. Extending identity checks requires probing each metric
family; CPU's usable `id`/container labels cannot be assumed on Pod-network or
filesystem series.

### Unsupported sources and formats, in plain language

- **Ingress metrics:** observations made by a reverse proxy before requests reach
  application Pods. They can be useful without Beyla/mesh, but describe routed
  traffic and proxy failures, not every request or task handled by the workload.
  No ingress-controller metric adapter is included here.
- **Native-only histograms:** another Prometheus representation of latency data,
  stored in histogram samples instead of separate `_bucket{le=...}` series. The
  current latency queries use classic buckets. A backend can support PromQL yet
  lack the classic series these charts need. Separate counters, where present,
  can still support rate/errors; a native-only Beyla histogram may also lack the
  `_count` series the current request adapter expects. Native support must cover
  discovery, count extraction, quantiles and population checks together.
- **Istio waypoints:** shared proxies used in ambient mesh. The scraped proxy Pod
  is not the application Pod whose traffic it observes. The current destination
  sidecar mapping cannot be reused by simply accepting another reporter value.

See the upstream [Prometheus histogram query documentation](https://prometheus.io/docs/prometheus/latest/querying/functions/#histogram_quantile),
[Istio metrics reference](https://istio.io/latest/docs/reference/config/metrics/),
and [Beyla attribute configuration](https://grafana.com/docs/beyla/latest/configure/metrics-traces-attributes/).
These explain upstream capabilities; Radar's narrower supported mappings are
listed above and verified as described below.

## Request sources

- **Istio destination sidecars:** `istio_requests_total` and
  `istio_request_duration_milliseconds_bucket`, `reporter="destination"`,
  `request_protocol="http"`. Requires scrape `namespace` and `pod` labels identifying
  the destination Pod, as well as `destination_workload_namespace`. This avoids
  attributing metrics solely from a workload name, which has no kind or UID.
  Waypoint reporters cannot use this mapping and are not currently included.
- **Beyla HTTP server:** `http_server_request_duration_seconds_count` and `_bucket`,
  with `k8s_namespace_name` and `k8s_pod_name`. Automatic attribution discovers an
  exact job from matching identities, including custom job names. With an explicit
  scope override, Radar uses its Beyla/Alloy job discriminator;
  `--beyla-job-selector` accepts one exact or regex `job` matcher.
  Application-only Beyla works without network metrics: verified with
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
An Istio 1.30.3 sidecar with the official Prometheus sample was also exercised
live. Its unlabeled store needs an explicit scope assertion; automatic Istio
attribution requires a verified KSM-backed cluster partition. Request and error
charts worked with the assertion, but histogram counts lagged the separate request
counter during traffic, so the strict coverage check withheld latency samples.
Istio latency compatibility remains incomplete; do not interpret missing latency
as no requests or a healthy latency result.

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
