# Inspektor Gadget integration spike (2026-07-19)

This validates Phase 0 of `inspektor-gadget-plan.md` against Inspektor Gadget
v0.54.1. The spike used the released macOS arm64 client, the official DaemonSet,
the released OCI gadgets, upstream source at `v0.54.1`, and the official
`ig-mcp-server` source.

## Verdict

**Go.** The embedded Go gRPC runtime is the right Phase 1 transport. The binary,
dependency, portability, lifecycle, targeting, and impersonation gates passed.
The implementation should incorporate the corrections below.

## Environments

- `kind-radar-gitops-demo`: IG installed and detected, but released gadgets cannot
  load on Docker Desktop's `5.10.124-linuxkit` arm64 kernel because it has no BTF.
  This is a useful unsupported-host fixture, not a viable live-gadget fixture.
- `radar-test-nonprod`: IG v0.54.1 installed on nine Amazon Linux 2023 nodes
  (`6.12.x`, x86_64 + arm64). Process, socket, and DNS gadgets ran successfully.
- Disposable `ig-spike` workloads exercise a long-running process, successful and
  NXDOMAIN DNS, an established TCP connection, and a two-container Pod.

## Runtime findings

### Snapshots

`snapshot_process` and `snapshot_socket` emit a single `datasource.TypeArray`
packet containing the complete snapshot, but the overall gadget session does not
self-terminate. A CLI run remained attached after the array was printed.

Phase 1 completion rule:

1. subscribe with `SubscribeArray`, not the per-row `Subscribe` callback;
2. copy the full array packet;
3. cancel the manager-owned gadget context;
4. wait for `RunGadget` cleanup with a bounded deadline.

This is an explicit datasource completion boundary, so no quiet-period heuristic
is needed for the released snapshot gadgets. A startup/collection deadline still
produces a partial result when no array arrives.

One-second CLI-bounded snapshots took about 6.6-7.5 seconds end-to-end because the
runtime waits for remote stop/result cleanup after cancellation. Radar should freeze
and expose the result as soon as the array arrives, transition the run to `stopping`,
and let manager-owned cleanup finish independently. `202 + runId` remains necessary
when startup/OCI pull does not produce data inside the HTTP fast-path budget.

### Targeting and identity

- Runtime node scoping is first-class: set `grpcruntime.ParamNode` to the Pod's
  `spec.nodeName`; the client connects only to that node's gadget Pod.
- Pod/container filtering uses `operator.KubeManager.namespace`, `podname`, and
  `containername`. A two-container snapshot returned both containers without a
  container filter and only the requested sidecar with one.
- IG has no runtime-container-ID or Pod-UID selector. Events do carry
  `runtime.containerId`; Radar must pin Pod UID/node/container ID before starting,
  discard nonmatching rows, and terminate partial if the Pod is recreated or moved.
- The embedded runtime should receive the already discovered gadget namespace.
  The CLI's convenience namespace discovery lists DaemonSets cluster-wide, but that
  permission is not needed by the runtime itself.

### Per-user RBAC

The live impersonation matrix passed:

1. an impersonated ServiceAccount with no grant failed before instrumentation;
2. granting only `get,list` on Pods plus `create` on `pods/portforward` in the
   `gadget` namespace allowed the same snapshot;
3. deleting the RoleBinding made the next request fail immediately.

No cluster-wide grant is needed for a run when Radar supplies the gadget namespace
and target node. Detection remains a separate service-identity operation. The test
Role and RoleBinding were removed after the matrix.

### Process, socket, and DNS shapes

- Process rows include comm, pid/tid, parent, credentials, Kubernetes metadata,
  owner, runtime container ID/image, and container start time. `snapshot_process`
  does not expose argv, so Phase 1 process output has no argv-redaction branch.
- Socket rows include protocol, addresses/ports, current numeric TCP state,
  container identity, and KubeIPResolver endpoint enrichment. The live capture
  identified both ends as Pods. TCP state must be mapped and fixture-tested by
  Radar; JSON emitted numeric values (`10` LISTEN and `5` FIN_WAIT2 in the capture).
- DNS rows already include query/response ID, qname/qtype, rcode, answer addresses,
  `latency_ns_raw`, nameserver, container ID, and Pod/Service-enriched endpoints.
  The v0.54.1 EKS capture emitted literal `qr` values `Q`/`R` and literal `rcode`
  values `Success`/`NameError` (DNS RCODE 3, commonly called NXDOMAIN);
  `pkg/igdebug/testdata/dns-v0.54.1.json` pins the released shape. Successful and
  nonexistent-name queries were captured with
  sub-millisecond latency.

