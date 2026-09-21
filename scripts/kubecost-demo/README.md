# Kubecost demo cluster

`./scripts/kubecost-demo.sh` (or `make kubecost-demo`) creates a disposable
`kind` cluster with Kubecost 3, deterministic prices, and a small application
fixture. It exists to test the behavior that mocks cannot prove: FinOps Agent
emission, Aggregator ingestion, the live allocation/asset response shapes, and
Radar's two local-Kubernetes connection lanes.

This uses Kubecost's free self-hosted chart without a product key. It is still a
substantial local workload: the Aggregator requests 3 GiB of memory, and the
images take time to download on the first run. Give Docker roughly 6 GiB or
more. The script disables the frontend, forecasting, network-cost, cloud-cost,
cluster-controller, telemetry, diagnostics, and heartbeat components; it uses
ephemeral storage because the whole cluster is disposable.

## Coverage matrix

| Scenario | Command | What it proves |
|----------|---------|----------------|
| Real Kubecost data | `up` | Kubecost 3.2.4's FinOps Agent, local store, and Aggregator produce allocation data for the exact Deployment, StatefulSet, and DaemonSet fixture pods. Export intervals are reduced to one minute, but ingestion is still asynchronous and bounded at ten minutes. |
| Raw REST shape | `query` | The active Aggregator exposes allocation and asset data through either the root or `/model` path, carries the configured cluster identity, namespaces, node assets, and any controller identity it resolved. The output calls out pods without controller identity. |
| Local Radar | Run Radar locally after `up`, then `RADAR_BASE_URL=http://localhost:9347 ./scripts/kubecost-demo.sh radar-smoke` | Radar discovers the active Aggregator StatefulSet and named `tcp-api` Service, then owns a scoped port-forward because the ClusterIP is unreachable from the host. The same API contract assertions used in-cluster run against local Radar. |
| In-cluster Radar | `install-radar` | The current Go tree is built into the cluster and connects directly through Kubecost Service DNS, without a port-forward. |
| Radar contract | `radar-smoke` | Summary, workload, node, and cluster trend endpoints report `source=kubecost`, current data has freshness metadata, and detected currency is EUR. |
| Cleanup | `down` | Deletes the kind cluster and all of its ephemeral storage. `install-radar` leaves the reusable `radar-kubecost-demo:dev` image in the host Docker image store. |

## Typical flow

```bash
make kubecost-demo
./scripts/kubecost-demo.sh query

kubectl config use-context kind-radar-kubecost-demo
make build
RADAR_COST_SOURCE=kubecost \
RADAR_KUBECOST_CLUSTER_ID=radar-kubecost-demo \
./radar --port 9347 --no-browser

RADAR_BASE_URL=http://localhost:9347 \
./scripts/kubecost-demo.sh radar-smoke
```

Open `http://localhost:9347/cost`. Expected behavior:

- the source label says Kubecost;
- values are labeled EUR, proving the Aggregator's literal
  `DISPLAY_CURRENCY` was detected;
- `cost-demo` contains `checkout`, `orders`, and `telemetry`;
- the node table contains the kind node;
- current values carry a freshness timestamp;
- the cluster trend uses the allocation history retained by Kubecost.

To exercise the in-cluster path instead:

```bash
./scripts/kubecost-demo.sh install-radar
./scripts/kubecost-demo.sh radar-smoke
```

`install-radar` intentionally builds the current Go tree directly. It proves
backend discovery and API behavior, not the frontend embedding pipeline. Use
`make build` plus local Radar or the visual-test workflow when evaluating UI
changes.

## Commands

```text
./scripts/kubecost-demo.sh up
./scripts/kubecost-demo.sh status
./scripts/kubecost-demo.sh query
./scripts/kubecost-demo.sh install-radar
./scripts/kubecost-demo.sh radar-smoke
./scripts/kubecost-demo.sh reset
./scripts/kubecost-demo.sh down
```

Every Kubernetes and Helm operation uses the explicit
`kind-radar-kubecost-demo` context. Creating the cluster may make it the current
kubectl context, as `kind` normally does, but the script never relies on or
modifies another cluster.

## Configuration overrides

The defaults are pinned so reruns are comparable. Override only what the test
requires:

```bash
CLUSTER_NAME=my-kubecost-test ./scripts/kubecost-demo.sh up
KUBECOST_CHART_VERSION=3.2.5 ./scripts/kubecost-demo.sh reset
DISPLAY_CURRENCY=GBP ./scripts/kubecost-demo.sh reset
DATA_TIMEOUT_SECONDS=900 ./scripts/kubecost-demo.sh up
```

`CLUSTER_ID` defaults to `CLUSTER_NAME`; override it only when the Kubecost
identity should deliberately differ from the kind cluster name.
`KUBECOST_LOCAL_PORT` and `RADAR_LOCAL_PORT` can be changed when ports 39004 or
39280 are occupied.

## Constraints learned from the live API

- **Pod readiness is not data readiness.** The FinOps Agent emits files and the
  Aggregator ingests them asynchronously. An empty first query is normal;
  `up` waits for all three exact fixture pods rather than treating Ready pods or
  one namespace-level row as proof of cost data.
- **Controller identity can be partial.** In a fresh Kubecost 3.2.4 install,
  the Aggregator reports the `orders-0` allocation with
  `__unallocated__` controller fields even though the pod belongs to a
  StatefulSet. `query` exposes this as `podsWithoutController`; `radar-smoke`
  verifies that raw premise using the same `1h`-then-rolling-`24h` current-data window
  selection as Radar, then asserts Radar recovered the `orders` row through its
  Kubernetes pod-owner lookup alongside the Deployment and DaemonSet.
- **The Aggregator is intentionally not made lightweight by changing its
  memory request.** Kubecost 3's default request is 3 GiB. Lowering it makes the
  demo easier to schedule but turns OOM/restart behavior into test noise.
- **The database and local store are ephemeral only here.** This reaches all
  Radar read paths without leaving large PVCs. It is not a production Kubecost
  recommendation.
- **EUR is a label, not a Radar conversion.** Deterministic custom prices make
  non-zero values stable enough to inspect; Radar preserves Kubecost's currency
  meaning and never converts those numbers.
- **Cluster history reflects Kubecost retention.** The demo's ephemeral store
  may only contain recent buckets, so the trend proves that Radar preserves and
  displays the history Kubecost returned; it does not assert a retention period.
  Workload and application trends remain current-only because historical owner
  attribution is a separate contract.
- **The two connection lanes are not interchangeable.** Local Radar must use
  its managed port-forward; in-cluster Radar must use Service DNS. A success in
  one does not prove the other. `install-radar` explicitly withholds
  `pods/portforward` RBAC, so its fallback cannot succeed and a green in-cluster
  smoke run proves the direct Service-DNS lane. Enabling that permission would
  make the lanes indistinguishable in this test.

Delete the cluster and its ephemeral storage with:

```bash
make kubecost-demo-down
```
