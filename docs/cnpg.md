# CloudNativePG

A task-shaped view over [CloudNativePG](https://cloudnative-pg.io/) (CNPG): which PostgreSQL cluster needs attention, why, what its instances are doing right now, and what to do about it — without assembling the story from ten CRD lists, `kubectl cnpg` and a Grafana dashboard. The per-kind renderers, issue detection and audit check it builds on are described in [integrations.md](integrations.md#cloudnativepg).

Reading never writes. Writes happen only through [Actions](#actions), [guided backup setup](#guided-backup-setup), [Restore](#restore), and Create and Edit size in Radar's apply flow. Each is made with the caller's own identity after the user reviews it; actions also bind the facts reviewed, and writes to existing objects show the [GitOps write guard](#gitops-write-guard).

## Code boundaries

- `pkg/cnpg` owns pure interpretation of backup declarations, fencing, cron schedules and instance ownership, with no Radar `internal/` imports. Audit, Issues and action guards use it. The TypeScript backup model (`packages/k8s-ui/src/utils/cnpg-backup.ts`) runs against the same cases in `pkg/cnpg/testdata/backup-declarations.json`.
- `internal/cnpg` owns read orchestration, capabilities, actions, reports and log/activity interpretation, independent of HTTP. Server adapters (`internal/server/cnpg_service.go`) supply the caller's authorized observations, Kubernetes clients and metrics reads; handlers keep routing, bounded decoding, serialization and stream framing. Reports call service operations directly, never handlers.
- `internal/integration` owns the shared read-coverage and action contracts, bounded fan-out and version-bound patching (see [INTEGRATION_GUIDE.md](INTEGRATION_GUIDE.md#3-workspace-integrations)). Namespace selection lives in `internal/readscope`.
- `internal/issues/source_cnpg*.go` owns findings derived from cached objects. Cross-resource findings record the inventories they read in `Issue.RequiredReads`, so REST, MCP and Diagnose withhold them from a caller who cannot read those inventories. An unread or incomplete Pod inventory never establishes a contradiction.
- `packages/k8s-ui/src/components/cnpg` owns presentation and pure fleet derivations. Problems carry semantic `reason` values and action capabilities carry `reasonCode` beside the display `reason`; consumers never branch on generated IDs or display text.
- `web/src/api/cnpg*.ts` owns fetching and wire types; `web/src/components/cnpg/` the screens. `host.tsx` composes CNPG into Radar's generic detail view, registered in `web/src/integrations/resourceHosts.tsx`.

A backup destination, a WAL archive and a destination for one ScheduledBackup method are different facts: snapshots and backup-only plugins do not establish continuous archiving.

## Where it lives

CNPG stays inside **Resources**; there is no new global navigation item. When the `postgresql.cnpg.io` CRDs are discovered, the Resources sidebar's CloudNativePG group gains a **Views** block above its exact kinds:

| Destination | Route | Job | Detail home for |
|---|---|---|---|
| Clusters | `/cnpg` | The fleet: every Cluster with readiness, replication, storage, backups and its top problem, ordered by urgency; **Needs attention** narrows it. | Cluster |
| Backups | `/cnpg/protection` | Recovery evidence per cluster, backup runs (7 days; failed first when any failed), schedules with their last run's outcome, destinations. | Backup, ScheduledBackup, ObjectStore |
| Declarations | `/cnpg/declarations` | Databases, DatabaseRoles (1.30+), Publications, Subscriptions and managed roles by cluster; declared vs reconciled. | Database, DatabaseRole, Publication, Subscription |
| Pooling | `/cnpg/pooling` | Poolers and the clusters they front. | Pooler |
| Operator | `/cnpg/operator` | Operator and plugin workloads, leader, watched namespaces, webhooks and reconcile errors, image catalogs, operator configuration. | ImageCatalog, ClusterImageCatalog |

Destination badges count **affected clusters**, not findings, and follow the namespace filter. Outside the CloudNativePG views the Clusters badge counts without disk use or measured replication lag, so it reads as a lower bound (≥N) and its zero as unknown (–).

**The Cluster kind's list is the Clusters view.** `/resources/clusters?apiGroup=postgresql.cnpg.io`, where the sidebar, favorites, search and links from Issues, Home, Checks and investigations land, renders the Clusters view in place of the generic table; its drawer, history and Back behave as for any kind. `/cnpg` stays the workspace's own home. A Radar without these views, or whose capabilities could not be read, keeps the generic table; on the first capabilities load the page waits rather than flashing the table. Cluster API's `clusters` kind (`cluster.x-k8s.io`) is untouched. **Create**, on both routes, guides name, namespace, instances and volume size into a strict-create manifest, so it can never update an existing Cluster.

**What "every Cluster" means.** Every Cluster the caller may list, in the namespaces Radar can see for them: the namespace filter, the caller's namespace visibility and Radar's own watch scope. Namespaces Radar's cache does not hold read as not read, and the count reads ≥N — never an empty list or a total that looks exact.

Every CNPG kind's full detail is `/cnpg/<plural>/<namespace|_>/<name>`, also the redirect target of the generic `/workload/...` URL. It uses Radar's detail view: **Overview** is a composed summary, **Spec & status** the kind's renderer, then YAML and the rest. A Cluster's page is organized by task; each tab is the one home of its facts, and the others link to it:

| Tab | Job | Holds |
|---|---|---|
| Overview | What needs attention, and where to act | Notices and problems; **At a glance** (one line per health dimension, instances, the controller phase, standby cloning progress); **About**; every operator condition, folded |
| Replication | Diagnose instances and assess a primary change | One card per instance (Pod readiness, node, role, timeline, streaming state, backlog, the HA slot kept for it), other slots, the Serving verdict, the HA configuration. Instance actions live here |
| Storage | What occupies space, why, and resize | Volumes and usage, what holds WAL (archive queue, slot retention per standby), resize |
| Performance | Connections, blocking and database health | **Sessions** (headroom, blocking tree, cancel/terminate), **Database health**, **History** charts |
| Backups | Recovery evidence and backups | **Restore to a new cluster**, the archiving repair panel (below), recovery evidence, runs, schedules, destination, restore validation |
| Activity, Logs | What happened | Events and changes; merged instance logs |
| Configuration | What is declared, and what the instances report | **Connect**, **PostgreSQL parameters** (declared vs each instance's value), declaration cards naming their spec paths, **Certificates** |
| YAML | The object | — |

While WAL archiving fails, and for a day after it resumes, Backups shows a **repair panel**: the operator's `ContinuousArchiving` message, the last archived and failed WAL, where the destination is declared, the credentials it references (named, never read), the archiver's logs and a checklist. The final step is green only for a completed Backup that started after the resume and whose own `status.beginWal` comes after the last WAL that failed to archive: a backup taken from a lagging standby can start after the resume yet begin before the failure. Without the failed WAL (only the primary's instance manager reports it, and its stats restart with the primary) that step stays unverified, never green.

Four health dimensions follow the reader on every tab: **Serving** on the title line, and **Replication**, **Storage** and **Backups** as tab marks — a dot when something needs a look, a hollow ring when it could not be assessed, nothing when it is fine. A single instance, or a Cluster with no backup destination, is posture: At a glance says so in words, without a mark. Every surface reads one assessment (`useCNPGClusterAssessment`), the fleet row enriched with what the instance managers report, so no surface reads calmer than another.

Configuration keeps every declared parameter visible when no instance answered. **Not sampled** (new, or beyond the sample cap), **Not read** (with the reason) and **Not reported** (the instance answered without it) are different states. `status.image` is the operator's target image, not evidence of a running image.

## Navigation

- **One drawer.** `?drawer=kind:group:namespace:name` backs the app's single drawer, so refresh, share and Back restore it. Links inside the drawer append to the chain.
- **Location, not history.** A detail's title line starts with where the object lives (`CloudNativePG / Clusters /`). The crumb is the only navigation control, the same in a fresh tab as after a drilldown; Back is the browser's.
- **Operator banner.** While the operator is not reconciling, a Cluster's page opens with that warning before any status it shows.
- **Context.** Detail URLs carry `ctx=<kube context>`. After a context switch the page says "<name> is not in <context>" — Radar never opens a same-named object from another cluster.
- **Namespace filter** narrows collections and counts. An explicitly opened object stays open, with a note when it is outside the filter.

On narrow screens the resource sidebar moves into a **Resources** dialog with the same navigation.

## The certainty contract

Every value is something the cluster reports, labelled with where it came from. When the cluster does not report something the UI says so; it never shows zero, "none" or green in its place. These are Radar's shared rules for unknown and partial values ([DESIGN.md](../DESIGN.md#unknown-partial-and-denied-values)); the table below is where each CloudNativePG fact comes from.

| Fact | Source | When it is not known |
|---|---|---|
| Instances, primary | `status.readyInstances`, `status.currentPrimary`, instance Pods controller-owned by the Cluster's UID | Missing operator readiness reads "Not reported by the operator". An unread Pod inventory never implies zero |
| Standby cloning | The primary's `/pg/status` base-backup progress (`pg_stat_progress_basebackup`), reported only for new instances joining | Omitted without runtime data; "None running" only from a report that was read. A total PostgreSQL has not estimated never reads 0 % |
| Replication | The primary's `pg_stat_replication` via the instance manager; in the fleet, Prometheus `cnpg_pg_replication_lag` **and** `cnpg_pg_replication_is_wal_receiver_up` | "Lag unknown" when neither is readable. A replay lag of 0 never stands in for streaming — a standby that receives nothing also reads 0 — so without receiver state it reads "streaming unverified" |
| Schedule | ScheduledBackups targeting the Cluster. A method without a destination reads blocked, and an enabled schedule with a verified destination blocker is a problem for its Cluster. Only a completed Backup is evidence of a successful run | "No access to ScheduledBackups" |
| Destination | The barman-cloud plugin entry (`barmanObjectName`, `serverName`), in-tree `barmanObjectStore`, or volume snapshots | "No destination configured" |
| Last successful backup | Newest of completed Backups, the ObjectStore's `lastSuccessfulBackupTime` and in-tree `status.lastSuccessfulBackup` (ignored for plugin clusters, where CNPG no longer sets it); the winning source is shown. Only the current Cluster incarnation's runs count (below) | "None observed", or "No access to Backups" |
| WAL archiving | The declared archive destination or `isWALArchiver` plugin, then the operator's `ContinuousArchiving` condition | Without a destination: "Not archived: no destination configured", so no point-in-time recovery. CNPG reports the condition True even then, accepting each WAL file without keeping it, so True alone never reads as archived. Third-party archivers are not assessed; an archiver with no condition reads "Not reported" |
| Recovery window | Earliest point from the ObjectStore's `status.serverRecoveryWindow`. No status reports the latest point: it follows WAL archiving, and reads "not advancing" only while `ContinuousArchiving` is False | "None: no backup destination", otherwise "Not reported" |
| Restore validation | A person's note on a restored Cluster (`radar.skyhook.io/restore-validation`, format version 1, naming this Cluster's UID); a note in an unknown format, or from an earlier Cluster of the name, is skipped. "Restored into …" needs a ready instance | "None recorded" — Kubernetes records no restore tests, so this is never green, and a recorded note is neutral, not passed |
| ObjectStore upload health | **Inferred** from its clusters' WAL archiving and recovery windows; ObjectStore has no status of its own | "Unknown" |
| Declarations | `status.applied` (true / false / absent = pending); managed roles from `status.managedRolesStatus`. A DatabaseRole also named in the Cluster's `spec.managed.roles` is overridden by it, and says so | Pending, never failed. Counts are exact only when every declaration kind and Cluster spec in scope was read; otherwise ≥N, and zero reads Unknown |
| Logical replication path | Per Subscription: the external cluster's host, resolved to a visible Cluster only through its `-rw`/`-ro`/`-r` or Pooler Service (anything else stays external, never guessed); the publication and slot as PostgreSQL names them, observed on the publisher's primary. Failover survival is declared, not observed: PostgreSQL 17's `failover` parameter plus the publisher's logical slot synchronization; before 17 it depends on `pg_failover_slots`, which Radar cannot see | Slot: "not read" or "no access", and "not found" only from a report that was read. Failover: "unknown" for an external publisher, an unknown major or PostgreSQL < 17. Apply errors and lag beyond `status.message` are not reported |
| GitOps source | The Argo CD, Flux or Helm manager detected on the server (`managedBy`) | "GitOps source not recorded" |
| Pooler pressure | Each PgBouncer exporter: clients, servers, max wait, pool mode, and how many Pods reported | "Not measured"; partial (a lower bound) when only some Pods reported |
| Pooler readiness | The Pooler-controlled Deployment's ready replicas; the Pooler's `status.instances` counts scheduled Pods only | "Unknown" when the Deployment cannot be read |
| Pooler paused | Requested `spec.pgbouncer.paused`; observed with PgBouncer's `SHOW STATE` over the caller's `pods/exec` (the exporter does not publish it) | "Not observable: needs create pods/exec" |
| Pooler limits | `spec.pgbouncer.parameters`; unset ones read "default", with PgBouncer's own default | — |
| Blocking sessions | `pg_stat_activity` + `pg_blocking_pids()` via psql over the caller's `pods/exec` | "Blocking detail needs create pods/exec" |
| Per-database health | The instance's exporter, per database: rollback ratio, temporary files, transaction and multixact ID age; cumulative since the last statistics reset | `—` with the missing metric family named; a database with no transactions has no ratio, not 0 % |
| Extensions with updates | Exporter `cnpg_pg_extensions_update_available` | "Every installed extension is at its default version" only when the family was reported |
| Connect | Service addresses and the database, owner and Secret resolved from the Cluster spec as CloudNativePG resolves them. The Secret is linked, **never read**; connection strings carry a `<password>` placeholder. Availability comes from ready `-rw` endpoints and instance roles; it is not a login check | "Not checked" with the reason or missing grant |
| Checkpoints | Exporter `pg_stat_checkpointer` (PostgreSQL 17+) or `pg_stat_bgwriter` | "Unknown: the exporter reported neither" |
| Instance CPU / memory | metrics-server usage of the postgres container, against its limit | "not measured: the metrics API is not available" |
| ScheduledBackup cron | Worded and previewed as the operator parses it (robfig/cron v1: six fields, seconds first), counted from `status.lastCheckTime`; when a due time has passed since then, the operator runs one backup as soon as it sees the schedule, and the UI says so. Times are in the operator's declared `TZ` when readable, otherwise a labelled UTC estimate | Verbatim only, without the capabilities read; a schedule no time satisfies reads "the operator would never run it" |

**Barman-cloud destinations.** Every backup takes its destination from the **Cluster's** plugin entry: `barmanObjectName` selects the ObjectStore, `serverName` the archive server (default: the Cluster name). The plugin ignores those parameters on Backups and ScheduledBackups, so they never satisfy a missing Cluster destination, and **Back up now** refuses them (400). Backup-to-ObjectStore links and restore sources are therefore inferred from the current Cluster configuration, which may have changed since a run. Third-party plugin destinations are not modeled.

**Cluster incarnations.** Backups match the live Cluster by the UID recorded in `status.pluginMetadata.clusterUID` or their Cluster owner, and must not predate its creation. Backups from a previous same-name Cluster stay browsable but supply no current protection, problems, inferred destinations or restore attribution; a schedule run before the current Cluster existed is not a missed run for it.

**Missed scheduled runs.** Besides the operator's overdue `nextScheduleTime`, the Issues engine raises "ScheduledBackup <name> has had no successful backup since its run" when an active schedule fired after the newest successful backup (allowing that backup's duration plus 10 minutes) and no newer Backup is still running. Nothing is raised until the namespace's ScheduledBackups, Backups and ObjectStores are listed from a synced cache, so startup or a context switch never reads as "no backup".

Problems come from Radar's Issues engine (the same detections as `/issues`), the audit's `cnpgNoDeclarativeBackup` (worded "No declarative backup schedule", because that is all it proves), and measurements:

- **A standby not receiving WAL** (warning; critical when no expected standby receives): from Prometheus in the fleet, from the instance managers on the Cluster page. Both raise the same problem, so the page's answer replaces the fleet's. A fenced standby, one running `pg_rewind` and a replica cluster's designated primary are not raised.
- **An inactive physical slot on the primary** retaining at least 1 GiB. CloudNativePG copies HA slots to standbys, where they are always inactive, so a standby's copy never raises.
- **A backup schedule that cannot run** because its method has no destination.
- **Disk usage** at or past 80 % / 90 %, and **sustained replication lag** (see [History](#history)).

A cluster **needs attention** when it has a warning-or-worse issue on itself, an instance Pod, one of its own Job Pods (Pod → Job → Cluster by controller name and UID, only where the caller may list Jobs), or an object that references it. The fleet orders clusters by worst problem, then the number of problems, then namespace and name.

The fleet's lag covers only standbys whose lag was read; with fewer than expected it reads "N of M standbys reporting" and never healthy. A replica cluster's lag is replay behind what arrived, so a dead upstream feed still reads 0: it reads "upstream feed not assessed", never healthy. A WAL receiver must be down in every sample over 5 minutes before the fleet raises it, so a restarting standby or a just-demoted primary is shown but not raised. A live read on the Cluster page overrides the fleet only where it disproves it.

## Access

All workspace data comes from `GET /api/cnpg/workspace`, authorized **per kind**: a cluster-wide `list`, or per namespace as a fallback; `ClusterImageCatalog` needs a cluster-scope `list`. Each kind reports coverage — `full`, `partial` (with the namespaces read), `denied`, `uncached`, `syncing`, `error` or `notInstalled` — and a partial or denied kind shows a coverage notice, with its facts reading "No access" rather than none. `syncing` and `error` never establish that no objects exist. A namespace the caller may read but Radar's cache does not hold reads "Radar does not cache …", not "no access". Denied namespaces are named only when the caller supplied the namespace list. Issues and audit findings are withheld where their kind is not covered.

The operator read ignores the namespace filter (the operator lives in its own namespace), returns ConfigMap data only with `get configmaps`, and never reads Secrets. Logs need `get pods/log`; Activity drops events for kinds the caller cannot list. Deleted child objects stay attributed to their Cluster because Radar records the owning cluster on timeline events at ingestion.

## Live instance data

Runtime reads go **through the caller's `pods/proxy`**: each instance manager's `/pg/status` (`:8000`), the Postgres exporter (`:9187`) and each PgBouncer exporter (`:9127`). The apiserver strips the caller's credentials before forwarding, so a Pod never sees who asked. Only fixed GET paths are requested, because some instance-manager paths mutate on GET; redirects are refused, and TLS is never downgraded after a certificate error. Reads are bounded and briefly memoized per identity and Pod UID.

Each source reports its own state: `ok`, `partial`, `denied`, `unreachable` or `error`. A status the instance manager answered without finishing its reads (it masks errors while PostgreSQL may be unavailable, and reads nothing during `pg_rewind`) is `partial`: lists it did not fill are `null`, never empty, and pending restart and role detail are not established from it. **Denied is never shown as zero**: without `get pods/proxy`, each tab keeps what Kubernetes says and names the missing grant for the live part. These reads carry no query text. When a refresh fails while earlier data is on screen, the page says so with the data's age, so cached values are never read as current.

## Storage

`GET /api/cnpg/clusters/{ns}/{name}/storage` backs the **Storage** tab; `GET /api/cnpg/disk` fills the fleet's **Disk** column with each cluster's fullest measured volume.

| Fact | Source | When it is not known |
|---|---|---|
| The instance's volumes | Claims labelled `cnpg.io/cluster` **and** owned by the Cluster's UID, naming an instance, by `cnpg.io/pvcRole` | "No access (needs list persistentvolumeclaims)". A labelled claim the Cluster does not own is listed as not counted |
| Requested, capacity, resize | The claim's request, capacity and resize conditions; the Cluster's `*PVC` status lists | `—` |
| Expansion allowed | The StorageClass's `allowVolumeExpansion` (unset = not allowed) | "expansion unknown" |
| Used space | Prometheus `kubelet_volume_stats_*` per claim, behind the PVC chart's grant | "Used space unknown" with the reason; never 0, never a value merged across clusters. A local volume's figure is labelled as the shared filesystem's |
| WAL on disk | Exporter `cnpg_collector_pg_wal` | "unavailable" with the pods/proxy state |
| Waiting to archive | The instance manager's `readyWalFiles` | Without a destination: "Not archived: no destination configured" |
| Held by replication slots | Slots from the instance manager; retained bytes from the exporter | "retained WAL not reported"; "No slots" only from a list that was read and is empty; bytes are a lower bound when some readings are missing |

The three WAL measures overlap and are shown side by side, never added up. Disk findings (≥ 80 % warning, ≥ 90 % critical) come only from a measurement, reach Needs attention only for callers who received it, and are not Issues-engine issues.

**Expanding.** The view names the field each size is declared in and whether the class allows expansion. **Edit size…** shows the GitOps write guard for that field, then Radar's apply flow with a manifest carrying only the new size (tablespaces carry the whole list, which the CRD does not merge by key). The operator resizes existing claims only when the class allows it and never shrinks them; Pending claims keep their original request. Restore advice appears only with recorded recovery evidence.

**Backlog, not lag.** A standby's catch-up is its **replay backlog in bytes**: the primary's current LSN minus the standby's replay LSN. PostgreSQL's `write_lag`, `flush_lag` and `replay_lag` are acknowledgement delays for recent WAL, empty when idle, and are never shown as catch-up time; the switchover dialog warns only from the backlog. The fleet's lag is Prometheus `cnpg_pg_replication_lag`, the time since the standby last replayed, so it keeps the word "lag". Pending restart is runtime-derived and never enters the Issues engine.

## HA and instances

`GET /api/cnpg/clusters/{ns}/{name}/ha` backs the instance cards, the HA section, Certificates, the Serving verdict and tab marks, the switchover dialog and the operation tracker. Reading the Cluster never implies the rest: each part is authorized on its own and reports `ok`, `denied` (with the grant), `notFound`, `notInstalled`, `unavailable` or `error`. Each section folds to one summary line and opens when something in it is out of line; a calm summary names only what is known.

| Fact | Source | Gate | When it is not known |
|---|---|---|---|
| Instances: node, QoS, running vs desired image, start times | Instance Pods | `list pods` | "Pods not readable" |
| Zone | `topology.kubernetes.io/zone` on each Node | cluster-scoped `get nodes` | Zones unknown, never "single zone" |
| Failover quorum | `FailoverQuorum` (1.27+): the recorded sync configuration, not the operator's verdict; shows whether R + W > N | `get failoverquorums` | Not installed, or not found (quorum failover off) |
| Disruption budgets | PDBs owned by the Cluster | `list poddisruptionbudgets` | `enablePDB: false` is the declared state, not a fault |
| Primary Lease | `Lease` of the Cluster's name (1.30+) | `get leases` | Absent before 1.30, not a fault |
| Operator leader | `db9c8771.cnpg.io` Lease in the operator's namespace | `list deployments` + `get leases` | Operator namespace unknown |
| Cluster Jobs | Jobs owned by the Cluster, by `cnpg.io/jobRole` | `list jobs` | "No access to Jobs" |
| Read-write endpoints | Ready EndpointSlice Pods of `<cluster>-rw` | `list endpointslices` | Serving falls back to primary Pod readiness, and says so |
| Certificates | `status.certificates.expirations` with renewal owner: operator-generated (CNPG renews) or named in `spec.certificates` (you renew); Secret **metadata only** | `get secrets` (metadata client) | "issuer unknown" |
| Maintenance | `spec.nodeMaintenanceWindow` | — | — |

HA readiness compares `spec.instances` with the expected instances: `status.instanceNames` plus instances on verified Cluster-owned pending or active join/initdb Jobs. A missing Pod is an observation, not proof that it never existed. When the instance Pods show fewer ready than `status.readyInstances`, or `status.currentPrimary` is not the Pod labelled primary, both values are shown and raised as a problem instead of one winning silently.

The four dimensions each come from their own source and read **unassessed** when it is unavailable. A replica cluster's Replication never reads healthy: Radar does not assess its feed from the source cluster.

Certificate expiry is also an Issues-engine finding. A certificate you renew warns under 30 days and is critical under 7; an operator-managed one raises only once renewal is overdue (under a day — CNPG renews at 7 days, so warning earlier would light every cluster for a third of each 90-day lifetime). Expired is critical.

## History

`GET /api/cnpg/clusters/{ns}/{name}/history?range=15m|1h|6h|24h` backs **Performance › History**; `GET /api/cnpg/fleet-metrics` fills the fleet's lag and volume growth.

**Sustained lag.** Lag becomes a problem only when every replay-lag sample Prometheus recorded for the worst standby over the last 10 minutes was at least 30 s (warning) or 5 min (critical), and that standby was already reporting at the window's start, so one that appeared a minute ago never qualifies. It is worded as a lower bound over the recorded samples. A spike is shown, not raised; volume growth is never raised.

**Queries.** Only server-built PromQL runs; nothing from the request reaches a query except the range name. Duplicate scrapes are collapsed with `max by (pod, …)` before any sum, and each chart names its source and its sample coverage.

**Isolation.** Instances are selected by `namespace` and `pod=~"^<cluster>-[0-9]+$"`. The `cluster` label is deliberately not used: shared Prometheus setups use it for the Kubernetes cluster, while CNPG scrape configs often relabel it to the CNPG Cluster name, so its meaning cannot be read from the series. Identity labels are added only when the operator configured them or kube-state-metrics Pod UIDs prove them. Without that proof, a Pod name seen under more than one identity refuses history (`ambiguous`), and proven labels that select nothing refuse it too (`scopeMismatch`) — never a value merged across clusters. Volume stats are scoped the same way over their own series, and the generic PVC usage gauge applies the same scope.

**Incarnations.** These series carry no Cluster UID, so history starts at the current Cluster's creation plus the query lookback, with a visible note while a new Cluster waits for that window. Reused Pod or PVC names never inherit a predecessor's samples.

**Unknown.** Each chart has its own state: `noSeries` (not scraped, never zero), `empty`, `denied`, `error` or `notRead`. Steps without a sample are hatched. A per-instance chart names each current instance it has no line for, so a stopped standby does not vanish from the chart about it.

**Without Prometheus**, History samples while the page is open, labelled "since this page opened". A rate is taken only between two readings of the same primary Pod (name and UID): a change of primary, a counter that went down or a missed read is a gap, never a value. Rates use the exporter's `cnpg_last_update_timestamp` when present, since CNPG 1.30 caches query results; otherwise they use Radar's fetch time and say "approximate".

**Intervals.** Selecting an interval opens Logs and Activity for it. Logs read each instance **from the start of the interval** — the pod log API has no upper bound, so a tail would return the lines nearest now — including the previous container run when the interval spans a restart.

**Access.** `get clusters`; CNPG series need the Prometheus pod-metrics grant (`get pods` in the namespace), and the volume chart `list persistentvolumeclaims` plus the PVC chart grant. Without Prometheus the response is `source: "none"` with the reason.

## Actions

Capabilities (`GET .../capabilities` on Clusters, ScheduledBackups and Poolers) return the facts a confirmation is bound to and each action's verdict: allowed, or a reason naming the missing grant or the blocking state. Each action has its own state guard. There is deliberately no shared "no ready instance" refusal: lifting a fence, rehydrating and restarting are the way out of exactly that state. Actions are POSTs to `.../actions/{action}`, made with the caller's impersonated client.

| Action | Write | Grant |
|---|---|---|
| Back up now | Create a `Backup` with an explicit method (plugin only when the plugin reports backup capability) | `create backups` |
| Switchover / promote | Status patch of `targetPrimary`, as `kubectl cnpg promote` does. A candidate the live read shows not streaming from the current primary is never the default, and choosing it warns | `patch clusters/status` |
| Restart (rolling) | Annotation `kubectl.kubernetes.io/restartedAt` | `patch clusters` |
| Restart one instance | Standby: delete the Pod with a UID precondition. Primary: a status phase write, as `kubectl cnpg restart` does. A standby is refused during a switchover or failover, and while its Pod is labelled primary but the status names another | `delete pods` / `patch clusters/status` |
| Reload configuration | Annotation `cnpg.io/reloadedAt` | `patch clusters` |
| Fence / lift fence | Annotation `cnpg.io/fencedInstances`, a JSON array (`["*"]` = all), with an explicit instance choice | `patch clusters` |
| Hibernate / rehydrate | Annotation `cnpg.io/hibernation` | `patch clusters` |
| Node maintenance set / lift | Merge patch of `spec.nodeMaintenanceWindow`, as `kubectl cnpg maintenance` does | `patch clusters` |
| ScheduledBackup suspend / resume / edit schedule | `spec.suspend`; `spec.schedule`, validated with the operator's parser (400 `invalid_schedule`) | `patch scheduledbackups` |
| ScheduledBackup run now | Create a `Backup` copying the schedule's settings; needs the target Cluster's destination for that method | `create backups` |
| Cancel / terminate a backend | `pg_cancel_backend` / `pg_terminate_backend` via psql, only while the pid's `backend_start` still matches | `create pods/exec` |
| Destroy instance (standby) | As `kubectl cnpg destroy`: the PVCs detached or deleted, then the Pod, then its Jobs | `delete pods`, `list`/`delete` (or `update`) `persistentvolumeclaims`, `list`/`delete jobs` |
| Pooler pause / resume | `spec.pgbouncer.paused` | `patch poolers` |

**Fencing.** Lifting one instance while `["*"]` applies is refused (409 `all_fenced`) unless the request converts the fence to a list (`convertFromAll` with the `remaining` instances). Lifting is refused while an offline volume-snapshot Backup runs, because the operator fenced the instance for it and lifts that fence itself, and, for a Cluster that declares volume snapshots, while its Backups cannot be listed. Fencing by name is refused for the instance a switchover or failover is promoting, for an instance whose Pod is labelled primary while the status names another, and for one whose Pod cannot be read, since its role cannot be verified. `["*"]` stays available.

**Destroy instance** refuses the primary, and the instance **must be fenced**: the operator never promotes a fenced instance, so no failover can make it primary between Radar's last check and a delete. CloudNativePG offers no lock, and upstream `kubectl cnpg destroy` does not check at all. Before every PVC, Pod and Job step, the Cluster and the Pod's role label are re-read, and any change stops the sequence. Kept PVCs record the detaching Cluster's UID (`radar.skyhook.io/detached-cluster-uid`), and a detached PVC belongs to an instance only when it carries this Cluster's UID.

**Operator webhook.** While the operator's webhook rejects writes, actions that write through it — a new Backup, or a patch of the Cluster or a ScheduledBackup — are refused with the reason. Status-subresource patches, Pod deletes and exec do not go through the webhook and stay allowed.

**Binding.** Every request carries the facts the user reviewed: kube context, UIDs, current and target primary, fencing and hibernation values, and generations where the spec matters. The server re-reads them and returns **409** (`context_changed` or `changed`) if anything changed; disruptive actions are never retried automatically. An error `code` means the write was refused before it took effect, except `outcome_unknown` and `partial`. `partial` means a multi-step action (destroy instance) stopped after some writes took effect; `completed` lists them.

**Uncertain writes.** While a write is in flight, or after an `outcome_unknown` or `partial` result, the target stays locked against another write, across dialog dismissal, context changes and reconnects; inspect the object and reload before reviewing again. A response without a code is unknown on 500, 502, 503 and 504: an Envoy-based ingress (Istio, Contour, Gateway API implementations) answers 503 after forwarding a write whose upstream connection reset. Radar's own refusals before any write carry the code `unavailable` and stay retryable. A Backup create that timed out is resolved by re-reading the same Backup name, never by creating another.

The confirmation dialog (`ActionConfirmDialog` in k8s-ui) leads with the effect, keeps the literal API writes in an expandable section, and requires typing the name for disruptive actions (switchover, primary fence, lifting a fence, cluster restart, hibernate, cold backups).

### Operation tracker

A successful POST means the write was accepted, not that it happened. The dialog hands off a tracked operation, bound to the kube context, the Cluster UID, the target and a baseline, which the Cluster header shows until it finishes: `requested → observed → progressing → completed | failed | stalled | superseded | unobservable`.

- A step Radar cannot see keeps the operation **unobservable**, never completed. A source not fetched successfully since the operation started is withheld from the observer, so a cached answer from before the action never certifies it.
- **Stalled** needs telemetry showing no movement for 10 minutes. A running Backup holds its phase for as long as the data takes, so only one the operator never picked up can stall.
- Completion per kind: **switchover**, the target is primary, the old primary streams again and the `-rw` endpoints point at the target (unreadable endpoints end it unobservable); **restart**, each instance recreated or restarted, except those fenced when it was requested; **fence**, recorded on the Cluster, then unobservable, because CloudNativePG reports no shutdown signal; **lift**, readiness and streaming; **hibernate / rehydrate**, the condition and ready instances; **backup**, the Backup's phase; **reload**, no completion signal, said so.
- Records live in the tab's `sessionStorage` (50 at most), keyed by the Radar API base, context, namespace, name and Cluster UID. In Radar Hub every in-cluster Radar reports the context `in-cluster`, so the API base keeps two clusters apart, and a recreated Cluster never inherits an operation. **Copy handoff** shares the subject, the checks and a context-pinned link, not the record itself.
- New kinds register an observer with `registerCNPGOperationObserver`.

## Guided backup setup

A Cluster's **Backups → Set up backups** joins three tasks: attach existing archive storage, create or repair a matching schedule, then verify uploads and a new successful base backup. Each write has its own review; a saved declaration is not a successful upload or a tested restore.

- **Attach** an existing same-namespace Barman ObjectStore by patching `spec.plugins`: other plugins are kept, the Barman WAL archiver is enabled, and the server name is explicit. It does not create stores or credentials, install the plugin, or migrate an existing archive destination; that needs a separately reviewed YAML migration. The server dry-runs the patch as the caller and refuses a known collision with another archive user of the same destination and server, and the user acknowledges that the identity is reserved. Reviewed digests of the Cluster and ObjectStore bind the write.
- **Schedules** use strict create. A method repair changes only `spec.method` and the plugin name.
- **Times.** One readable watching operator Deployment with a literal, valid `TZ` supplies the declared clock. Anything else (ambiguous inventory, an indirect `TZ`, none) gives a labelled UTC estimate, never a claim about the running clock.
- **Grants.** Attaching needs `get clusters`, `get objectstores` and `patch clusters`, with archive users found by `list clusters`. Repair needs `get` and `patch scheduledbackups`, plus `get clusters` and `get objectstores`; creation needs `create scheduledbackups`.

## Restore

Restore starts from the Cluster's More menu, a Backup's **Restore from this backup** or an ObjectStore's **Restore a cluster from this store**. The dialog is review, not a gate: a target before the first recovery point, after the newest evidence or in the future is **warned about, never blocked**, and missing evidence is listed as a gap.

- **Recover to** the latest archived WAL, a point in time (the manifest always carries UTC), or the end of a backup. CloudNativePG restores a `bootstrap.recovery.backup` reference only for in-tree and snapshot backups, so plugin Backups restore through the ObjectStore with `recoveryTarget.backupID` pinned. A volume-snapshot Backup offers only the end of its snapshot, since WAL replay past it needs a WAL archive source the dialog does not configure.
- **What the source holds** counts only that source's evidence: Backups whose Cluster still points at the store and server, or whose recorded in-tree destination matches. WAL coverage requires a live Cluster archiving into that store and server; a backup-only plugin does not establish it. A backup recorded by a predecessor Cluster, or predating the current one, is rejected.
- **Copied from the source** (instances, image, storage, parameters, resources) is editable in the manifest. The manifest never carries `plugins` or `backup`: the new cluster has no archiving until configured, and must not reuse the source's `serverName`. Physical recovery does not upgrade majors; a changed or unknown image major needs an explicit matching image.

The manifest opens in the standard create dialog (server dry-run, strict create). Back keeps the YAML draft; changing the recovery source or time replaces the manifest only on an explicit **Replace manifest**. The capability is `create clusters` in the target namespace, refused while the operator's webhook rejects writes.

**Progress.** The submitted manifest decides whether it is a restore. The header tracker completes at a healthy phase with every instance ready, and the Overview shows **Restore in progress** from `GET .../recovery`. That panel counts only recovery Jobs (`full-recovery`, `snapshot-recovery`) and reads done once a full recovery Job completed or a restored instance is ready, so a later switchover, scale-up or failed join never reopens it; a snapshot recovery Job without a WAL source only puts files in place, so it does not count. Once done it collapses to **Next steps**: point applications at it (never marked done, since Radar cannot tell), record a validation note, and set up backups and WAL archiving.

**Restore validation.** The Backups tab first shows what Radar reads in the restored cluster: recovery state, the timeline history, databases, roles and table estimates. These describe what is there and are never matched against the declared target, since a later failover or switchover writes the same history. Someone then records what they checked, and the server writes the `radar.skyhook.io/restore-validation` annotation (format version 1) with their user name, the time and the source and restored Clusters' UIDs it read itself (`patch clusters`, behind the GitOps write guard). It reads "Recorded", never "passed".

## Report bundle

**Download report…** (Cluster More menu) returns a zip shaped like `kubectl cnpg report cluster`: the Cluster, its owned Pods, Jobs and PVCs, events about them, its Backups, ScheduledBackups, Poolers and ObjectStore, operator and plugin versions, and the live instance and storage snapshots Radar shows. `report.json` lists every item read, skipped or denied (with the grant), and the Secrets the cluster references by name. Only the Cluster read can fail the request; every other read is skipped and recorded.

- Secret values are never read. Credential patterns in environment values, commands, arguments and Event messages are redacted; serialized spec copies (`cnpg.io/podSpec`, `cnpg.io/podPatch`, last-applied) are removed; initialization and import SQL is always withheld.
- Pod logs are opt-in, and query text in PostgreSQL log records is removed unless also opted in, in JSON records and plain-text lines alike. The match is deliberately loose; a false positive only removes more.
- Ownership uses UIDs: Pods by their current Job's UID, Backups by Cluster incarnation, Events by the exact UID. Excluded Pods and Events are counted in the contents note; a predecessor Cluster's Backups are left out.

## Operator diagnosis

The Operator screen leads with **Current state**: clusters stuck on a CNPG-I plugin, beside that plugin's readiness; components not ready or scaled to 0; a leader Lease not renewed; a `Fail` webhook with no ready endpoint, which means every CNPG write is rejected; then restart history, where only a restart within the last hour is worded as current trouble; restarts are never raised as a problem. Per operator Deployment it adds the leader Lease, the namespaces it watches (from the container's `WATCH_NAMESPACE` env only: the operator reads its config ConfigMap after building its cache, so a value there does not scope what it watches), its webhooks, reconcile error counters from its metrics port through `pods/proxy`, recent events and a link to its logs. Each fact carries its own access state.

**The verdict on other pages.** `GET /api/cnpg/operator/status?namespaces=` says, per namespace, whether the watching operator is leading and whether a `Fail` webhook has no ready endpoint: `reconciling`, `notReconciling`, `unknown` or `notWatched`. An unreadable lease, or a `WATCH_NAMESPACE` Radar cannot resolve, makes it `unknown`, never "reconciling", unless another operator confirmed to watch the namespace is leading. An unreadable webhook adds an unknown note without changing the state. While it is `notReconciling`, the fleet and every Cluster page show a banner. Capabilities carry the same verdict.

## Inside PostgreSQL

**psql** opens the dock terminal in the instance's `postgres` container over the caller's own `pods/exec`, as `kubectl cnpg psql` does (local socket, `postgres` superuser). It is disabled, with the grant named, without `create pods/exec`.

**Blocking view.** `GET .../sessions` runs a fixed query (never built from input) with psql in the chosen instance, under a statement timeout, and lists only backends in a blocking relation as blocker → victim trees, with query text cut at 200 characters. Query text is shown because the same grant lets the caller open psql and read it. Cancel is offered before terminate.

**Diagnostic SQL** (sessions, restore checks, parameters) runs as the `postgres` superuser, so every statement first pins `search_path` to `pg_catalog`: an application schema's function with an exact-match signature would otherwise be chosen over a built-in. Read-only checks run with `default_transaction_read_only`, and user values travel as psql variables, never in the SQL text.

## GitOps write guard

Any write to an object a GitOps tool manages can be undone by the next sync. `POST /api/gitops/write-evidence` reports, for the paths an action writes, whether each is declared in `last-applied-configuration`, owned by a GitOps field manager (Argo CD, Flux, Helm), or covered by an ignore rule (Argo CD only with `RespectIgnoreDifferences`), plus the owner's sync policy. `evaluateGitOpsWriteGuard` in k8s-ui turns that into a warning, and `GitOpsWriteWarning` renders it, with an acknowledgement when a revert is likely. The copy never promises a change will stick. Status writes are described as fields ordinary GitOps sync does not manage.

The guard is shared: the CNPG actions, Set image and the investigation apply dialog use it (`useGitOpsWriteGuard` in `web/`).

## API

All routes are registered in `internal/server/server.go`; the handler files and their doc comments carry the detail. Each route uses the caller's own access, and a partial answer says what it withheld. Missing grants are structured objects (`{verb, group?, resource, subresource?, namespace?}`). The workspace, operator, catalog, activity and log reads also work against older Radars; the newer runtime, measurement and action contracts are behind the `cnpgWorkspace` feature flag, and guided backup setup behind `cnpgProtectionSetup`.

| Route (under `/api/cnpg`) | Returns | Gate | Handler |
|---|---|---|---|
| `GET /workspace` | Every CNPG kind with per-kind coverage, owner-validated instance and Job Pods, issues, schedule readings, `managedBy` | Per kind ([Access](#access)) | `cnpg_workspace.go` |
| `GET /operator`, `GET /operator/status` | Operator and plugin components with diagnosis; the per-namespace verdict | Each read the caller's own, with coverage | `cnpg_operator.go`, `cnpg_operator_status.go` |
| `GET /imagecatalogs/{ns}/{name}/clusters`, `GET /clusterimagecatalogs/{name}/clusters` | Clusters pinned to a catalog | `list clusters`; cluster-wide for a ClusterImageCatalog, since a view-filtered answer would wrongly say nothing uses it | `cnpg_handlers.go` |
| `GET /clusters/{ns}/{name}/runtime`, `GET /poolers/{ns}/{name}/runtime` | Live instance or PgBouncer data | Read access to the Cluster or Pooler, and `list pods`; a `pods/proxy` denial is a per-source state in a 200, not a 403 | `cnpg_runtime.go` |
| `GET /clusters/{ns}/{name}/storage`, `GET /disk` | Volumes, WAL facts and usage; the fleet's fullest volume | `get clusters`; claims, WAL and usage each with their own coverage | `cnpg_storage.go` |
| `GET /clusters/{ns}/{name}/ha` | HA facts | `get clusters`; each sub-read gated on its own | `cnpg_cluster_ha.go` |
| `GET /clusters/{ns}/{name}/history`, `GET /fleet-metrics` | Prometheus charts; fleet lag, WAL receivers, inactive slots, growth | `get clusters` (403); `get pods` for CNPG series | `cnpg_history.go` |
| `GET /clusters/{ns}/{name}/logs`, `.../logs/stream` | Merged instance logs, from Pods labelled `cnpg.io/cluster` **and** controlled by the Cluster's UID | `get clusters`, `list pods`, `get pods/log` | `cnpg_cluster_logs.go` |
| `GET /clusters/{ns}/{name}/activity` | Timeline of the Cluster and its children | Kinds the caller cannot list are dropped | `cnpg_cluster_activity.go` |
| `GET .../capabilities`, `POST .../actions/{action}` on clusters, scheduledbackups, poolers | Action verdicts; reviewed writes | Per action | `cnpg_actions.go`, `cnpg_pooler_actions.go` |
| `GET /clusters/{ns}/{name}/sessions`, `/restore-checks`, `/parameters`; `GET /poolers/{ns}/{name}/pgbouncer-state` | Blocking tree; restored-cluster checks; parameter values; PgBouncer state | Runtime's gate plus `create pods/exec`; a denial is a 200 with `state: "denied"` | `cnpg_sessions.go`, `cnpg_inspect.go`, `cnpg_pooler_actions.go` |
| `GET /clusters/{ns}/{name}/instances/{pod}/destroy-plan` | The PVCs and Jobs a destroy would touch | Read as the caller | `cnpg_destroy.go` |
| `GET /clusters/{ns}/{name}/recovery`, `POST .../restore-validation`, `GET /restore/capability` | Restore progress; the validation note; the restore verdict | `get clusters`; `patch clusters`; `create clusters` | `cnpg_recovery.go` |
| `GET /clusters/{ns}/{name}/report` | The report bundle | `get clusters`; every other read skipped and recorded | `cnpg_report.go` |
| `POST /clusters/{ns}/{name}/protection/preview`, `GET .../schedule-preview`, `GET /scheduledbackups/{ns}/{name}/method-preview`, `.../schedule-preview` | Guided-setup dry runs and schedule previews | Read as the caller; the protection preview also server-dry-runs the patch | `cnpg_protection.go`, `cnpg_schedule.go` |

## Testing

`make cnpg-demo` (read `scripts/cnpg-demo/README.md` first) produces every state the workspace distinguishes from frozen fixtures: WAL archiving failure, failed and unrecognized-phase Backups, failing declarations, a Pooler, both catalog kinds and an ObjectStore with a failing server. Its `runtime` mode adds what needs a live controller: real backups and a restore, load, a lock chain and Prometheus.
