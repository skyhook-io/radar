# Agent Substrate demo (fake control plane + worker Pods)

```bash
./scripts/substrate-demo.sh up        # fixtures + fake API (background) + TLS check
./scripts/substrate-demo.sh status    # is it up, what it serves, which fixtures exist
./scripts/substrate-demo.sh restart   # restart only the API: re-read Pod UIDs; writes the crash lines once the Pods are old enough
./scripts/substrate-demo.sh fault unavailable|unauthenticated|latency 2s|clear
./scripts/substrate-demo.sh down      # stop the API, delete the fixture namespace + SandboxConfigs
./scripts/substrate-demo.sh purge     # down, plus the Substrate CRDs if this fixture installed them
```

The cluster side defaults to kube context `radar-test-nonprod`. Override it
with `--context <ctx>` or `SUBSTRATE_DEMO_CONTEXT`. The namespace defaults to
`radar-substrate-demo`; override it with `--namespace <ns>` or
`SUBSTRATE_DEMO_NAMESPACE`. `up` remembers both, so later `down`/`status`/`restart`
go to the same place. The binary is built into the state directory, never the
worktree.

Open **Settings → Agent Substrate** for that kube context, then set the
Advanced endpoint overrides:

| Setting | Value |
|---|---|
| endpoint | `127.0.0.1:18443` (`SUBSTRATE_DEMO_PORT`) |
| caFile | `~/.radar/substrate-demo/ca.pem` |
| serverName | `api.ate-system.svc` |
| tokenFile | `~/.radar/substrate-demo/token` (global viewer) or `~/.radar/substrate-demo/token-limited` (atespace-only viewer: also set atespaces `code-review`, `support-bots`) |

The state directory (`SUBSTRATE_DEMO_STATE`, default `~/.radar/substrate-demo`)
holds the CA, server key, both tokens, the built binary, `pods.json`, the
generated manifests and `fakeapi.log`. It survives `down`, so Radar's settings
stay valid. Delete the directory to start over with a new CA and new tokens.

## What is real and what is fake

| | Real | Fake |
|---|---|---|
| Wire protocol | `substrate.ateapi.v1.Control` from Radar's vendored `internal/substrate/ateapi`, gRPC over TLS 1.3, `authorization: Bearer` | — |
| Server | — | `fakeapi/`, read RPCs only: ListActors, GetActor, ListActorTemplates, GetActorTemplate, ListWorkers, GetWorker, ListWorkerActorAssignments, ListAtespaces, GetAtespace, GetActorEgressPolicy, ListTags, GetTag. Every other RPC returns `Unimplemented`. gRPC reflection is on (authenticated), so `grpcurl` works. |
| Authorization | The two grants a person can hold in upstream's `model.fga` (global viewer; viewer of named atespaces) and ate-api-server's error wording (`permission denied: principal "user:…" lacks "can_list_actors" on "global:root"`) | The principals' names and which atespaces the second one holds |
| Data | Field meanings per `ateapi.proto`; crash texts, lifecycle messages and the golden-snapshot error format are Substrate's own strings | Every value. Deterministic: the same build always serves the same actors |
| CRDs `workerpools`, `sandboxconfigs`, `csidriverconfigs` (`ate.dev`) | Copied unchanged into `crds/` from Substrate `manifests/ate-install/generated` at commit `d283af4e` (the commit `internal/substrate/ateapi` is vendored from). `up` installs any that are missing. | — |
| WorkerPools `agent-pool`, `healthy-pool`, `batch-pool`, `microvm-pool`; SandboxConfigs `gvisor-default`, `microvm` | Kubernetes objects | Their `status` is written by `up` to agree with the fake Workers; no Substrate controller runs |
| Worker Pods | Real Pods (busybox) named and labelled like worker Pods (`ate.dev/worker-pool=<pool>`) | Their stdout is a script that prints Substrate-shaped log lines |
| Worker ↔ Pod binding | `Worker.metadata.name` and `worker_pod_uid` are the Pod's real UID, `node_name`/`ips` the Pod's real node and IP (read at API start) | When a Pod is missing or not scheduled, the Worker keeps its UID but gets a made-up node and IP |

### Timeline

