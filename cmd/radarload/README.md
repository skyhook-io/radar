# radarload

Measure a **local Radar process** while driving it through HTTP. The target
binary can be built from another checkout; radarload imports only the shared
stdlib-only resource identity helper. No Kubernetes credentials are needed.

```sh
go run ./cmd/radarload -url http://localhost:9280 -pid 12345 \
  -duration 10m -sse 3 -askers 4 -qps 20 -seed 1 -per-kind 100 \
  -label main-SHA -out old.json
# Start the other build against the same cluster, then:
go run ./cmd/radarload -url http://localhost:9280 -pid 23456 \
  -duration 10m -sse 3 -askers 4 -qps 20 -seed 1 \
  -subjects-from old.json -label new-SHA -out new.json
go run ./cmd/radarload compare old.json new.json
```

Use the **binary's PID**, not the `go run` parent. RSS and CPU come from local
`ps`; the URL and PID must identify the same process. Use matching startup flags,
identity, subject manifest, cluster activity, and warmup for both builds.
`-user fleet-argo -groups team-a,viewers` adds proxy headers to every request,
including SSE and pprof. Header names are configurable through `-user-header`
and `-groups-header` (defaults: `X-Forwarded-User`, `X-Forwarded-Groups`).

Readiness requires `/api/connection?contexts=0` to say `connected` and
`/api/diagnostics` to report `informers.syncStatus.phase=complete`, including
deferred typed informers. Diagnostics also supplies Radar/Go version and OS/arch;
it does not report a git SHA, so use `-label`. The tool avoids `/api/version-check`,
which can trigger an external update lookup.

Inventory uses Pod, Deployment, Service, ConfigMap, Secret, Ingress, Node and
PersistentVolumeClaim lists. `/api/api-resources` adds discovered HTTPRoute,
GRPCRoute, Gateway, VirtualService, DestinationRule, Application, Kustomization,
HelmRelease and Certificate kinds. Subjects are sorted before seeded sampling,
and saved in the JSON. Unreadable kinds, unavailable discovery, and resource
coverage are recorded. The testserver has no dynamic discovery or CRD fixtures.
The tool understands the actual array and coverage-envelope list contracts.
Lists above the server's 50,000-object limit fail explicitly in inventory; they
are not silently truncated. `-subjects-from` reuses the prior sampled subjects;
current inventory is still recorded for comparison. Neighborhood roots are probed
once before measurement and saved separately: grouped Pods and ordinary Nodes
can be present in resource lists while absent from the topology. Probe rejection
counts are recorded. Replay performs the same probes for warmup, preserves the
saved neighborhood root set, and fails if a saved root is no longer readable.

A seeded 20-request cycle fixes the offered mix:

| Share | Endpoint |
| --- | --- |
| 60% | `/api/resources/{kind}/{namespace}/{name}` |
| 10% | `/api/ai/resources/{kind}/{namespace}/{name}` (includes resource context) |
| 10% | `/api/ai/neighborhood/{kind}/{namespace}/{name}?profile=auto&hops=1` |
| 10% | Same HTTP neighborhood route with `profile=all&hops=2` |
| 5% | `/api/applications` |
| 5% | `/api/resources/{kind}?include=summary` |

There is no distinct non-AI REST neighborhood route in the current server; both
neighborhood groups use the existing read-only REST endpoint. No new server
endpoint is needed. Subjects retain API group, and cluster-scoped paths use `_`.

QPS is combined across workers. A slot without a free worker is dropped and
counted, with no growing queue. Compare achieved QPS, drops and errors alongside
latency; overload can change the successful request mix. Latencies include full
body consumption and errors, including in-flight requests canceled at run end.
SSE consumers discard arbitrarily large frames, count dispatched data events,
and reconnect after errors. The tool does **not** mutate the cluster: a quiet
cluster has fewer change-driven topology broadcasts. Keep the external change
workload fixed when comparing builds.

Every two seconds, plus boundary samples, radarload captures RSS (bytes),
cumulative process CPU (seconds), Go HeapAlloc/HeapInuse/HeapSys/NumGC, and
goroutine count. `ps` CPU precision is platform-dependent (Linux commonly whole
seconds). Pprof sampling itself adds overhead, so use identical settings.
`PauseTotalNs` is parsed when supplied, but standard Go heap text does **not**
emit it. Run GC pause time is instead calculated from the 256-entry `PauseNs`
ring and consecutive NumGC values; per-interval pause deltas are saved with samples. If that ring is missing or overwritten
between samples, the summary reports `gc_pause_ns: null` and an explicit reason;
it never substitutes zero for missing data. Absolute cumulative pause totals
remain null when Go doesn't supply them.

Pprof must be reachable: loopback listeners enable it by default; authenticated
installations also enable it; shared unauthenticated listeners require
`RADAR_PPROF=true`; Cloud does not mount it. Missing/malformed heap or goroutine
profiles fail preflight instead of running without heap samples. Sample and
binary-profile failures during a run are saved and return a nonzero exit.

The 30-second CPU profile is centered inside the measurement window (minimum
run duration: 35s), and a binary heap profile is fetched afterward. Files beside
`old.json` are `old.cpu.pprof` and `old.heap.pprof`. JSON includes config,
subjects/inventory, sample series, build metadata, profiles, RSS/HeapInuse
mean/p95/peak, CPU and GC deltas, SSE counts and endpoint count/errors/p50/p95/p99.
Percentiles use nearest rank. Compare prints B−A and `(B−A)/abs(A)`; changes from
a zero baseline are `n/a` unless both values are zero. It warns when workload
settings or subjects differ. SIGINT saves partial results and returns nonzero.
