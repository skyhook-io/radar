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
tests or claim every backend was recreated here. Historical membership, large
scale, HA deduplication, ingress/waypoints and native-only histograms remain
separate work. Raw metric availability is not proof of rendered chart quality.

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
