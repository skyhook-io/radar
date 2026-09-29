# CloudNativePG workspace

A task-shaped view over [CloudNativePG](https://cloudnative-pg.io/) (CNPG): which PostgreSQL cluster needs attention, why, and what to inspect next — without assembling the story from ten separate CRD lists. The per-kind renderers, issue detection and audit check it builds on are described in [integrations.md](integrations.md#cloudnativepg).

The workspace is read-only. It never writes to a cluster.

## Where it lives

CNPG stays inside **Resources**; there is no new global navigation item. When the `postgresql.cnpg.io` CRDs are discovered, the Resources sidebar's CloudNativePG group gains a **Workspace** block above its exact kinds:

| Destination | Route | Job | Detail home for |
|---|---|---|---|
| Overview | `/cnpg` | The fleet: every Cluster with instances, replication, protection, declarations and its top problem. Defaults to **Needs attention**. | Cluster |
| Protection | `/cnpg/protection` | Recovery evidence per cluster, failed backups (7 days), destinations, schedules. | Backup, ScheduledBackup, ObjectStore |
| Declarations | `/cnpg/declarations` | Databases, DatabaseRoles (1.30+), Publications, Subscriptions and managed roles by cluster; declared vs reconciled. | Database, DatabaseRole, Publication, Subscription |
| Pooling | `/cnpg/pooling` | Poolers and the clusters they front. | Pooler |
| Operator | `/cnpg/operator` | Operator and plugin workloads, image catalogs, operator configuration. | ImageCatalog, ClusterImageCatalog |

Destination badges count **affected clusters**, not findings, and follow the namespace filter (the sidebar says so). The exact kinds stay under a collapsible **Resource kinds** block, grouped by API group; on workspace screens it starts collapsed.

Every CNPG kind's full detail is `/cnpg/<plural>/<namespace|_>/<name>` — reached from a row's **Open**, from the drawer's expand control, and by redirect from the generic `/workload/...` URL. The page keeps the workspace sidebar (its destination highlighted, the object nested under it) and uses Radar's detail view underneath: **Overview** is a composed summary, **Spec & status** is the kind's existing renderer, then YAML and the rest. A Cluster adds **Protection** (its recovery evidence), **Activity** (in place of Timeline) and merged instance **Logs**.

## Navigation

- **One drawer.** Rows inspect in the app's single drawer; `?drawer=kind:group:namespace:name` backs it, so refresh, share and Back restore it. Links inside the drawer append to that chain and show "← <previous object>" at the top of the drawer.
- **Return vs location.** A full detail shows "← <previous page>" only when it was reached by a drilldown (the label travels in history state); sidebar and global-nav hops are location changes and carry no return label. The crumb (`CloudNativePG / Protection / name`) always names the object's place, so a fresh tab has a parent without a fabricated previous task.
- **Context.** Detail URLs carry `ctx=<kube context>` (added on first view when absent). After a context switch the page says "<name> is not in <context>" with **Switch back** and **Go to …** — Radar never opens a same-named object from another cluster.
- **Namespace filter** narrows collections and counts. An explicitly opened object stays open, with a note when it is outside the filter.

## The certainty contract

Every value is something the cluster reports, labelled with where it came from. When the cluster does not report something the UI says so; it never shows zero, "none" or green in its place.

| Fact | Source | When it is not known |
|---|---|---|
| Instances, primary | `status.readyInstances`, `status.currentPrimary`, instance Pods (controller-owned by the Cluster's UID) | `–` |
| Replication | The primary's `pg_stat_replication` via the instance manager (Runtime); otherwise Pod readiness only | "Lag unknown" when runtime data is unavailable: readiness does not show whether a replica is streaming |
| Schedule | ScheduledBackups targeting the Cluster (`spec.suspend` → suspended) | "No access to ScheduledBackups" when unreadable in that namespace |
| Destination | barman-cloud plugin `barmanObjectName`, in-tree `barmanObjectStore`, or volume snapshots | "No destination configured" |
| Last successful backup | Newest of: completed Backup CRs (7-day window plus the newest per cluster), ObjectStore `serverRecoveryWindow[...].lastSuccessfulBackupTime`, in-tree `status.lastSuccessfulBackup` (ignored for plugin clusters, where CNPG no longer sets it) — the winning source is shown | "None observed", or "No access to Backups" |
| WAL archiving | `ContinuousArchiving` condition | "Not reported" |
| Recovery window | ObjectStore `status.serverRecoveryWindow` for the cluster's server name | "Not reported" |
| Restore validation | A Cluster in the same namespace bootstrapped (`bootstrap.recovery`) from this cluster's store/server or one of its Backups, **with a ready instance** | "None recorded" (unknown tone) — Kubernetes records no restore tests, so this is never green. A matching cluster without a ready instance reads "Recovery declared in …". |
| ObjectStore upload health | **Inferred** from its user clusters' WAL archiving and recovery windows (ObjectStore has no status of its own) | "Unknown" |
| Declarations | `status.applied` (true / false / absent = pending); managed roles from `status.managedRolesStatus` (`reconciled`, `cannotReconcile`; anything else pending). A DatabaseRole whose name also appears in the Cluster's `spec.managed.roles` is overridden — the Cluster spec wins and the operator reports it not applied; its summary says so, or "unknown" when the Cluster is not visible | Pending, never failed |
| GitOps source | Argo CD / Flux labels and the Argo tracking annotation | "GitOps source not recorded" |
| Pooler pressure | Each pooler Pod's PgBouncer exporter (`:9127/metrics`): per pool clients active/waiting, servers active/idle/used, max wait, the pool mode PgBouncer reports, and how many Pods reported | "Not measured" when no Pod could be read; partial (a lower bound) when only some reported |
| Pooler readiness | The Pooler's Deployment (controlled by the Pooler's UID): ready of desired replicas. The Pooler's own `status.instances` counts scheduled Pods only | "Unknown" when the Deployment cannot be read |
| Pooler paused | Requested: `spec.pgbouncer.paused`. Observed: each PgBouncer's `SHOW STATE` over the caller's `pods/exec` | Observed reads "Not observable: needs create pods/exec"; the exporter does not publish it |
| Pooler limits | `spec.pgbouncer.parameters`; unset ones read "default" plus PgBouncer's own default where it was read from PgBouncer (`SHOW CONFIG`, 1.24): `default_pool_size` 20, `max_client_conn` 100, the per-database/user maxima and reserve/min pools 0. CloudNativePG writes only the parameters the Pooler sets | — |
| Blocking sessions | `pg_stat_activity` + `pg_blocking_pids()` on one instance, via psql over the caller's `pods/exec` | "Blocking detail needs create pods/exec"; the exporter's aggregate counts still apply |
| Instance CPU / memory | metrics-server (`metrics.k8s.io`) usage of the postgres container, against its limit | "not measured: the metrics API is not available" |
| ScheduledBackup cron | Shown verbatim | CNPG's cron is six-field (seconds first) and is never translated |

Backup schedules: besides "No backup has run since this schedule was due" (the operator's own `nextScheduleTime` passed), the Issues engine raises **"No successful backup since ScheduledBackup <name> fired at <time>"** on the Cluster when an active schedule fired (six-field cron, seconds first, parsed as the operator does) after the cluster's newest successful backup (Backup objects, the ObjectStore's `lastSuccessfulBackupTime`, in-tree `status.lastSuccessfulBackup`), allowing the last successful backup's duration plus 10 minutes, and no Backup started since is still running. Suspended schedules raise nothing. The workspace shows it only to callers who can list ScheduledBackups in the namespace.

Problems come from Radar's Issues engine (the same detections as `/issues`) plus the audit's `cnpgNoDeclarativeBackup`, worded "No declarative backup schedule" because that is all it proves. A cluster **needs attention** when it has an issue of warning or worse on itself, an instance Pod, or an object that references it.

## Access

All data comes from `GET /api/cnpg/workspace`, authorized **per kind**: namespaced kinds use a cluster-wide `list` or fall back per namespace; `ClusterImageCatalog` needs a cluster-scope `list`. Each kind reports coverage (`full`, `partial` with the namespaces read, `denied`, `syncing`, `error`, `notInstalled`). Issues and audit findings are withheld where the underlying kind is not covered — Pod evidence only reaches callers who can list Pods. Denied namespaces are named only when the caller supplied the namespace list. A partial or denied kind makes the screen show a coverage notice, and its facts read "No access" rather than none.

`GET /api/cnpg/operator` reads operator and plugin Deployments (label `app.kubernetes.io/name=cloudnative-pg`, plugin Services labelled `cnpg.io/pluginName`) and the operator's config references. It ignores the namespace view filter (the operator lives in its own namespace), returns ConfigMap data only with `get configmaps`, and never reads Secrets.

Cluster logs (`/api/cnpg/clusters/{ns}/{name}/logs`) need `get pods/log`; Activity (`.../activity`) drops events for kinds the caller cannot list. Deleted child objects stay attributed to their Cluster because Radar records the owning cluster on timeline events at ingestion; history recorded before that is marked incomplete.

## Runtime

`GET /api/cnpg/clusters/{ns}/{name}/runtime` and `GET /api/cnpg/poolers/{ns}/{name}/runtime` read live data **through the caller's `pods/proxy`**: each instance manager's `/pg/status` (`:8000`) and the Postgres exporter (`:9187`), or each PgBouncer exporter (`:9127`). The apiserver strips the caller's credentials and `Impersonate-*` headers before forwarding, so a Pod never sees who asked. Only fixed GET paths are requested (some instance-manager paths mutate on GET), redirects are refused, and TLS is never downgraded after a certificate error. Requests are bounded: 4 in flight, 5 s deadlines, 1 MiB per status and 4 MiB per metrics body, memoized per identity and Pod UID for 5 s (status) or 25 s (metrics).

Each source reports its own state (`ok`, `partial`, `denied`, `unreachable`, `error`). **Denied is never shown as zero**: without `get pods/proxy` the Runtime tab names the missing grant, and the rest of the workspace falls back to the facts above. The Runtime tab shows replication (per-standby write, flush and replay lag), aggregated sessions and lock waits, transaction rates, storage and WAL, replication slots, and trends sampled while the tab is open, with gaps shown where a sample failed. These reads carry no per-session query text; the blocking view (below) does, for callers who can exec into the instance.

## Storage

`GET /api/cnpg/clusters/{ns}/{name}/storage` backs the Runtime tab's **Storage & WAL** section. `GET /api/cnpg/disk` fills the fleet's **Disk** column and the Cluster overview's **Storage** fact with each cluster's fullest measured volume. Each value has its own source and its own "unknown":

| Fact | Source | When it is not known |
|---|---|---|
| The instance's volumes | Claims labelled `cnpg.io/cluster` **and** owned by the Cluster's UID, naming an instance (`cnpg.io/instanceName`), by `cnpg.io/pvcRole` | "No access (needs list persistentvolumeclaims)". A labelled claim the Cluster does not own is listed as not counted |
| Requested / capacity | The claim's `spec.resources.requests.storage` / `status.capacity` | `—` |
| Resize in progress | Claim conditions (`Resizing`, `FileSystemResizePending`, resize errors), `status.allocatedResourceStatuses`, request larger than capacity, the Cluster's `status.resizingPVC` | — |
| Healthy / dangling / unusable | The Cluster's `status.healthyPVC`, `danglingPVC`, `unusablePVC`, `initializingPVC` | Not shown |
| Expansion allowed | The StorageClass's `allowVolumeExpansion` (unset = not allowed) | "expansion unknown" when the class cannot be read |
| Used space | Prometheus `kubelet_volume_stats_used_bytes` / `_capacity_bytes` per claim, behind the same grant as the PVC chart | "Used space unknown" with the reason (no series, no Prometheus, no access). Never 0. When kubelet's filesystem is much larger than the claim (local volumes), the view says the figure is the shared filesystem's |
| WAL on disk | Exporter `cnpg_collector_pg_wal{value="size"\|"count"}` | "unavailable" with the pods/proxy state |
| Waiting to archive | Instance manager `readyWalFiles`, with the last archived/failed times | same |
| Held by replication slots | Exporter `cnpg_pg_replication_slots_pg_wal_lsn_diff` | same |

The three WAL measures overlap and are shown side by side, never added up. Disk findings (≥ 80 % warning, ≥ 90 % critical) come only from a measurement, are computed where they are read, and reach the fleet's Needs attention only for callers who received the measurement (`list persistentvolumeclaims` + the Prometheus PVC grant); they are not Issues-engine issues.

**Expanding.** The view names the field each size is declared in — `spec.storage.size`, `spec.walStorage.size`, or `spec.tablespaces[name=<t>].storage.size` (or the `pvcTemplate` request when that is how it was declared) — and whether the class allows expansion. **Edit size…** shows the GitOps write guard for that field, then opens Radar's apply flow with a manifest carrying only the new size (tablespaces carry the whole list, which the CRD does not merge by key). The operator resizes the claims; CloudNativePG does not shrink volumes.

Volumes and their usage do not depend on `pods/proxy`: without it the Runtime tab still shows them under its access notice.

The replication view measures each standby's catch-up as **replay backlog in bytes**: the primary's current LSN minus the standby's replay LSN, broken into not sent / not written / not flushed / not replayed. PostgreSQL's `write_lag`, `flush_lag` and `replay_lag` are shown as what they are — acknowledgement delay for recent WAL, empty when idle and caught up — never as catch-up time. Each instance also carries its own report from `/pg/status`: a role detail derived only from that report (`primary`, `streaming` standby, `fileBased` = no WAL receiver, `replayPaused`, `pgRewind`), `pendingRestart` / `pendingRestartForDecrease`, timeline and instance-manager version. Pending restart is runtime-derived: it shows on the cluster page and the instance cards for callers who can read runtime, and never enters the cached-object Issues engine.

## HA and instances

`GET /api/cnpg/clusters/{ns}/{name}/ha` backs the Overview's "HA and instances" section, the header dimension chips, the switchover dialog and the operation tracker. Reading the Cluster (`get clusters`) never implies the rest: each part is authorized on its own and reports `ok | denied (with the grant) | notFound | notInstalled | unavailable | error`.

| Fact | Source | Gate | When it is not known |
|---|---|---|---|
| Instances: node, QoS, running image vs desired (`status.image`, else `spec.imageName`), Pod and postgres-container start | instance Pods (controller-owned) | `list pods` | "Pods not readable" |
| Zone | `topology.kubernetes.io/zone` on each Node | cluster-scoped `get nodes` | zones unknown (never "single zone") |
| Failover quorum | `FailoverQuorum` of the Cluster's name (1.27+): recorded sync configuration, not the operator's verdict. N = potentially synchronous standbys, W = `standbyNumber`, R = those with a ready Pod (not the recorded primary); shows whether R + W > N | `get failoverquorums` | not installed / not found (quorum failover off) / a reset object = "no configuration recorded: a failover would wait" |
| Disruption budgets | PDBs owned by the Cluster: expected / healthy / allowed, stale when not observed | `list poddisruptionbudgets` | `enablePDB: false` is the declared state, not a fault |
| Primary Lease | `Lease` of the Cluster's name (1.30+), holder, renew, expired | `get leases` | "creates one from 1.30" — absence on older versions is not a fault |
| Operator leader | `db9c8771.cnpg.io` Lease in the operator Deployment's namespace | `list deployments` + `get leases` | operator namespace unknown |
| Cluster Jobs | Jobs owned by the Cluster (`cnpg.io/jobRole`: initdb, join, major-upgrade, snapshot-recovery…), phase | `list jobs` | "No access to Jobs" |
| Read-write endpoints | ready EndpointSlice Pods of `<cluster>-rw` | `list endpointslices` | the Serving chip falls back to primary Pod readiness and says so |
| Certificates | `status.certificates.expirations` (Go `Time.String()`, parsed server-side; unparseable = unknown) with renewal owner: operator-generated (CNPG renews) or named in `spec.certificates` (you renew). For user-provided Secrets, their **metadata only** names a cert-manager `Certificate` | `get secrets` (metadata client, never data) | "issuer unknown" |
| Maintenance | `spec.nodeMaintenanceWindow` (`reusePVC` defaults to true) | — | — |

Header chips — **Serving · Replication · Protection · Storage** — each come from their own source (primary Pod readiness + `-rw` endpoints; the primary's `pg_stat_replication`; WAL archiving, destination and last backup; volume usage) and read **unassessed** when it is unavailable. The controller phase stays labelled "reported by CNPG".

Certificate expiry is also an Issues-engine finding (`CNPGCertificateExpiring`, one per Secret): a certificate its owner renews is a warning under 30 days and critical under 7; an operator-managed one only once renewal is overdue (under a day — CNPG renews at 7 days by default, so earlier would light every cluster for a third of each 90-day lifetime); expired is critical.

## Actions

Capabilities (`GET /api/cnpg/clusters/{ns}/{name}/capabilities`, `/api/cnpg/scheduledbackups/{ns}/{name}/capabilities`) return the facts a confirmation is bound to, each action's verdict (allowed, or a reason naming the missing grant or the blocking state), per-instance actions, and the effects of a restart or hibernation. Actions are POSTs to `.../actions/{action}`, made with the caller's impersonated client:

| Action | Write | Grant |
|---|---|---|
| Back up now | create `Backup` with an explicit method (plugin only when the plugin reports backup capability; an unreported capability is labelled) | `create backups` |
| Switchover / promote | status patch: `targetPrimary`, `targetPrimaryTimestamp`, phase and Ready condition, as `kubectl cnpg promote` does | `patch clusters/status` |
| Restart (rolling) | annotation `kubectl.kubernetes.io/restartedAt` | `patch clusters` |
| Restart one instance | standby: delete the Pod with a UID precondition; primary: status phase write, as `kubectl cnpg restart` does | `delete pods` / `patch clusters/status` |
| Reload configuration | annotation `cnpg.io/reloadedAt` | `patch clusters` |
| Fence / lift fence | annotation `cnpg.io/fencedInstances`, a JSON array (`["*"]` = all). Malformed JSON blocks the action; lifting one instance while `*` applies is refused | `patch clusters` |
| Hibernate / rehydrate | annotation `cnpg.io/hibernation` `on` / `off` | `patch clusters` |
| Node maintenance set / lift (advanced) | merge patch `spec.nodeMaintenanceWindow {inProgress, reusePVC}` as `kubectl cnpg maintenance set/unset`; binds the reviewed maintenance facts; a standing banner offers the lift | `patch clusters` |
| ScheduledBackup suspend / resume | `spec.suspend` | `patch scheduledbackups` |
| ScheduledBackup run now | create `Backup` copying method, plugin configuration, online settings and target | `create backups` |

| Cancel / terminate a backend | from the blocking view only: `pg_cancel_backend` / `pg_terminate_backend` run by psql in the instance, for the pid only while its `backend_start` still matches, client backends only | `create pods/exec` |
| Destroy instance (standby) | `kubectl cnpg destroy` parity: the instance's PVCs detached (`--keep-pvc`: Cluster owner removed, `cnpg.io/pvcStatus: detached`) or deleted with UID preconditions, then the Pod, then Jobs labelled `cnpg.io/instanceName`. The operator creates a replacement instance under a new name. The primary is refused (switch over first) | `delete pods`, `list`/`delete` (or `update`) `persistentvolumeclaims`, `list`/`delete jobs` |
| Pooler pause / resume | `spec.pgbouncer.paused` (the operator runs PgBouncer `PAUSE` / `RESUME`) | `patch poolers` |

Restore to a new cluster opens the standard create dialog with a `bootstrap.recovery` manifest (source Backup or ObjectStore, optional point-in-time target) for review; it is not a separate API.

Every request carries the facts the user reviewed (kube context, UIDs, current and target primary, fencing and hibernation values). The server re-reads them and returns **409** if anything changed; disruptive actions are never retried automatically. Errors carry a `code` (`context_changed`, `changed`, `blocked`, `all_fenced`, `operator_webhook_unavailable`, `outcome_unknown`). An outcome that is unknown after a timeout is resolved by re-reading the same Backup name, never by creating another.

Cancel and terminate bind the instance Pod UID, the pid and the backend's `backend_start`; destroy binds the Pod UID and the name and UID of every PVC reviewed; pause/resume binds the reviewed `paused`. Each returns `target` identifying what it acted on, so an operation tracker can follow the outcome.

The dialog (`ActionConfirmDialog` in k8s-ui) leads with the effect, keeps the literal API writes in an expandable section, and requires typing the name for disruptive actions (switchover, primary fence, lifting a fence, cluster restart, hibernate, cold backups).

### Operation tracker

A successful POST means the write was accepted, not that it happened. The dialog hands off a tracked operation (`trackCNPGOperation` in `web/src/components/cnpg/operations/`) bound to the kube context, the Cluster UID, the target (name + UID) and a baseline; the Cluster header shows it until it finishes. States: `requested → observed → progressing → completed | failed | stalled | superseded | unobservable`. A step Radar cannot see keeps the operation **unobservable**, never completed; **stalled** needs telemetry showing no movement for 10 minutes. A newer conflicting operation, or the Cluster being recreated, supersedes it. Completion per kind: switchover — the target is `currentPrimary`, the old primary streams again, the `-rw` endpoints point at the target (where readable) and the phase is healthy; restart — per instance, a recreated Pod, a restarted postgres container or a later `cnpg_pg_postmaster_start_time`; reload — no completion signal, said so; fence / lift — Pod readiness and streaming; hibernate / rehydrate — the hibernation condition and ready instances; backup and schedule run — the Backup's phase; maintenance — the spec. Operations persist per browser session (`sessionStorage`, in memory when unavailable). New kinds register an observer with `registerCNPGOperationObserver(kind, fn)`.

## Inside PostgreSQL

**psql.** Every instance row in Runtime → Replication has **psql**, and the cluster menu has **Open psql on the primary**. It opens the dock terminal on the instance Pod, container `postgres`, running `psql` (`?shell=psql`, one argv element) over the caller's own `pods/exec` — the local socket as the `postgres` superuser, as `kubectl cnpg psql` does. The tab says whether the instance was the primary (read-write) or a standby (read-only while it stays one) when it opened. Disabled, with the grant named, without `create pods/exec`.

**Blocking view.** Runtime → Sessions keeps the exporter's aggregates and adds, for exec-capable callers, `GET /api/cnpg/clusters/{ns}/{name}/sessions`: a fixed query (never built from input) run by psql in the primary (or a chosen instance) with a 5 s statement timeout, excluding its own backend. It lists only backends in a blocking relation as blocker → victim trees (a victim of two blockers appears under both; a lock cycle is marked), with wait event, transaction/query/connection age, user, database, application, client address and query text cut at 200 characters. Query text is shown because the same grant lets the caller open psql and read it. Beside it: client connections against `max_connections` minus the superuser reserve, and each instance's CPU and memory from metrics-server, so lock waits and resource pressure are told apart. Cancel is offered first (it does nothing for an idle-in-transaction holder, which the dialog says); terminate names the transaction it rolls back and requires typing the pid.

## GitOps write guard

Any write to an object a GitOps tool manages can be undone by the next sync. `POST /api/gitops/write-evidence` reports, for the paths an action writes, whether each is declared in `last-applied-configuration`, owned by a GitOps field manager (Argo CD, Flux, Helm), or covered by an ignore rule (Argo CD only with `RespectIgnoreDifferences`), plus the owner's sync policy (Argo CD self-heal and automated sync, Flux suspend and interval, HelmRelease drift detection). `evaluateGitOpsWriteGuard` in k8s-ui turns that into a warning, and `GitOpsWriteWarning` renders it with an acknowledgement when a revert is likely. The copy never promises a change will stick. Status writes are described as fields ordinary GitOps sync does not manage.

The guard is shared: the CNPG actions, Set image and the investigation apply dialog use it (`useGitOpsWriteGuard` in `web/`). Other GitOps-aware writes (YAML force-apply, Helm and GitOps rollbacks) still carry their own warnings.

## Testing

`make cnpg-demo` (read `scripts/cnpg-demo/README.md` first) produces WAL archiving failure, failed and unrecognised-phase Backups, failing declarations, a Pooler, both catalog kinds and an ObjectStore with a failing server — every state the workspace distinguishes, except successful restores.
