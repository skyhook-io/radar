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
`-user fleet-argo -groups team-a,viewers` sends Radar's default proxy headers
(`X-Forwarded-User`, `X-Forwarded-Groups`) on every request, including SSE and
pprof, for a target running `--auth-mode=proxy`.

Readiness requires `/api/connection?contexts=0` to say `connected` and
`/api/diagnostics` to report `informers.syncStatus.phase=complete`, including
deferred typed informers. Diagnostics also supplies Radar/Go version and OS/arch;
it does not report a git SHA, so use `-label`. The tool avoids `/api/version-check`,
which can trigger an external update lookup.

Inventory uses Pod, Deployment, Service, ConfigMap, Secret, Ingress, Node and
PersistentVolumeClaim lists. `/api/api-resources` adds discovered HTTPRoute,
GRPCRoute, Gateway, VirtualService, DestinationRule, Application, Kustomization,
HelmRelease and Certificate kinds. Subjects are sorted before seeded sampling
and saved in the JSON; unreadable kinds, unavailable discovery and each list's
coverage are recorded. For an offline target, point it at the replay or census
testservers. Lists above the server's 50,000-object limit fail in inventory
rather than being truncated.

`-subjects-from` reuses a prior run's subjects; current inventory is still
recorded. Every subject, fresh or replayed, is GET-probed with the configured
identity before measurement, and unreadable ones are dropped and counted by kind
and group in `subject_probe_errors`, so two builds compared against one manifest
run the same subjects only when neither drops any (`compare` warns when they
differ). Neighborhood roots are probed and saved separately, since grouped Pods
and ordinary Nodes can be listed yet absent from the topology. Replay keeps the
saved roots that survive the subject probe and fails if one of them no longer
returns a neighborhood.

A seeded 20-request cycle fixes the offered mix:

| Share | Endpoint |
| --- | --- |
| 60% | `/api/resources/{kind}/{namespace}/{name}` |
| 10% | `/api/ai/resources/{kind}/{namespace}/{name}` (includes resource context) |
| 10% | `/api/ai/neighborhood/{kind}/{namespace}/{name}?profile=auto&hops=1` |
| 10% | Same HTTP neighborhood route with `profile=all&hops=2` |
| 5% | `/api/applications` |
| 5% | `/api/resources/{kind}?include=summary` |

Both neighborhood shares use the same read-only route; there is no separate
non-AI neighborhood endpoint.

QPS is combined across workers. A slot without a free worker is dropped and
counted, with no growing queue. Compare achieved QPS, drops and errors alongside
latency; overload can change the successful request mix. Latencies include full
body consumption and errors, including in-flight requests canceled at run end.
SSE consumers discard arbitrarily large frames, count dispatched data events,
and reconnect after errors. The tool does **not** mutate the cluster: a quiet
cluster has fewer change-driven topology broadcasts. Keep the external change
workload fixed when comparing builds.

RSS and cumulative process CPU are read every two seconds. Go heap figures
(HeapAlloc/HeapInuse/HeapSys/NumGC) and the goroutine count are read before the
workload, every 30 seconds and on the window's last tick, because producing a
heap profile costs the target CPU. `ps` CPU precision is platform-dependent
(Linux commonly whole seconds). Samples that finish after the window are
discarded. GC pause time comes from the 256-entry `PauseNs` ring between
consecutive heap reads; if the ring was overwritten (more than 256 GCs between
reads), `gc_pause_ns` is null with a reason, never zero.

Pprof must be reachable: loopback listeners enable it by default; authenticated
installations also enable it; shared unauthenticated listeners require
`RADAR_PPROF=true`; Cloud does not mount it. Missing/malformed heap or goroutine
profiles fail preflight instead of running without heap samples. Sample and
binary-profile failures during a run are saved and return a nonzero exit.

A 30-second CPU profile is taken in the middle of the window (minimum run:
35s) and a binary heap profile afterward, saved beside the JSON as
`old.cpu.pprof` and `old.heap.pprof`. Percentiles use nearest rank. `compare`
prints B−A and `(B−A)/abs(A)` (`n/a` from a zero baseline) and warns when
workload settings or subjects differ. SIGINT saves partial results and returns
nonzero.
