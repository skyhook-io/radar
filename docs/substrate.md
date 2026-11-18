# Agent Substrate workspace

A view over [Agent Substrate](https://github.com/agent-substrate/substrate): which actors are not running as they should, why, and whether the workers and pools have room for them. Substrate keeps actors, actor templates, workers and atespaces in its own API server (PostgreSQL behind gRPC), not in Kubernetes. Kubernetes holds only the WorkerPools, their worker Pods and the SandboxConfigs. The workspace joins the two, and says which side each value came from. The per-kind readings it builds on (WorkerPool status, Issues, charts) are described in [integrations.md](integrations.md#agent-runtimes-agent-sandbox-and-agent-substrate).

Reading never writes. Radar does not create, suspend, resume or delete actors.

## Code boundaries

- `pkg/substrate` is the read model and every derivation: actor rows, resume source, pool joins and capacity, template rows, eligible pools, log filtering and the needs-attention rules. It is pure (no gRPC, no `internal/` imports), so Radar Hub can reuse it. Its types are also the wire shapes of `/api/substrate/*`. What an omitted field means depends on the field: an omitted object or measurement is unknown, never zero; an omitted list or map on something Substrate returned is empty (no snapshots; a selector that matches every pool); an omitted boolean is false; and a list a response builds from reads is `[]` when read and empty, absent when not read.
- `internal/substrate` is the client: the connection settings store, the dial (port-forward or endpoint, TLS, tokens), one snapshot read shared by every request, conversion from Substrate's API types, and error codes. `internal/substrate/ateapi` is Substrate's generated v1 API, vendored.
- `internal/server/substrate_workspace_handlers.go` serves the routes and decides who may read what ([API](#api)).
- `web/src/api/substrateWorkspace.ts` holds fetching and wire types; `web/src/components/substrate/` the screens. `host.tsx` composes the WorkerPool kind into Radar's generic views; `workspaceRoute.ts` the route metadata.

## Where it lives

When the `ate.dev` CRDs are discovered, the Resources sidebar's **Integrations** block lists Substrate as **Agent actors**, with "Agent Substrate" beneath and the number of attention items on the row. Each destination's badge counts the objects there that need attention: actors crashed or stuck in a transition, and the templates, workers and pools a Needs attention row names. Opened, it lists these destinations; SandboxConfig and CSIDriverConfig stay under **Other kinds**.

| Destination | Route | Answers |
|---|---|---|
| Overview | `/substrate` | Is Substrate working: a status line (pools short or full, workers by state, actor slots, actors and how many crashed, templates whose golden snapshot failed or that no pool can run), **Needs attention**, actor states, the worker pools and the atespaces |
| Actors | `/substrate/actors` | Every actor, filtered by state, atespace, template, pool, worker or name, paged by the server. State counts come from the full read, not the page |
| Templates | `/substrate/templates` | Each template's golden snapshot, its actors by state, and the pools that can run it |
| Workers | `/substrate/workers` | Substrate's workers in every pool: state, room left, the actors on each, and whether the worker Pod is Ready |
| Pools | `/resources/workerpools?apiGroup=ate.dev` | The WorkerPool kind's list is Radar's pool table (the Overview's), joining each WorkerPool with Substrate's workers |

Badges leave out `info` rows (a drain still within its deadline). A badge whose rules could not be evaluated, because their inputs were not read, reads as a lower bound (≥N), and its zero as unknown.

**Pages.**

| Page | Route | Holds |
|---|---|---|
| Actor | `/substrate/actors/<atespace>/<name>` | Overview (Substrate's crash message and what a revert keeps and drops, or Radar's reading, first; then its facts; its last lines from its current worker when it has one, otherwise how to find its earlier lines; links), Snapshots, Logs (its current worker Pod's log: container output and lifecycle events), Egress, Spec |
| Template | `/substrate/templates/<atespace>/<name>` | Golden snapshot, the read-only spec (images, env with secret values redacted, volumes, snapshot settings), the template's actors by state, the pools matching its selector |
| WorkerPool | `/substrate/workerpools/<namespace>/<name>` | Overview: the Prometheus capacity verdict, placement (which actor holds which slot, per worker), sandbox classes and the templates whose selector matches the pool; the WorkerPool's own renderer under "Spec & status"; a Workers tab. "Open full view" and links to the generic WorkerPool detail land here |

## Navigation

- **Two drawers.** Kubernetes objects (WorkerPools, worker Pods, Nodes) open in the app's resource drawer (`?drawer=`). Actors are not Kubernetes objects: they open in Substrate's own drawer (`?actor=<atespace>/<name>`), which shows the actor page's Overview with **Open full view**. One drawer is open at a time.
- **Links.** A Needs attention row links to its subject: an actor list narrowed to the actors it covers (`/substrate/actors?state=crashed`), a template's page, a worker's Pod, its pool or its node.
- **Context.** Page URLs carry `ctx=<kube context>`, kept across a context switch, so an actor of the same name in another cluster never opens.
- **Namespace filter.** It does not apply: actors live in atespaces, which are Substrate's tenancy, not namespaces. The header's picker is disabled with a tooltip on every Substrate screen and on the Pools list. The Actors screen has an Atespace filter instead.

## Three sources

The Overview and each list show a source line: Substrate's API (connected or why not, who Radar reads as, the endpoint, how old the read is), Prometheus (connected or not), and Kubernetes (always).

| Source | What it holds | Read as | Where |
|---|---|---|---|
| Kubernetes | WorkerPools (desired and ready workers, sandbox classes, Radar's WorkerPool verdict), worker Pod readiness, Node readiness | WorkerPools as the caller, through the workspace coverage machinery; Nodes only when the caller may list them; Pod readiness from Radar's cache, shown only beside the API's workers | Everywhere Radar runs |
| Substrate's API | Actors, templates, workers, atespaces, an actor's egress policy | One identity, chosen per kube context ([Connecting](#connecting-to-substrates-api)) | Only a local Radar on a private listener |
| Prometheus | Charts and hourly facts on the WorkerPool and Node pages | Radar's Prometheus connection | Wherever Prometheus is connected ([integrations.md](integrations.md#agent-runtimes-agent-sandbox-and-agent-substrate)) |

Without the API, the workspace still shows pools from Kubernetes ("3 ready of 5, worker Pods, from Kubernetes"), says actors, templates and workers are kept in Substrate's API, and offers to connect when this Radar can.

### Where each value comes from

| Value | Source | When it can't be read |
|---|---|---|
| Actor state, template, assignment (worker, pool, Pod, node, epoch), crash message and time, snapshots | `ListActors` in the shared read; `GetActor`, fresh, on an actor's page and drawer | "Not connected" with the reason. A partial read (atespaces denied, read only through the settings' scope, or the cap reached) says so, and counts over it are lower bounds |
| How long a state has held | The actor's or worker's last update time. Substrate records no transition time, but every state change updates the object, so this is a lower bound: "at least 12m". A worker row's `heldForSeconds` is absent when Substrate reports no update time | Absent when Substrate reports no update time |
| What a resume restores (`resumesWith`, `resumeCondition`) | The newest snapshot with local files or a completed durable copy. Filesystem or volumes by its fidelity (a `rootfs` snapshot restores the filesystem); volumes only when it was captured under a template UID the actor no longer runs. A `memory` snapshot is qualified by runtime, as Substrate decides it: Substrate restores memory only on a worker whose runtime can restore the snapshot (same class and schema, matching attributes, CPU features a subset of the worker's), and otherwise places the actor on another runtime of the class and restores the filesystem. So a resuming actor reads `memory` or `filesystem` with `runtimeMismatch` from the worker it was placed on; otherwise it reads `filesystem` with `runtimeMismatch` when no active worker of its class can restore it, else `memory` with `compatibleRuntime` | `unknown` when the template's UID was not read, or the snapshot names no fidelity. Without the worker list, a memory snapshot reads `memory` with `compatibleRuntime` |
| Golden snapshot | The template's golden status: ready (with its tag), failed (terminal, with Substrate's message), building, or disabled. A building snapshot that no pool can run reads `blocked: noMatchingPool`: Substrate retries it without end and never fails it | The template list was not read |
| Pools that can run a template (`matchingPools`) | Substrate's equality-only worker selector against every pool Radar knows: WorkerPools' labels, else the labels Substrate copies onto that pool's workers. An empty selector matches every pool | `[]` (none) only with `matchingPoolsComplete`, when every WorkerPool was read. `null` when Radar cannot say: no pool list was read, or none of a partial list matches. Otherwise the pools found, which on a partial list may not be all |
| Worker state, capacity, allocated, epochs, sandbox classes | `ListWorkers` | "Not connected" or the coverage reason |
| A free actor slot on a worker (`hasFreeSlot`) | Actors allocated below the worker's actor capacity, the dimension Substrate's own capacity metrics count. Substrate treats an unreported dimension as zero, so no capacity is no slot. A free slot does not mean a given actor fits: placement also books its CPU and memory, runtime overhead included | — |
| Worker Pod Ready | Radar's Pod cache | Absent when the Pod is not cached |
| WorkerPool desired, ready, verdict | Radar's WorkerPool cache, read as the caller | The pools coverage names the namespaces not read; never "no pools" |
| Node Ready (for the upload rule) | Radar's Node cache, when the caller may list Nodes | The node's state is unknown, and the row does not escalate on it |
| Atespaces | `ListAtespaces`, else the settings' scoped atespaces. Each row says how much of its actors and templates was read (`actorsRead`, `templatesRead`: `full`, `partial`, `denied` or `unread`), with counts only when full or partial | The list is absent when atespaces were neither listed nor scoped; atespaces outside the scope stay unknown |
| An actor's log lines | The assigned worker Pod's log, filtered by atespace and actor name as `kubectl ate logs actor` filters it (`origin: worker`): the actor's container output and lifecycle events, and, as source `worker`, the worker process's own records about the actor, matched by its UID, which `kubectl ate` does not show. The newest memory-limit kill among them is returned as `memoryKill` (time, Pod, limit and usage bytes), with `current` when it is tied to the actor's present crash (at most 10 minutes before it, and no start or restore of the actor between them in the lines read); otherwise it is an earlier execution's record, not the crash's cause. Each line names its `kind` and `pod`. An actor with no worker (suspended or crashed) is looked for on the node it last ran on, in up to 6 worker Pods there (`origin: lastNode`). Each Pod read covers its newest 20,000 lines | `origin: none` with a reason code: `noWorker`, `nodeGone`, `noWorkerPods`, `workerPodGone`, `denied`, `noMatch`. `windowFull` says older lines may exist |
| Egress | `GetActorEgressPolicy`: the rules' destinations, not the whole policy | "None" when Substrate has no policy; the error when the read failed |

**Coverage and budgets.** Each API kind carries a coverage: `full` (every page of every scope read), `partial`, `denied` or `failed`, with a `code` unless full (`denied`, `scoped`, `capExceeded`, `unreachable`, `unauthenticated`, `failed`). A snapshot reads at most 50,000 actors and 10,000 of each other kind, across every atespace, in pages of 1,000. Past that budget the kind is partial with code `capExceeded`, holds what was read, and names the atespaces the budget left unread in `unreadAtespaces`, never a silent truncation. Read atespace by atespace, it also names `readAtespaces` and `deniedAtespaces`. A read's pages are not one point in time: an actor created or deleted during it may be missed, and Radar keeps the newest entry it saw for each actor.

**Freshness.** One read of every kind serves all requests for 5 seconds after it completes. After that, a request starts a new read and, while it runs, is answered from the last good read for up to 2 minutes after that read completed: the tier says `refreshing: true`, and `lastError` (`code`, `message`, `at`) when the newest read failed. Past 2 minutes a failed read is the answer. `readStartedAt` and `readAt` bound the read the counts come from. With nothing usable, a request waits for the read, except `/status`, which answers `connecting` instead. A context switch or a settings save discards every read and cancels any still running under the old connection.

## Connecting to Substrate's API

**Who reads the API.** Substrate authorizes by its own roles, not Kubernetes RBAC, and Radar reads it with one identity. A Radar several people use would show each of them everything that identity sees. So the API is read, per request (`substrateGate`), in two places:

- **A local Radar** (CLI or Desktop) with authentication off and a loopback listener, with the identity chosen in its connection settings.
- **Radar Hub, for org owners.** A request whose Hub role is `radar:owner` reads as `system:serviceaccount:ate-system:ate-client`, the identity `kubectl ate` uses without `--token-file`, with identity mode `sharedAsCaller`. Radar mints its token (a TokenRequest, audience `api.ate-system.svc`, one hour) with the owner's own Kubernetes access, impersonated, or the user's token in user-token mode, never with Radar's ServiceAccount. So Kubernetes lets Radar read Substrate for the owner only because the owner could do the same with `kubectl ate`. Nothing is chosen or saved, which Hub's GET-only proxy requires: Radar runs in the cluster and dials `api.ate-system.svc:443` directly, verifying TLS against the live ClusterTrustBundle its ServiceAccount reads. Each owner's own Kubernetes access must be able to create a token for `ate-system/ate-client`, and Radar checks it at least hourly per owner: an owner is served only while a TokenRequest made with their own client succeeded within the last 50 minutes, so no owner reads on another's access. An owner whose access cannot mint gets 403 `mintDenied`, even while another owner's token serves. The reads share one token and one connection, the newest minted, replaced on the first owner request within 10 minutes of its expiry; a background refresh never mints. The cost is one TokenRequest per owner per hour, which the Kubernetes audit log records as that user (impersonated by Radar's ServiceAccount). The token is never logged or returned, and the token and the owners' proofs are dropped on a context switch. Caveat: `ate-client` is usually a global owner in Substrate, so this reads everything Substrate holds. Radar only ever calls Substrate's read RPCs, pinned by a test (`TestRadarCallsOnlyReadRPCs`).

Everyone else is refused before anything is minted or read: in Radar Hub, a member, a viewer or a request with no Hub role gets 403 `ownersOnly`; any other shared Radar (in-cluster, authentication on, a listener beyond loopback) gets 403 `sharedRadar`. Those Radars show the Kubernetes and Prometheus sources only. What other Hub roles and shared modes should see is pending a decision. The connection settings (`GET`/`PUT /connection`) stay local-only. An older Radar under a newer UI answers `sharedRadar` as before, so the UI needs no new feature flag.

**Identity.** The Connect panel asks who Radar reads Substrate as:

- **Shared** (`shared`): Radar mints tokens for `ate-system/ate-client`, as `kubectl ate` does without `--token-file` (audience `api.ate-system.svc`, one hour, re-minted 10 minutes before expiry). Everyone who can create a token for that ServiceAccount is the same principal, `system:serviceaccount:ate-system:ate-client`. Minting needs `create serviceaccounts/token` in `ate-system`.
- **Token file** (`tokenFile`): Radar sends the bearer token in a local file, read on every call, so a rotated token is picked up without reconnecting. Substrate does not say who a token authenticates as, so the source line names only "a token file".
- **Atespaces** (optional): for a token that may read some atespaces but not list them. Radar reads the named atespaces (up to 64) one by one; the rest stay unknown. When Substrate lists atespaces, the list is ignored.

When Substrate denies a list across atespaces, Radar lists the atespaces it may see and reads each one, eight at a time. Atespaces it is refused are named in the coverage, and the kind is partial with code `denied`.

**Endpoint.** By default Radar port-forwards to Service `ate-system/api` port 443 (resolving a named target port as kubectl does), and checks the server's certificate against the cluster's live ClusterTrustBundles for Substrate's signer (`servicedns.podcert.ate.dev/identity`, label `podcert.ate.dev/canarying=live`) and the name `api.ate-system.svc`. Under **Advanced**:

- **Endpoint** (`host:port`) dials directly instead of port-forwarding.
- **CA file** (PEM) verifies the endpoint instead of the trust bundles.
- **Server name** overrides the TLS name checked.

CA file and server name apply only with an endpoint. Connections use TLS 1.3, and the token is sent only after the handshake verifies the server. Error text never carries the token.

**Where settings live.** `~/.radar/substrate.json`, next to `clusters.json`, keyed by the kube context's binding. It holds paths, never a token. Each entry records the cluster it was saved for; when the context later points at another cluster, the settings are not used (code `targetChanged`) until saved again. A context with no entry is `notConfigured`. See [configuration.md](configuration.md#agent-substrate-connection).

**Connection states.** The API tier is `connected` (a snapshot was read; `refreshing` and `lastError` say when it is an older one served while a refresh runs or after one failed), `connecting` (settings saved, first read still running; `/status` never waits for it), `notConfigured` (code `notConfigured`), `disabled` (a shared Radar; code `sharedRadar`), or `failed` with a code: `targetChanged`, `contextChanged` (the context or settings changed during the read; safe to retry), `notInstalled` (no Service `ate-system/api`), `tls`, `unreachable`, `unauthenticated`, `settingsFile` (`~/.radar/substrate.json` unreadable) or `failed`. `connected` and `connecting` carry no code.

## Needs attention

`pkg/substrate.NeedsAttention` applies every rule to one read. A rule runs only when the lists it needs were read, at least in part; a rule whose inputs were not read is skipped, never read as "none". The workspace also returns `checks`, each rule with whether it ran and why not, and `notAssessed`, the health surfaces no rule reads (API operation errors, resumes that fail before placement, request routing and egress, worker log collection), so an empty list says what was checked.

Severities are `critical`, `warning` and `info`, an expected state still within its budget, shown so the operator knows it is in progress. Rows are ordered by severity, then by rule in the table's order (what already broke actors first, what may lose state last), then newest `since` first. A row's `since` is the newest change among what it covers, a lower bound on how long the condition has held; the thresholds below are measured the same way.

| Rule | Row | Severity | Needs |
|---|---|---|---|
| `crashedActors` | One row for every crashed actor, grouped by template and by crash message, naming the latest. A template group whose crashes are all memory-limit kills (the worker's "killed for exceeding its memory limit") carries `cause: memoryLimit`; when every crash is one, the step names the remedy: create a template with a higher memory limit, move the actors to it (`kubectl ate update actor --template`, since a template can't be edited) and revert them. A snapshot restores memory only under the template it was taken under, so they resume from their volumes | Critical | Actors |
| `noMatchingPool` | A template whose worker selector matches no known pool. Says too when its golden snapshot cannot be built for the same reason | Warning; critical when any of its actors is not running | Templates, and every WorkerPool read |
| `missingTemplate` | Actors naming a template Substrate does not have, one row per template. Substrate refuses to resume them (`FailedPrecondition`) without changing their state, so no other rule sees them | Warning | Actors, and every template read with none denied |
| `workerUnavailable` | A worker unavailable for more than 5 minutes. Substrate's controller marks a worker unavailable when its Pod stops being Ready with an IP, and available once it is again, so the row reads the Pod from Radar's cache: not Ready or no IP means Kubernetes is the cause; Ready with an IP means the controller has not marked it available (read its log); an unread Pod says both | Warning; critical when actors are still assigned to it | Workers (Pods for the cause) |
| `workerDraining` | A draining worker that still holds actors. Draining only stops new placements; Substrate neither suspends nor moves the actors, and crashes any still assigned when the Pod is gone. The deadline is the worker Pod's `deletionTimestamp` from Radar's Pod cache, or, when Radar can't read it, the drain's start plus the 60 minutes Substrate gives worker Pods | Info until the deadline; warning after it | Workers, actors |
| `workerRestarting` | A worker whose registered epoch is ahead of the observed one: its process restarted and Substrate has not yet reconciled the registration. When actors still assigned ran under the earlier process, their sandboxes are gone: the row leads with them ("3 actors lost their sandboxes when … restarted"), links to them (`/actors?worker=<pod>&impact=restarted`) and says to revert them, since Substrate only marks them crashed on reconcile | Critical when actors lost their sandboxes; otherwise warning | Workers (actors for the loss) |
| `poolShort` | Fewer workers take actors than the pool wants: active workers from Substrate when every worker was read, else ready worker Pods from Kubernetes. A partial worker list (capped, or partly denied) proves a free slot it saw but never a shortfall or a full pool; placement then carries `complete: false`. Also raised from Prometheus alone: Substrate's own capacity alert condition held for 5 minutes (`workerShortfall`), or no schedulable worker of a known class (`noSchedulableWorkers`) | Warning; critical when none takes actors and the pool is not still converging (pending, resolving, scaling, or under 5 minutes old), or when Prometheus counts no schedulable worker | WorkerPools, workers or Prometheus |
| `poolFull` | No active worker in the pool has a free actor slot, from a complete worker read, or (Prometheus) no Substrate API server sees one. Slots only: CPU and memory are not part of it. A pool short of workers reads `short`, not full | Warning | Workers or Prometheus |
| `stuckTransition` | Actors resuming, suspending, reverting or deleting for more than 5 minutes, one row per state, saying which worker each waits on | Warning | Actors |
| `goldenSnapshotFailed` | A template whose golden snapshot failed, so new actors using the template's default boot cold. Substrate does not retry, and templates are immutable, so the step is to recreate it, with a higher memory limit when the golden actor was killed for exceeding it. The reading drops Substrate's reason code (`GoldenActorCrashed:`) | Warning | Templates |
| `nodeUploadsOverdue` | A node whose atelet counts snapshot uploads past their deadline (Prometheus), by band (1–5m, 5–15m, over 15m), with the oldest state not yet stored. atelet's clock runs across suspend and resume, so it covers actors the API-only rule below cannot | Warning; critical when the node is NotReady or gone | Prometheus and a node running workers |
| `uploadOverdue` | Suspended actors whose newest snapshot is still pending upload and that were last updated more than 10 minutes ago, broken down by the node holding the files. Radar's threshold on the actor's last change, not Substrate's upload deadline | Warning; critical when such a node is NotReady or gone, or the oldest last update is over 60 minutes ago | Actors (Nodes for the escalation) |

Prometheus verdicts are read for the pools the caller read as WorkerPools and, when the caller may read Nodes, the nodes running workers; `service` on the workspace says whether they were read and why not. Each row carries a title, Radar's reading, a next step (one action, at most 25 words), the `command` that runs it when one does, its subject and related links, how many things it covers, and up to 20 sample actors. A row covering several actors writes `<name>` and `<atespace>` in its command. On an actor's page, `ActorAttention` returns the rows that cover that actor, judged on the fresh read, with the command filled in and the crash step following that actor's own crash message. Readings never embed a clock time: deadlines and times are fields, which the UI formats.

Worker rows (`/workers`, the pool page) carry `workerState` when the worker is draining, unavailable, or active with a registration Substrate has not reconciled (`reconciling`), with how many actors are still assigned and how many lost their sandboxes. A pool's readiness is counted from the worker Pods Radar's cache holds in namespaces where the caller may list Pods (`kubernetes.pods`: created, ready, terminating); `status.readyReplicas` is cited only where it disagrees. The memory-limit step on an actor's page follows the snapshot its revert keeps: on a new template, a snapshot stamped with the old one restores its volumes only, an unstamped root-filesystem snapshot its saved files, and with none the actor boots fresh.

What a worker's state means for an actor on it, `workerImpact` (`unavailable` with the Pod's `podState`, `draining` with the Pod's `deadline`, or `restarted`), is on the actor page, on every row of `/actors`, and on each worker's assigned actors in `/workers` and the pool page.

## API

Routes are registered in `internal/server/server.go` and advertised by the `substrateWorkspace` capability, so Radar Hub asks only a Radar that serves them. All are read-only except `PUT /connection`. "API tier" means the route needs Substrate's API: a request the gate refuses answers 403 `sharedRadar` or `ownersOnly` (or `mintDenied` for a Hub owner whose access cannot mint the token); a tier that is not connected answers 409 (`notConfigured`, `targetChanged`, `settingsFile`, `contextChanged`) or 503 with the code. A bad `PUT /connection` body is 400 `invalidSettings`; an unreadable worker Pod log is 502 `logsUnavailable`.

| Route (under `/api/substrate`) | Returns | Gate |
|---|---|---|
| `GET /status` | Whether WorkerPool is served, Service `ate-system/api` (found, when the caller may read Services there), the API tier, `coverage.pools`, `canConfigure`. Never waits for a first read: it answers `connecting` | Connected to a cluster. Never touches the API on a shared Radar |
| `GET /connection`, `PUT /connection` | GET: this context's saved settings (paths, never a token), whether they were saved for another cluster (`targetChanged`), the shared principal, and the `target` (context and cluster) it was read for. PUT sends `{target, settings}`: that `target` back, and `settings` shaped exactly as GET returns them, or `{"identity": "none"}` (no other field) to remove them; a context that has since moved is refused with `targetChanged` and nothing is saved | A Radar that may read the API; body capped at 16 KiB (413 over it) |
| `GET /workspace` | Status line, Needs attention, `checks`, `notAssessed`, actor states, pool rows (with Prometheus verdicts), `service`, atespaces, per-kind coverage | WorkerPools as the caller everywhere; the API parts only where the API is read |
| `GET /actors` | Actor rows, `total`, per-state `counts` (after every filter but `state`, before paging), `coverage` and `sources` | API tier |
| `GET /actors/{atespace}/{name}` | The actor read fresh, its template (with `matchingPools`), worker, related links, the rows that cover it, egress | API tier; 404 when Substrate has no such actor, 403 when it denies it |
| `GET /actors/{atespace}/{name}/logs` | The actor's lines, from its worker Pod or the node it last ran on ([Where each value comes from](#where-each-value-comes-from)); `source` (`all`, the default, or `containers`, `lifecycle` or `worker`), `container` (not with `lifecycle` or `worker`, whose lines no container emits), `tailLines` (default 500, at most 5,000) | API tier, plus the caller's access to the worker Pods' namespace and to their logs |
| `GET /workers` | Workers with their state, how long it has held, a free slot, Pod readiness and their actors, and counts by state; `pool` narrows to one pool | API tier |
| `GET /templates`, `GET /templates/{atespace}/{name}` | Template rows; one template with its spec, actors by state and matching pools | API tier |
| `GET /workerpools/{namespace}/{name}` | One pool's row, its workers, the templates whose selector matches it, and `coverage.pools` | API tier, and `get workerpools` in that namespace. 404 only when a direct read finds no WorkerPool and every worker was read; 503 `incomplete` when absence cannot be proven |

**`/actors` filters.** All combine.

- `state`: one or more actor states, comma-separated (an unknown state is 400).
- `atespace`: one atespace. When the shared read does not hold all of it (partial, or capped), Radar reads that atespace directly, under the same 50,000-actor budget, and `sources` says `atespace`.
- `template` and `pool`: `<atespace>/<name>` and `<namespace>/<name>`, or a bare name to match it in any atespace or namespace.
- `worker`: a worker Pod name or Substrate's worker name.
- `node` with `nodeHas`: `running` (the actor's worker is on that node) or `localSnapshot` (its newest snapshot is not durably stored and its files are on that node, so losing the node loses that state). `node` without one of them is 400.
- `q`: a case-insensitive substring of the actor's name, within what was read. When `atespace` is set and that atespace's direct read is itself capped, Radar also looks the exact name up with `GetActor` and adds it (`sources` gains `actor`).
- `impact`: `unavailable`, `draining` or `restarted`, the actors whose worker's state means that for them (`workerImpact.kind`); anything else is 400.
- `sort`: `updated` (default, newest change first), `attention` (crashed, then on a worker that is unavailable or restarted under them, then stuck past 5 minutes, longest waiting first, then the rest, newest first) or `name`; anything else is 400.
- `limit` (default 100, clamped to 1–1,000) and `offset` (default 0, clamped to 0–50,000). A value that isn't a number takes the default.

## Testing

`make substrate-demo` (read `scripts/substrate-demo/README.md` first) runs a fake Substrate API server on this machine against real WorkerPools and worker Pods in an existing cluster. It is not a kind cluster: the context defaults to `radar-test-nonprod` (`--context`). It serves Substrate's real wire protocol with deterministic data: 1,293 actors in four atespaces, and one scenario for every Needs attention rule, plus a limited token that is denied one atespace, and injected faults (`unavailable`, `unauthenticated`, `latency`). Connect Radar with the endpoint form the script prints.

Unit tests: `pkg/substrate/*_test.go` (rules, derivations, log filtering), `internal/substrate/manager_test.go`, `internal/server/substrate_workspace_handlers_test.go` (gates, coverage, shared Radar), and `web/src/components/substrate/*.test.tsx`.
