# Workload metrics compatibility demo

This is a separate, disposable kind cluster, not an installer for a cloud cluster.
Read `../beyla-demo/README.md` as well: the baseline is built by that script,
including its real two-port/two-replica HTTP application and Redis fixture.
The original Beyla demo's cluster/configurations are untouched.

## Commands

```sh
bash scripts/workload-metrics-demo.sh up
bash scripts/workload-metrics-demo.sh traffic
bash scripts/workload-metrics-demo.sh status
make build
bash scripts/workload-metrics-demo.sh check
bash scripts/workload-metrics-demo.sh history
```

Requires Docker, kind, kubectl, Helm, Node.js, Python 3 and curl. The script uses
its own kubeconfig and fixed `radar-workload-metrics-demo` kind context; it never
changes the user's global current context. `up` is idempotent, but reapplies the
baseline and restarts only demo workloads. No public Service is created.

## Shape and deliberate differences

- Prometheus 3.5, Beyla chart 1.16.10 (app 3.25): reuse the existing demo installer,
  then restrict Beyla to application metrics in `demo`. It does not instrument
  Istio proxies/control-plane processes or collect L4 network flows in this lab.
- Add kubelet/cAdvisor scrapes with original cgroup `id`, and KSM 2.19 workload
  inventory with original Pod `uid`. Every scrape gets `cluster=workload-demo`.
  This is an ordinary labeled single-cluster collector, not proof that unlabeled
  remote stores are safe. No broad node-label copying or custom Radar metric names.
- Istio 1.30.3 revision `radar-lab` watches only its control-plane/fixture namespaces.
  Its sidecars are scraped directly, without merged application metrics. No
  gateway, CNI, ambient or injection into the Beyla baseline namespace.
- Persistent fixtures: two-port/two-replica Istio HTTP server, idle no-server
  worker, Redis StatefulSet and non-injected DaemonSet. The idle fixtures are not
  real queue processors. The Beyla baseline web gets a CPU limit so CFS periods
  can be measured, rather than treating an absent quota as zero throttling.
- Existing demo callers are scaled to zero. `traffic` creates a finite Job, with
  deliberate 503s, against both HTTP fixtures; no endless load generator.

Wait for at least two scrapes; rate/latency assertions need traffic in the queried
window. `check` waits a bounded period for recent usable observations and returns
nonzero when an expected panel is missing, attribution is absent or HTTP errors
are never observed. It saves full Radar API evidence and cleans up its own Radar
and port-forward processes. It never switches an existing Radar instance.

## What this does and does not certify

The baseline checks resource attribution, Beyla and Istio separately, nonzero
errors, latency, multiple ports/replicas and a non-HTTP resource-positive workload.
No aggregate latency equality is assumed between separate Istio counters and
histograms. Deterministic missing-bucket/population and async-histogram cases run
in `pkg/prom`'s real-Prometheus suite, enabled in CI.

VM/Mimir, auth/tenant paths and real cloud workloads have separate dated live
evidence in `docs/workload-metrics.md`; this local fixture does not replace those
tests or claim every backend was recreated here.

`history` captures settled chart values, rotates both HTTP Deployments, verifies
old Pods are deleted, scales each to zero, and requires identical values at the
same historical timestamps with `workload-history` scope. Original replica counts
are restored even on failure; rotated Pods are deliberately not restored. Only
these isolated kind fixtures are changed. Run finite `traffic` first; the test
waits for a complete baseline before any rollout.

Raw KSM history is tested live; recorded ownership and 128-Pod totals are tested
in the real-Prometheus suite. Large live scale, HA deduplication, ingress/waypoints
and native-only histograms remain separate work. Raw metric availability is not
proof of rendered chart quality.

For an existing cloud lab, connect a dedicated Radar instance to the desired
cluster/backend using its normal kubeconfig and metrics credentials. Capture
without mutating that cluster or persisting credentials:

```sh
node scripts/workload-metrics-demo/capture.mjs http://localhost:9347 Deployment/example/web
```