The API serves the dataset as of the moment it is read (rebuilt at most every
5 seconds), so ages do not grow while it runs: "resuming for 4s" or "stuck for
17m" reads the same an hour after `restart` as right after it, and so do the
attention counts. The exception is the crash clock. Once the crashes are
written into the worker Pods' logs (see [A crashed actor's last
lines](#a-crashed-actors-last-lines)), every recorded crash keeps the time
those lines state and ages from then: the crash times of the 15 crashed and 2
reverting actors, `healthy-pool-worker-1`'s restart and `mcp-browser`'s golden
failure. Until then the crash clock is "now" too.

Both clocks are deliberate; don't "fix" either toward the other:

- **Relative to each read.** Anchoring to the API's start made every age
  grow with uptime: after two hours every actor read "2h ago" and every
  in-progress transition counted as stuck, so screenshots depended on when
  they were taken. Reading relative to the request keeps the counts and ages
  in this README true at any time.
- **Crashes pinned to the anchor.** A crash's last lines in a Pod's log carry
  real timestamps, and Radar matches a memory-limit kill to the crash time
  (within minutes). Sliding the crash with the clock would separate it from
  its own evidence. The anchor is the time those lines were written, kept in
  `crash-anchor` in the state directory with a key over the crashed actors'
  Pod UIDs and the crash timeline in `fakeapi/`.
- **What rewrites the anchor.** A `restart` or `up` that finds no anchor for
  the current key writes the lines and a new anchor, provided the Pods are
  old enough to hold every recent crash. Recreating any Pod a crashed actor
  last ran on, or retiming a crash in `fakeapi/`, changes the key. Restarting
  the API alone does not. Deleting `crash-anchor` also forces new lines, but
  on Pods that keep the old ones they then read as an earlier run, so
  recreate the Pods instead (`down`, `up`).

| Ago | What |
|---|---|
| 4s to 3m25s | Normal transitions, none stuck: RESUMING `codex-task-1050` 4s, `browser-sbx-014` 35s, `ticket-41290` 1m50s; SUSPENDING `ticket-41250` 9s, `pr-8829-reviewer` 2m40s, `ticket-41211` 3m25s; REVERTING `lit-review-0151` 40s, `pr-8701-reviewer` 2m10s; DELETING `browser-sbx-007` 1m05s. Also the `lit-baseline-wip` tag (3m, upload pending) and about 2% of suspended actors (under 4m, upload pending) |
| 7m40s | `agent-pool-worker-2` restarted (epoch 4, not yet reconciled) |
| 17m | `ticket-41302` stuck resuming |
| 21m to 36m, crash clock | The incident: 11 `langgraph-research` actors killed over their memory limit, one to two minutes apart. `lit-review-0151`, now reverting, crashed in it at 26m30s |
| 43m, crash clock | `healthy-pool-worker-1` restarted; `pr-8812`, `pr-8790` and `pr-8701-reviewer` crashed with it, the same second |
| 1h05m, 2h20m | `pr-8818-reviewer` stuck resuming; `ticket-40977` stuck suspending |
| 1h20m | `agent-pool-worker-0` unavailable |
| 2h50m | `ticket-40801`'s upload pending (over an hour, so critical) |
| 3h25m, crash clock | `ticket-41077` failed to restore |
| 5h10m, crash clock | `mcp-browser`'s golden actor crashed, 4 minutes after the template update that created it |
| 2m to 20 days | Running actors (2m to 6h), suspended actors (4m to 20 days), golden snapshots (2h to 5 days) |
| 12 to 13 days | The `triage-v1` actors' last update |
| days to weeks | Actors' creation (within their template's life), templates (3h to 34 days), atespaces (6 to 41 days) |

Anything that happened on a worker is capped at its Pod's age, since nothing
on a Pod predates it and a Worker is named after its Pod's UID: the worker's
create and state times, and the update times of the actors it holds. Right
after Pods are (re)created, "unavailable for 1h20m" or "suspending for 2h20m"
therefore read as the Pod's age, and grow to their designed values as the Pods
age. A draining worker's state time is its Pod's drain start (its deletion
deadline less the grace period), and the actors it holds were placed before
it. `up` keeps the Pods unless their script changed; `restart` keeps them
always. Crash times are not capped: a crashed actor has no worker.

## The data

Four atespaces: `support-bots`, `code-review`, `research`, `sandbox-dev`. 1,293
actors (1,287 ordinary, 4 of a deleted template, 2 golden actors) in this state
mix: Suspended 1,185, Running 81, Resuming 5, Suspending 4, Reverting 2,
Deleting 1, Crashed 15.