Phase 1 should aggregate IG's response-side latency/rcode/answers rather than
recomputing correlation. Radar still owns unmatched-query accounting, event-loss
disclosure, truncation, capture metadata, and Kubernetes-cache correlation.

### Lifecycle and failure paths

- A node-scoped detached DNS instance appeared as Running on exactly one node and
  disappeared after explicit delete, validating instance teardown.
- A nonexistent OCI reference returned the full registry/pull error; preserve it
  through Radar's structured error envelope.
- The runtime's target discovery does not check Pod readiness before dialing. Radar
  must explicitly require a Ready gadget Pod on the target node.
- A requested node with no gadget Pod is rejected by the runtime.
- The local no-BTF error is explicit and actionable. Detection can still report
  `installed`; the first run should preserve this compatibility error rather than
  inventing a global healthy flag.

Allowed-gadget enforcement is performed after resolving the image and compares the
normalized tag/digest (or an allowed prefix). The upstream error includes both the
rejected reference and allowed list. Phase 1 should preserve it verbatim and link
the security-posture documentation.

## Artifact pins

Released v0.54.1 multi-platform manifest digests:

- `snapshot_process`: `sha256:a436639d529d8d8924d74801ad25832e0469d1bddf73c206a042c421ffa1cf1a`
- `snapshot_socket`: `sha256:85d76e100f8124d806eeebab043f0f2e4d9d116ff9683cc6b1f1b0018cc1d566`
- `trace_dns`: `sha256:24e93e604421ca095e4f9116c71514cc0d326cef89fb4c5d83a885bc985af935`

## Dependency and build gate

- Baseline Radar (`CGO_ENABLED=0`, darwin/arm64): 140,407,314 bytes.
- Radar linking `grpcruntime` v0.54.1: 141,278,210 bytes.
- Delta: **870,896 bytes (~0.83 MiB)**, well below the 25 MiB pivot threshold.
- Radar's Kubernetes modules remain at v0.36.2; IG does not force a downgrade.
- The alternate module file adds IG plus Viper/OpenTelemetry support dependencies
  and advances only indirect `x/exp` and `x/time` versions.
- Static CGO-disabled builds passed for linux/amd64, linux/arm64, and darwin/arm64.
- IG is Apache-2.0 licensed.

## Official MCP server evaluation

`ig-mcp-server` confirms the same embedded gRPC approach and is useful prior art,
but is not a reusable Phase 1 runtime layer:

- it registers generic per-gadget tools and returns truncated raw JSON strings;
- foreground runs use a caller-bound context and background runs use daemon-owned
  detached instances;
- it has no Radar run ownership, per-user impersonated config, Pod lifecycle guard,
  curated aggregation, K8s-cache correlation, or shared Radar result model;
- its current main branch still pins IG v0.50.0 and Kubernetes v0.35.3.

Align naming where useful (`gadget_trace_dns` concepts and explicit background
lifecycle), but keep Radar's single curated `inspect_pod_runtime` tool and isolate
the IG API behind `pkg/igdebug` interfaces.

## Phase 1 corrections

1. Use `SubscribeArray` as the snapshot boundary; remove the quiet-period path for
   the three pinned released gadgets.
2. Consume IG's DNS latency/rcode/answers and endpoint enrichment; do not duplicate
   response correlation.
3. Set `ParamNode` for real node-scoped execution and separately guard Pod
   UID/node/runtime-container-ID continuity.
4. Explicitly preflight the target gadget Pod's Ready condition.
5. Treat no-BTF as a run compatibility error; local kind is detection/error-state
   coverage, while live integration tests need a BTF-capable Linux runner/cluster.
6. Keep the official MCP server as alignment/prior art, not a dependency.
7. Use the existing `internal/k8s.ConfigFromContext` helper; do not create another
   impersonated REST-config path.
8. Count DNS duration from IG's post-start instrumentation window. Keep OCI pull
   and proxy setup under a separate startup deadline.
9. Add explicit tests for context-switch unload, snapshot data-ready before cleanup,
   total and per-user cap races, and Pod UID recreation.

## Maintainer questions (prepare; do not post)

1. Is one `TypeArray` packet a compatibility-guaranteed completion boundary for
   `snapshot_process` and `snapshot_socket`, or should clients inspect additional
   datasource annotations before canceling?
2. Is there a supported way to filter by runtime container ID or Pod UID, rather
   than Kubernetes names plus client-side lifecycle guards?
3. Is event sequence-gap reporting the supported measure of IG-side loss for the
   gRPC runtime, and can clients access it without replacing runtime internals?
4. Are numeric `snapshot_socket.state` values intentionally the stable JSON API,
   and is there a public mapping helper clients should use?
5. Does the project want downstream curated MCP tools to reuse naming/annotations
   from `ig-mcp-server`, even when they intentionally do not expose generic gadgets?