This saves workload responses and per-Pod Istio counter/count/bucket history,
preserving labels. It requires the raw-query permission as well as workload read
access. `RADAR_CAPTURE_AUTHORIZATION` optionally authenticates to Radar (not to
Prometheus); it is never written into evidence or sent through redirects.
Connection evidence omits the backend address and error text, which may contain
credentials. Raw metric labels and cluster names are retained: treat captures as
private operational data, not automatically safe public attachments.
For large raw captures start that dedicated Radar process with
`RADAR_MCP_PROM_MAX_RESPONSE_BYTES=8388608`. The capture fails on summarized
responses instead of presenting them as complete. A capture records observations;
unlike `check`, it does not certify expected chart values.

`down` deletes only this named kind cluster and its local test history. It does
not delete the saved evidence or any cloud installation. Run it deliberately:

```sh
bash scripts/workload-metrics-demo.sh down
```

## Numerical tests and validation evidence

Fast tests live in `pkg/prom/workload_*_test.go`,
`internal/prometheus/workload_metrics_test.go`, and the frontend
`workloadMetricValues.test.ts`.

The numerical suite (also enabled in CI) evaluates the production expressions using Prometheus
itself, including cluster separation, observer separation, unit conversion,
quantiles and duplicate cAdvisor scrape targets:

```sh
RADAR_TEST_PROMTOOL_IMAGE=prom/prometheus:v3.5.0 go test -C pkg ./prom -run 'TestWorkload(History)?PromQL' -v
```

This does not replace live exporter compatibility testing or UI screenshots.
Beyla direct exposition is live-verified, including Radar auto-discovery and a
two-replica HTTP workload with bounded traffic and scheduled 503 responses.
The repeatable kind lab additionally passes all eight core panels for both Beyla
3.25 and Istio 1.30.3, including positive request/error/latency samples from
two-replica, multi-port fixtures. Its worker, Redis StatefulSet and DaemonSet pass
resource-panel checks without inventing HTTP traffic. A fresh VictoriaMetrics
1.151 / Istio 1.30.3 run also returns available p50/p95 samples with the current
histogram checks. These are API-level checks, not a fresh browser sweep.

An unlabeled Istio store still needs an explicit scope assertion; automatic
Istio attribution requires a verified KSM-backed cluster partition. Envoy
[merges histogram observations separately from counters](https://github.com/envoyproxy/envoy/blob/main/source/docs/stats.md),
so latency compares matching label populations and internal histogram consistency,
not equality with the separate request counter. Per-metric Istio Telemetry label
overrides can create structurally different populations; those still withhold
latency. Missing latency never implies no requests or healthy latency.

The reporting-Pod count is derived from request rate series, not the Kubernetes
selection. A partial count does not prove missing instrumentation: Pods can be
idle or newly started. Panels retain useful observations but label incomplete
population coverage in current-only mode. Historical counts describe reporting
Pods at each timestamp, without a current-replica denominator. Rate
windows are at least five minutes and twice the evaluation step. Long ranges
therefore cover the interval but smooth short spikes; the UI shows the actual
window. All request numerators, denominators, histograms and coverage queries use
that same window.

## Future ingress adapter investigation

The Kubernetes Ingress provider in Traefik v3.5 constructs service identifiers
from namespace, Service name and port. A default backend is a separate case.
These are provider-internal identities, not Kubernetes references. See the
[pinned provider source](https://github.com/traefik/traefik/blob/v3.5.0/pkg/provider/kubernetes/ingress/kubernetes.go).

Hyphen concatenation can collide across namespaces: `team-a/api` and `team/a-api`
with the same port. Namespace-scoped Service inspection alone cannot prove that
the identifier uniquely belongs to the requested target. The spike must prove
identity and shared-Service behavior using live metrics before adding this source;
splitting the metric label on hyphens is not an acceptable mapping. No Traefik
adapter is implemented by the current workload metrics feature.