| Template | Atespace | Actors | Selector → pools | Limits | Snapshots | Golden |
|---|---|---|---|---|---|---|
| `adk-support-agent` | support-bots | 611 | none → every pool | 0.5 CPU, 1Gi | memory | ready, 5d |
| `claude-code-gvisor` | code-review | 430 | none → every pool | 1 CPU, 1.5Gi | memory | ready, 2h |
| `langgraph-research` | research | 160 | none → every pool | 2 CPU, 2Gi | memory | ready, 2d |
| `codex-microvm` | code-review | 56 | `sandbox=microvm` → microvm-pool | 4 CPU, 8Gi | memory | ready, 1d |
| `mcp-browser` | sandbox-dev | 15 (+golden) | `sandbox=gvisor` → batch-pool | 1 CPU, 3Gi | rootfs | **failed** |
| `notebook-runner` | research | 12 | `tier=batch` → batch-pool | 2 CPU, 4Gi | volumes | disabled |
| `gpu-eval` | research | 3 (+golden) | `accelerator=nvidia-l4` → **no pool** | 4 CPU, 16Gi | — | none yet |
| `triage-v1` (**deleted**) | support-bots | 4 | — | — | memory | — |

A template with no selector matches every pool, `microvm-pool` included.
Substrate allows that: an actor with no snapshot takes the class of whichever
pool it lands on. `agent-pool` and `healthy-pool` carry only the fixture
label, so only selector-less templates match them.

Workers (gVisor ones: 16 CPU, 64Gi, 24 actor slots; microvm: 32 CPU, 96Gi, 8
slots). Allocated is the sum of each assigned actor's limits plus the runtime
overhead (gVisor 48Mi per actor; microvm 96Mi plus 8Mi per GiB of limit). It
matches ListWorkerActorAssignments.

| Pod | Pool | State | Slots |
|---|---|---|---|
| `agent-pool-worker-0` (Pod Running, not Ready) | agent-pool | UNAVAILABLE for 1h20m | 3/24 |
| `agent-pool-worker-1` | agent-pool | ACTIVE | 18/24 |
| `agent-pool-worker-2` | agent-pool | ACTIVE, registered epoch 4 > observed 3 | 13/24 |
| `agent-pool-worker-3` (Pod Terminating) | agent-pool | DRAINING | 4/24 |
| `healthy-pool-worker-0` | healthy-pool | ACTIVE, full | 24/24 |
| `healthy-pool-worker-1` | healthy-pool | ACTIVE, epoch 2 (restarted, reconciled) | 14/24 |
| `batch-pool-worker-0` | batch-pool | ACTIVE | 8/24 |
| `microvm-pool-worker-0` | microvm-pool | ACTIVE | 6/8 |

WorkerPool status as written by `up`:

| Pool | Desired | Replicas | Ready | Note |
|---|---|---|---|---|
| `agent-pool` | 5 | 4 | 3 | short of workers: one Pod missing, `agent-pool-worker-0` not Ready (so Substrate marks it unavailable), and `agent-pool-worker-3` Terminating but still Ready |
| `healthy-pool` | 2 | 2 | 2 | |
| `batch-pool` | 1 | 1 | 1 | |
| `microvm-pool` | 1 | 1 | 1 | resolves SandboxConfig `microvm` |

## Scenarios (needs attention)

Names are `atespace/name`. A golden actor's name is its template's UID, as in
Substrate.

