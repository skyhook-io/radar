# UI load testing (synthetic fake cluster)

Drive Radar's UI at high object counts — tens of thousands of pods — without a
real cluster, real workloads, a kubelet, or etcd. Radar connects to an in-process
fake Kubernetes client and treats the synthetic objects exactly as it would a
real cluster: the same informer cache, topology builder, SSE broadcaster and
React render path all run unchanged.

This is the leanest realization of the industry pattern behind
[KWOK](https://kwok.sigs.k8s.io/) ("Kubernetes WithOut Kubelet") — fake objects,
no containers — but with no separate control-plane process at all: the objects
live only as Go values in the test server's memory.

## Run

```bash
make loadtest                 # 50,000 pods on :9281, admin on :9282
make loadtest PODS=10000      # 10,000 pods
make loadtest PODS=50000 LOADTEST_PORT=9351
```

Or directly:

```bash
go run -tags withoutebpf ./cmd/testserver -pods 50000 [-port 9281] [-admin-port 9282] \
  [-nodes 50] [-namespaces 10] [-pods-per-app 200]
```

With `-pods 0` (the default) the server seeds the small nginx fixture instead —
the fixture the Playwright e2e suite relies on.

## What gets generated

A topology-realistic population, not a flat pile of orphan pods:

- `N` pods, grouped into apps of `pods-per-app` (default 200)
- one Deployment → ReplicaSet → Pods chain per app, with correct ownerReferences
- a matching Service, ConfigMap and Secret per app (the pod template references
  the ConfigMap and Secret, so topology shows the "Configures" edges)
- pods spread round-robin across `nodes` fake Nodes
- apps spread round-robin across `namespaces` namespaces

Names are deterministic (`app-0004-000817`, `loadtest-node-042`, `loadtest-03`),
so the same count always produces the same objects.

## Control the pod count live

The admin listener (default `<port>+1`) adjusts the count at runtime. Radar's
informers observe the create/delete events and the UI updates over SSE with no
reload:

```bash
curl -XPOST localhost:9282/loadtest/scale -d '{"pods":10000}'
curl localhost:9282/loadtest/status
# {"pods":10000,"target":10000,"nodes":50,"namespaces":10,"pods_per_app":200}
```

`scale` returns once the informer cache has converged on the target
(`"converged": true`).

## How it stays honest at scale

The initial population is handed to the fake clientset at construction, so it
arrives through the informers' initial **LIST** — safe at any count. Live scaling
travels the fake clientset's **watch** channel, which panics if more than 100
events queue before the informer drains them. The scaler therefore mutates in
batches below that bound and blocks on the informer's observed count between
batches, so it never overflows and always converges.

## Scope and limits

- This exercises everything **at and above** Radar's informer store — informer
  memory, topology CPU, SSE fan-out, virtualized render. It deliberately does
  **not** exercise the real network transport, client-go decode, or a real
  apiserver; for that fidelity, point Radar at a KWOK cluster instead (the
  generated object shapes are standard Kubernetes objects and feed KWOK as-is).
- The test server does not initialize API discovery, so the sidebar count badges
  for **grouped** kinds (Deployments, ReplicaSets, Jobs, …) render as "–" even
  though the data is present (`/api/resource-counts` returns the real numbers).
  Core-group kinds (Pods, Services, ConfigMaps, Secrets) show counts. Wiring
  discovery (via `InitTestDynamicResourceCache`) is the same step CRD kinds such
  as HTTPRoutes need, and is the natural next extension.
- The Pods and ReplicaSets tables fetch `?include=summary` rows (only the fields
  the table reads, 5–8x smaller than full objects at realistic pod weight) and
  guard above 50,000 rows in scope; Events and EndpointSlices still guard at
  25,000. Both limits are real Radar responsiveness boundaries this harness is
  well suited to exercise.
