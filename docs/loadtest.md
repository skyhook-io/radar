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
go run ./cmd/testserver -pods 50000 [-port 9281] [-admin-port 9282] \
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

## Relationship cost baselines

Before the family cutover, Radar kept one all-namespace relationship graph in memory for Related Resources,
summaries and AI resource context, rebuilding it on every topology broadcast while a browser was connected.
These baselines retain that implementation for comparison with caller-scoped family reads from the informer cache.

```bash
make bench-relationships
```

Fixtures, the same in both packages:

| Name | Shape | Objects | Pods |
|---|---|---|---|
| L | The field census (`fieldShape` in `pkg/topology/scale_bench_test.go`): 432 namespaces, 248 nodes | 191,504 | 29,455 |
| XL | L with every kind scaled by 10/3 | about 638,000 | about 98,000 |

The graph benchmarks preserve the pre-cutover broadcast relationship path for comparison; production drawer,
summary and resource-context reads now use caller-scoped families. Display topology, GitOps trees, neighborhoods
and Applications still build their own graphs.

What each benchmark reports:

- `BenchmarkRelationshipGraphRetained`: heap the shared graph (`graph-MiB`) and its lookup index (`index-MiB`)
  kept, built with the former broadcaster's options (Secrets included).
- `BenchmarkSharedRelationshipBuild`: one rebuild, time and bytes allocated, with the previous graph and an index
  of its size still held (building the new index happens later, on the next relationship read).
- `BenchmarkDrawerRelationships`: one Related Resources read. `shared-index` is auth off; `per-user-copy` is
  auth on, where each request copies the graph, strips what the user may not list and rebuilds the index.
- `BenchmarkSummaryRelationshipsPage`: a relationship read from an already built graph for every row of a
  1,000-row Pod list. Before cutover, production read graph relationships only for rows with no readable top
  controller, and a stale graph forced a rebuild and index before answering.
- `BenchmarkIndexRelist`: one relist (`Replace`) of a Pod store, with and without an index. Part of it, keying
  the list, runs outside the store's lock, so the total is an upper bound on how long readers wait; an index
  adds only to the locked part, so the difference is its cost. Pods follow the census: one owner each.
- `BenchmarkIndexedStoreChurn`: update cost while eight readers list a namespace, with and without an index.
- `BenchmarkConfigConsumers` (`pkg/relations`): what references a ConfigMap, and a Secret every Pod pulls with,
  answered from the cache by reading the namespace's Pods, ReplicaSets and Deployments. `typical` is the census's
  average namespace (68 Pods); `dense` holds 5,000 Pods, 5,000 ReplicaSets and 2,500 Deployments.
- `BenchmarkNodePods` (`pkg/relations`): a Node's Pods (about 30), by reading every Pod as Radar does and by an
  index of Pods by Node, which Radar doesn't keep; `BenchmarkIndexRelist`'s `with-owner-uid-and-node` prices
  that index on each relist.

Baseline (Apple M1 Pro, 8 cores, Go 1.26, November 2026):

| Measurement | L | XL |
|---|---|---|
| Graph held | 200 MiB (184,512 nodes, 246,320 edges) | 656 MiB (615,036 nodes, 821,058 edges) |
| Its index | 102 MiB | 353 MiB |
| Rebuild (three-run spread) | 1.5-2.0 s, 926 MB allocated | 9.0-10.9 s, 3.0 GB allocated |
| Drawer read, shared index | 0.02-0.13 ms, 2-8 KB | |
| Drawer read, per-user copy (half the namespaces, every cluster-scoped kind readable) | 341 ms, 139 MB | |
| Summary page, a warm read for each of 1,000 Pods | 49 ms, 3 MB | |
| Pod relist, namespace index only | 16 ms | 62 ms |
| Pod relist, plus the owner-UID index | 33 ms | 129 ms |
| Pod update, alone, without / with the owner-UID index | 180 ns / 245 ns | |
| Pod update with eight concurrent readers, without / with | 18.0 µs / 18.8 µs | |
| Config consumers from the cache, typical namespace (ConfigMap / Secret) | 33 µs / 76 µs | |
| Config consumers from the cache, dense namespace (ConfigMap / Secret, 500 listed) | 6 ms / 14 ms | |
| A Node's Pods, reading every Pod | 10 ms, 19 MB | 34-40 ms, 63 MB |
| A Node's Pods, with an index of Pods by Node | 14-27 µs | 15-18 µs |
| Pod relist, owner-UID index plus an index by Node | 34 ms (+5 ms) | 161 ms (+57 ms) |

Radar doesn't keep the index of Pods by Node: a Node's Pods are asked for rarely, and reading every Pod is what
the relationship read does today, while the index would cost every relist. An index of a busy kind earns its
keep only when its question is frequent; the Reflector index (`ReflectsIndex`) files only mirrors, so it holds
next to nothing.

The synthetic fixtures carry more references per Pod than most clusters, so the graph is larger than in a
real process of the same shape. For a current process-level profile, run `go run ./cmd/testserver -census large-eks`
and take `/debug/pprof/heap?gc=1`. The removed broadcast relationship graph no longer appears in that profile; use
the retained graph benchmarks for the historical comparison above. The fake API server keeps its own copy of
every object (`client-go/testing.(*tracker)`), which is not Radar's memory; subtract it.