1. **Crash cluster from one template.** 11 `research/langgraph-research`
   actors crashed in one incident, within 15 minutes, with `killed for
   exceeding its memory limit` (the worker's wording for a memory-limit kill):
   `lit-review-0193` (21m10s, newest), `-0188` 22m05s, `-0175` 23m40s, `-0170`
   24m20s, `-0166` 25m50s, `-0159` 27m15s, `-0147` 28m30s, `-0139` 30m05s,
   `-0122` 31m45s, `-0108` 33m20s, `-0096` 35m50s. Each has
   `crash.message`, `crash_time`, its newest snapshot (memory, durable
   COMPLETED, locality = last node), `assigned_node`, and no
   `worker_assignment`. Two of them hold a newer generation a revert
   discards: `lit-review-0175` has one PENDING with locality (a finalized
   async suspend whose upload may still finish: `recovery.discarded[].why =
   uploadPending`), and `lit-review-0188` one PENDING with no locality (a
   failed synchronous suspend, whose upload Substrate never records:
   `uploadAbandoned`). For both, the recovery source is the older COMPLETED
   generation, and so is the resume source of `-0188`.
2. **Crashes from a worker restart.** `code-review/pr-8812-reviewer`,
   `code-review/pr-8790-reviewer`: `worker restarted while hosting the actor`,
   43m ago, the same second (a burst of identical times, on purpose). Their
   last worker is `healthy-pool-worker-1` (epoch 2, restarted 20 seconds
   earlier and already reconciled). The third actor it lost,
   `code-review/pr-8701-reviewer`, is being reverted (14).
3. **One-off crash.** `support-bots/ticket-41077`: `atelet Restore: while
   restoring "agent" application container: container "agent" exited with status
   1 during restore` (Substrate's `atelet <RPC>: <error>` form; the error text
   itself is illustrative), 3h25m ago and never reverted.
4. **Golden snapshot failed.** `sandbox-dev/mcp-browser`:
   `status.golden_snapshot_status.error_message` = `GoldenActorCrashed: golden
   actor crashed before its snapshot was taken: killed for exceeding its memory
   limit`, no `golden_tag`. Its golden actor `sandbox-dev/<mcp-browser UID>` is
   CRASHED (5h10m), 4 minutes after the template update that created it. New
   actors of this template boot cold.
5. **Template that matches no pool.** `research/gpu-eval` selects
   `accelerator=nvidia-l4` and no pool carries it. Its golden status is empty
   (warm-up never started), its golden actor is SUSPENDED with no snapshot, and
   `gpu-eval-01..03` were never resumed.
6. **Golden disabled.** `research/notebook-runner`
   (`golden_snapshot_config.mode = DISABLED`, volumes fidelity).
7. **Unavailable worker with actors still bound.** `agent-pool-worker-0`: UNAVAILABLE since
   1h20m ago (update_time). Its Pod runs but is not Ready: a readiness probe that
   always fails (`exec: false`) stands in for a worker that stopped passing its
   own, and Substrate marks a worker UNAVAILABLE while its Pod is not Ready. It
   still hosts `support-bots/ticket-41001`, `support-bots/ticket-41002` and
   `code-review/pr-8801-reviewer` (RUNNING since before that), whose logs keep
   flowing.
8. **Draining worker with actors still assigned.** `agent-pool-worker-3`, Pod
   Terminating with a 24h grace period, so the row is `info`: draining stops
   new placements and moves nothing, and the Pod's deadline
   (`deletionTimestamp`) is a day out. DRAINING since the Pod's deletion
   began. RUNNING `support-bots/ticket-41210`, `research/lit-review-0197`
   (both placed before the drain); SUSPENDING `support-bots/ticket-41211`
   (3m25s), `code-review/pr-8829-reviewer` (2m40s).
9. **Registration not yet reconciled.** `agent-pool-worker-2`, restarted
   7m40s ago: `registered_epoch` 4, `observed_epoch` 3, issued 4.
   `support-bots/ticket-41120`, `support-bots/ticket-41121` and
   `code-review/pr-8822-reviewer` still read RUNNING with
   `worker_assignment.worker_epoch` 3. They ran under the replaced worker
   process, so the Pod prints nothing for them.
10. **Stuck transitions** (old `update_time`, still holding a worker):
    RESUMING `support-bots/ticket-41302` 17m on `agent-pool-worker-1` and
    `code-review/pr-8818-reviewer` 1h05m on `healthy-pool-worker-0`;
    SUSPENDING `support-bots/ticket-40977` 2h20m on `agent-pool-worker-1`.
    These three are the only stuck actors, however long the API runs. For
    contrast, these transitions are normal and recent: RESUMING
    `code-review/codex-task-1050` (4s), `sandbox-dev/browser-sbx-014` (35s),
    `support-bots/ticket-41290` (1m50s); SUSPENDING
    `support-bots/ticket-41250` (9s).
11. **Upload stuck.** `support-bots/ticket-40801`: SUSPENDED 2h50m ago, newest
    snapshot durable state still PENDING, locality set; over an hour, so
    critical. It is the only overdue upload. About 2% of other
    suspended actors were suspended under 4 minutes ago and are legitimately
    PENDING.
12. **Memory snapshots no worker can restore.** Every Worker's default runtime
    is the `current` bundle hash, and it can also restore the `previous` one.
    About 10% of suspended actors (suspended more than 3 days ago) carry a
    `retired` `sandbox_bundle_hash` that no worker reports. Their memory
    snapshots read `resumesWith: filesystem`, `resumeCondition:
    runtimeMismatch`: Substrate relaxes the runtime and restores the
    filesystem. Every other memory snapshot reads `memory` with
    `compatibleRuntime`, since where it lands decides. About 8% of
    memory-fidelity snapshots were taken at rootfs (the fidelity setting is a
    ceiling, not a guarantee).
13. **Full worker.** `healthy-pool-worker-0`, 24 of 24 slots.
14. **In-flight lifecycle.** REVERTING `research/lit-review-0151` (40s; it
    crashed in the incident of 1) and `code-review/pr-8701-reviewer` (2m10s;
    lost in the restart of 2), their crashes still recorded, as Substrate keeps
    them until the revert lands; DELETING `sandbox-dev/browser-sbx-007` (1m05s,
    `delete_time` set).
15. **Egress.** support-bots actors: HTTPS to `api.openai.com` and
    `*.zendesk.com` with credential header replacement. code-review: GitHub
    with an actor JWT, TLS passthrough to `*.githubusercontent.com:443`, and
    cleartext HTTP to `proxy.golang.org`. sandbox-dev: `https *` on all ports
    (deliberately wide). research actors have none (`NotFound`).
16. **Template that no longer exists.** `support-bots/triage-legacy-01..04`
    name `support-bots/triage-v1`, which the API does not hold (Substrate
    deletes a template's golden actor and tag, not its actors). Last updated
    12 to 13 days ago: a resume fails before it changes them. On a complete
    template read Radar raises `missingTemplate`; with the atespace-only token
    it cannot, since the read is not complete.
17. **Tags.** One golden tag per ready template (name = template UID,
    PUBLISHED), plus `code-review/repo-warm-baseline` (seeds
    `pr-8401..8406-reviewer` via `source_tag`), `support-bots/kb-loaded-2026-11`
    (seeds `ticket-41300..41305`), and `research/lit-baseline-wip` (snapshot
    still PENDING).

`fakeapi scenarios --pods ~/.radar/substrate-demo/pods.json` prints every
Worker, its name (the Pod UID) and every actor that isn't plainly RUNNING or
SUSPENDED.

## Tokens

| Token | Principal | Can |
|---|---|---|
| `token` | `system:serviceaccount:ate-system:ate-client` | everything (global viewer) |
| `token-limited` | `demo-atespace-viewer` | viewer of `code-review` and `support-bots` only, the other grant `model.fga` gives a person: actors, templates, tags, egress policies and GetAtespace there. ListAtespaces, ListWorkers, GetWorker, ListWorkerActorAssignments, every cross-atespace list, and anything in `research` or `sandbox-dev` are `PermissionDenied`. The client cannot discover atespaces, so Radar's settings must name them; workers, placement and capacity stay unknown while WorkerPools still read from Kubernetes. |

Global viewer is the only grant behind the global lists, and an atespace grant
cannot subtract one atespace from it, so "everything but research" is not a
persona upstream can express.

A missing token gets `Unauthenticated: missing bearer token`, a wrong one
`invalid bearer token`. Page tokens are bound to the list they came from, and
replaying one elsewhere is `InvalidArgument`. `page_size` defaults to 100 and is
capped at 1000.

## Faults

`fault` writes `~/.radar/substrate-demo/fault`, which the server reads on every
call: `unavailable` (every call `Unavailable`), `unauthenticated` (every call
`Unauthenticated`, like an expired token), `latency 3s` (each call waits; a
client deadline shorter than that gets `DeadlineExceeded`). `fault clear`
removes it. The `serve` flags `--latency` and `--unavailable` set the same
faults at startup.

## Worker Pod logs

Each fixture Pod runs `sh` from the `substrate-demo-worker-logs` ConfigMap. A
Pod holds only what happened on it, and these Pods are new, so nothing is
backdated: at start a Pod prints the lifecycle events of the actors it holds
now, at the current time, then one line of their output every 4s. Actors that
ran on another worker, crashed, or ran under an earlier worker process
(`agent-pool-worker-2`'s epoch-3 actors) print nothing here; Radar reads only
the current worker's Pod, so their history is not on any Pod it reads. The
lines take the shapes Substrate's worker emits on stdout:

- **Lifecycle events** (`EmitLifecycleLog`): `{"labels":{ate.atespace,
  ate.actor.name, ate.actor.uid, ate.template.atespace, ate.template.name},
  "message":"Actor restoring|restored|starting|started|checkpointing", "time":…}`,
  sometimes with `trace_id`/`span_id`/`trace_flags`. No container name key.
  That missing key is what marks a lifecycle line. A resuming actor shows only
  `restoring`; a suspending one ends at `checkpointing`.
- **Container output** (`WrapContainerLogs`): the app's JSON fields (or
  `message` for plain text) plus `labels` including `ate.actor.container.name`.
  `_sandbox` lines are runsc diagnostics, which kubectl-ate hides by default.
- **The worker's own slog lines**: `Worker process registered` on a worker
  whose epoch is above 1, and `Actor usage sampled` with flat `ate.*` keys and
  no `labels`. kubectl-ate does not attribute those to an actor, and a reader
  should not either.

The plain `labels` key is what Substrate uses off GCE (this cluster is EKS).
The wording of the worker's own lines (`Worker process registered`, the SIGTERM
line on the draining Pod) is illustrative, not copied from Substrate. Traffic
is far lighter than a real worker's: one line per Pod every 4 seconds, so the
newest 20,000 lines Radar scans span about a day here and minutes on a busy
worker.

### A crashed actor's last lines

A crash clears the actor's worker, but the worker Pod that ran it keeps its
log. When the API starts on a set of Pods that ran crashed actors, `restart`
(and `up`) appends each such actor's last lines to that Pod's stdout with
`kubectl exec`, once per set of Pods and timeline. Only crashes that happened
after the Pod started get lines, so nothing is written until the Pods are old
enough to hold every recent crash (the incident and the worker restart, up to
43m ago): about 46 minutes. Until then `restart` says how long is left, and the
crash clock is "now". Writing them sooner would leave the rest without lines
for as long as those Pods live. Older crashes get lines only if the Pods are
older still when they are written: `ticket-41077` needs about 3h30m,
`mcp-browser`'s golden actor about 5h15m.

- Memory-limit crashes (`langgraph-research`): `Actor restoring` /
  `restored`, the actor's output, then the worker's own lines: `Actor usage
  sampled` with memory climbing to the limit, `Killing a sandbox over its
  memory limit` (with `memoryLimitBytes`), and `Sandbox died` (with
  `actorUID`), as gVisor's ateworker logs them.
- Worker-restart crashes (`pr-8812-reviewer`, `pr-8790-reviewer` on
  `healthy-pool-worker-1`): the run's lines stop when the worker process
  died.
- `ticket-41077`: only `Actor restoring`, since the restore failed.

The API then serves the crash times those lines state, from
`crash-anchor` in the state directory, on every later start: the crash clock
(see [Timeline](#timeline)). Each crashed actor's `assignedNode` and snapshot
locality are that Pod's node. Recreating the Pods starts a new story. So does
retiming a crash in `fakeapi/`; on Pods that keep the old lines, those then
read as an earlier run of the same actor, so recreate the Pods (`down`, `up`)
for a clean log.

## Prometheus

The fixture serves no Substrate metrics. With Prometheus connected, the
workspace's service readings (pool resume p95, failed resumes, crashes,
capacity verdicts; node upload verdicts) read `unknown` with the reason each
query gave; without it, `checks` lists the Prometheus checks as not run.

## Cluster footprint

`up` creates these objects, all labelled `radar.skyhook.io/fixture=substrate-demo`:

- The three `ate.dev` CRDs, only if missing. When they already exist without the
  label, they are used as they are and never changed.
- Namespace `radar-substrate-demo`, holding ConfigMap
  `substrate-demo-worker-logs`, the four WorkerPools and 8 busybox Pods
  (`agent-pool-worker-0..3`, `healthy-pool-worker-0..1`, `batch-pool-worker-0`,
  `microvm-pool-worker-0`; 2m CPU / 8Mi requests each).
- Cluster-scoped SandboxConfigs `gvisor-default` and `microvm`.

`up` treats `agent-pool-worker-0` being Running but not Ready as intended. It
recreates a Pod when its script or its readiness probe changes. Recreating a
Pod no crashed actor ran on leaves the crash lines alone.

`up` refuses to touch a same-named namespace, SandboxConfig or Pod without the
label. `down` deletes the namespace and the labelled SandboxConfigs. A
Terminating fixture Pod is force-deleted first: the kubelet does not shorten a
grace period already running, and the draining Pod's would hold the namespace
open for a day.

If a Pod can't be scheduled, `up` warns instead of failing. The Worker keeps the
Pod's UID but gets a made-up node (`unscheduled-node-*`) and IP and prints no
logs, and the draining Pod is not deleted. Once the Pods run, re-run `up`.
