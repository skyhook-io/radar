# CloudNativePG workspace

A task-shaped view over [CloudNativePG](https://cloudnative-pg.io/) (CNPG): which PostgreSQL cluster needs attention, why, and what to inspect next — without assembling the story from ten separate CRD lists. The per-kind renderers, issue detection and audit check it builds on are described in [integrations.md](integrations.md#cloudnativepg).

The workspace is read-only. It never writes to a cluster.

## Where it lives

CNPG stays inside **Resources**; there is no new global navigation item. When the `postgresql.cnpg.io` CRDs are discovered, the Resources sidebar's CloudNativePG group gains a **Workspace** block above its exact kinds:

| Destination | Route | Job | Detail home for |
|---|---|---|---|
| Overview | `/cnpg` | The fleet: every Cluster with instances, replication, protection, declarations and its top problem. Defaults to **Needs attention**. | Cluster |
| Protection | `/cnpg/protection` | Recovery evidence per cluster, failed backups (7 days), destinations, schedules. | Backup, ScheduledBackup, ObjectStore |
| Declarations | `/cnpg/declarations` | Databases, Publications, Subscriptions and managed roles by cluster; declared vs reconciled. | Database, Publication, Subscription |
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
| Replication | Pod readiness only | Always "lag unknown": readiness does not show whether a replica is streaming. Lag needs runtime data Radar does not read yet. |
| Schedule | ScheduledBackups targeting the Cluster (`spec.suspend` → suspended) | "No access to ScheduledBackups" when unreadable in that namespace |
| Destination | barman-cloud plugin `barmanObjectName`, in-tree `barmanObjectStore`, or volume snapshots | "No destination configured" |
| Last successful backup | Newest of: completed Backup CRs (7-day window plus the newest per cluster), ObjectStore `serverRecoveryWindow[...].lastSuccessfulBackupTime`, in-tree `status.lastSuccessfulBackup` (ignored for plugin clusters, where CNPG no longer sets it) — the winning source is shown | "None observed", or "No access to Backups" |
| WAL archiving | `ContinuousArchiving` condition | "Not reported" |
| Recovery window | ObjectStore `status.serverRecoveryWindow` for the cluster's server name | "Not reported" |
| Restore validation | A Cluster in the same namespace bootstrapped (`bootstrap.recovery`) from this cluster's store/server or one of its Backups, **with a ready instance** | "None recorded" (unknown tone) — Kubernetes records no restore tests, so this is never green. A matching cluster without a ready instance reads "Recovery declared in …". |
| ObjectStore upload health | **Inferred** from its user clusters' WAL archiving and recovery windows (ObjectStore has no status of its own) | "Unknown" |
| Declarations | `status.applied` (true / false / absent = pending); managed roles from `status.managedRolesStatus` (`reconciled`, `cannotReconcile`; anything else pending) | Pending, never failed |
| GitOps source | Argo CD / Flux labels and the Argo tracking annotation | "GitOps source not recorded" |
| Pooler pressure | — | "Not measured": needs PgBouncer metrics |
| ScheduledBackup cron | Shown verbatim | CNPG's cron is six-field (seconds first) and is never translated |

Problems come from Radar's Issues engine (the same detections as `/issues`) plus the audit's `cnpgNoDeclarativeBackup`, worded "No declarative backup schedule" because that is all it proves. A cluster **needs attention** when it has an issue of warning or worse on itself, an instance Pod, or an object that references it.

## Access

All data comes from `GET /api/cnpg/workspace`, authorized **per kind**: namespaced kinds use a cluster-wide `list` or fall back per namespace; `ClusterImageCatalog` needs a cluster-scope `list`. Each kind reports coverage (`full`, `partial` with the namespaces read, `denied`, `syncing`, `error`, `notInstalled`). Issues and audit findings are withheld where the underlying kind is not covered — Pod evidence only reaches callers who can list Pods. Denied namespaces are named only when the caller supplied the namespace list. A partial or denied kind makes the screen show a coverage notice, and its facts read "No access" rather than none.

`GET /api/cnpg/operator` reads operator and plugin Deployments (label `app.kubernetes.io/name=cloudnative-pg`, plugin Services labelled `cnpg.io/pluginName`) and the operator's config references. It ignores the namespace view filter (the operator lives in its own namespace), returns ConfigMap data only with `get configmaps`, and never reads Secrets.

Cluster logs (`/api/cnpg/clusters/{ns}/{name}/logs`) need `get pods/log`; Activity (`.../activity`) drops events for kinds the caller cannot list. Deleted child objects stay attributed to their Cluster because Radar records the owning cluster on timeline events at ingestion; history recorded before that is marked incomplete.

## Not in this version

Runtime data (replication lag, sessions, locks, WAL and slots via the instance manager or Prometheus), Pooler pressure, and operations (Backup now, Switchover, Restart, Hibernate, Restore). See `docs/plans/CNPG_WORKSPACE.md`.

## API

Every route is gated on the caller's own access, and a partial answer names what it withheld rather than shrinking silently.

- Workspace: `/api/cnpg/workspace` returns every CNPG kind (plus owner-validated instance Pods) with per-kind `coverage` (`full|partial|denied|notInstalled|syncing|error`, `partial` naming only in-scope denied namespaces), CNPG issues from the Issues engine and `cnpgNoDeclarativeBackup` audit findings, each withheld where the caller lacks coverage. Namespaced kinds follow the view filter and the capacity per-namespace `list` fallback; `ClusterImageCatalog` needs a cluster-scope `list`. Handler `internal/server/cnpg_workspace.go`
- Operator: `/api/cnpg/operator` returns the operator Deployments (`app.kubernetes.io/name=cloudnative-pg`) and plugin Deployments (served by Services labelled `cnpg.io/pluginName`) with image-tag version and readiness (`null` when unreported), plus the operator's ConfigMap/Secret/monitoring-queries references from its args and env. ConfigMap data only with `get configmaps`; the Secret is name-only, never read. Deployments and Services carry the workspace `coverage` states; deliberately ignores the namespace view filter (the operator lives in its own namespace). Handler `internal/server/cnpg_operator.go`
- Catalog reverse-lookup: `/api/cnpg/imagecatalogs/{ns}/{name}/clusters` and `/api/cnpg/clusterimagecatalogs/{name}/clusters` return the Clusters pinned to an image catalog, with the major each asks for and the image it actually resolved. Cluster-scoped catalogs are referenceable from any namespace, so the cluster-scoped route reads cluster-wide gated on `list clusters` — a view-filtered answer would report "nothing uses this" before an edit.
- Cluster logs: `/api/cnpg/clusters/{ns}/{name}/logs` (bounded snapshot) and `/logs/stream` (SSE, re-resolves instances every 5s) merge every instance Pod — label `cnpg.io/cluster` AND controller ownerRef to the Cluster's UID, never the label alone. Gated on `get clusters` + `list pods` + `get pods/log` before the Cluster lookup (404 after). `container` defaults to `postgres`, `tailLines` 200, `sinceTime` is converted to seconds and trimmed, `pod` must be a validated instance (400). Entries keep raw `content` and add `level`/`logger`/`message` parsed from the instance manager's JSON (`record.error_severity` wins over `level`).
- Cluster activity: `/api/cnpg/clusters/{ns}/{name}/activity?since=&limit=` reads the timeline store for the Cluster, its instance Pods (by owner) and CNPG children attributed by the retained `cnpg.io/cluster` label — `pkg/timeline.ExtractLabels` records it from the label or `spec.cluster.name` on CNPG-group objects and Pods, so deleted children stay attributed. K8s Event rows join by subject UID. Rows of a kind the caller can't `list` in the namespace are dropped; `oldest` is the namespace's retention floor and `attributionSince` the earliest labelled row — history before it cannot attribute deleted children.

## Testing

`make cnpg-demo` (read `scripts/cnpg-demo/README.md` first) produces WAL archiving failure, failed and unrecognised-phase Backups, failing declarations, a Pooler, both catalog kinds and an ObjectStore with a failing server — every state the workspace distinguishes, except successful restores.
