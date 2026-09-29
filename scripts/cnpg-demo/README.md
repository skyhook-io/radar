# CloudNativePG Demo Cluster

Bootstraps a `kind` cluster with CloudNativePG installed and a curated set of
fixtures covering the states Radar's CNPG surfaces need to render correctly.
Use it for visual-testing CNPG UI changes, or for catching regressions across
badge / drawer / Issues / audit / filter in a single pass.

Without it you are testing against whatever cluster is in your current context
— often a customer or EKS cluster with one healthy Postgres and none of the
variety that matters.

## Quick start

```bash
# Prerequisites: kind, kubectl, python3
./scripts/cnpg-demo.sh up        # ~6 minutes on first run
./scripts/cnpg-demo.sh status    # inventory, and whether the operator is frozen

# Run Radar against it
kubectl config use-context kind-radar-cnpg-demo
./scripts/visual-test-start.sh

# When done
./scripts/cnpg-demo.sh down
```

## Read this before changing the fixtures

Three things about CNPG are not obvious, cost hours to discover, and determine
which states can be faked and which cannot. Each one fails in a way that looks
like success.

### 1. Freezing the operator is not enough to hold a status

Every Postgres pod runs an **instance manager** that writes cluster status
independently of the controller. Scaling `cnpg-controller-manager` to zero
stops the controller — it does **not** stop the instance managers.

The consequence is specific:

| Field | Survives with the operator frozen? |
|---|---|
| `status.phase` | **Yes** — nothing else writes it |
| `status.conditions` | **No** — rewritten from the pods, reverts in ~10 min |
| `status.readyInstances` | **No** — same |

So a hand-patched `ContinuousArchiving=False` looks correct, demos fine, and
quietly heals itself about ten minutes later. Anything built that way is a
fixture with a half-life. This is why the terminal phases below are patched and
the WAL failure is not.

Freezing also requires deleting CNPG's webhook configurations, because the
operator **serves its own admission webhooks** from the controller pod. At zero
replicas every write to a CNPG resource otherwise fails with a connection
error. `cnpg-demo.sh up` handles this.

Thawing is therefore not just scaling back up: the operator builds its PKI at
startup by looking up its own webhook configurations, so with them deleted it
fails `ensurePKI` and **crash-loops**. `thaw` re-applies the install manifest
first, which restores them and is idempotent.

### 2. Order matters twice, and both failures look like success

**A Pooler must not share a name with one of its cluster's Services.** CNPG
creates `<cluster>-rw`, `-ro` and `-r` Services for every cluster, and a Pooler
creates a Service of its own name. Name a Pooler `pg-healthy-rw` and it finds a
Service it does not own, then refuses to reconcile — forever — with
`invalid ownership for managed resources`. Nothing surfaces on the CR: no
status, no condition, no event. It just sits there looking slow.

**Backup objects must be CREATED after the freeze, not merely patched after
it.** A Backup created against a running operator is not just reconciled to
`running` — the operator *attempts* it, and one pointed at a cluster with no
backup configuration fails and stamps `LastBackupSucceeded=False` on that
cluster. The pristine baseline silently acquired a `Backup Failed` badge, via a
resource attached to it rather than anything wrong with it. `07-backups.yaml`
is therefore held back and applied after the operator is down.

Before a frozen cluster is thawed, the script deletes those three Backup
fixtures while the operator is still stopped. Otherwise a reused `up` or
`live` run would let the controller attempt them and permanently contaminate
the healthy baseline with `LastBackupSucceeded=False`.

Velero's Backup is the exception: no controller is installed for it, so nothing
reconciles it away and it can be patched at any point.

### 3. Make the failure real instead of asserting it

The durable way to get `ContinuousArchiving=False` is to let it actually
happen: point `spec.backup.barmanObjectStore` at an unroutable endpoint
(`192.0.2.1`, TEST-NET-1, which never answers), then give the archiver a
segment to fail on by writing some rows and calling `pg_switch_wal()`.

**But only after the cluster is up.** CNPG verifies the object store during
bootstrap, so a cluster created with an unroutable endpoint never finishes
coming up — it sits in `Waiting for the instances to become active` and never
reaches Ready. That still produces `ContinuousArchiving=False`, which is why it
is such a convincing wrong answer: the condition looks right on a cluster that
is simply broken. The script therefore creates the cluster clean, waits for
2/2, and only then attaches the store — and its success check requires **both**
`ContinuousArchiving=False` **and** 2/2 Ready, because asserting the condition
alone passes on the broken version.

The archiver then fails on its own, the instance manager reports it, and the
state is self-sustaining for as long as the cluster exists. It is also better
evidence — a real archiving failure rather than a hand-written condition.

The cluster stays **2/2 Ready and serving** throughout. That is the entire
point of the scenario: nothing about availability looks wrong, and the recovery
point is silently frozen.

## What's in the cluster

### Clusters — all 2/2 Ready, four different badges

The uniform instance count is deliberate. Every difference here is invisible to
counts, which is the class of bug this fixture exists to catch.

| Resource | Badge | What it exercises |
|---|---|---|
| `pg/pg-healthy` | `Healthy` | Baseline. Also one of the three clusters `cnpgNoDeclarativeBackup` reports — it has no backup configuration at all. |
| `pg/pg-wal-failing` | `WAL Archiving Failing` | The headline state. **Real** archiver failure (see above). Badge, drawer banner, `critical` Issue and the AI context must all agree while every pod is Ready. |
| `pg/pg-unrecoverable` | `Unrecoverable` | Terminal phase outranking instance counts: red badge on a 2/2 row, with Instances and Primary muted + tooltipped. Also the unresolvable-catalog case — see below. |
| `pg/pg-doomed` | `Unrecoverable` | A second terminal cluster so the Status filter renders a count of `(2)`. A dropdown where every option reads `(1)` cannot distinguish "counts work" from "counts are always 1". Its image comes from an ImageCatalog, so `spec.imageName` is empty and only `status.image` says what is running. |

### Everything else

| Resource | Kind | What it exercises |
|---|---|---|
| `pg/pg-healthy-pooler` | Pooler | Positive state is **Scheduled**, not Ready — `status.instances` counts pods being scheduled, not ready ones. Deliberately not named `pg-healthy-rw`; see below. |
| `pg/pg-nightly` | ScheduledBackup | `method: plugin`. The audit check must count all three method values; a default-method fixture would pass whether or not it is plugin-aware. |
| `pg/pg-backup-ok` | Backup | `completed` |
| `pg/pg-backup-wal-broken` | Backup | `walArchivingFailing` — a Radar-curated label, not a CNPG phase string |
| `pg/pg-backup-unknown-phase` | Backup | An unmapped phase, proving a future CNPG minor passes through **verbatim** rather than being asserted healthy |
| `pg/velero-nightly-cluster` | Backup (`velero.io`) | Shared `backups` plural — must render Velero's column set, not CNPG's |
| `pg/kb-mysql` | Cluster (`apps.kubeblocks.io`) | Shared `clusters` plural — must render the **generic** drawer: not blank, not a fabricated Postgres status |

The last two are load-bearing. With only CNPG installed, a guard that matches
everything and a guard that matches the right API group behave identically, so
the collision handling cannot be verified at all.

### Backup destinations — the plugin path

| Resource | Kind | What it exercises |
|---|---|---|
| `pg/pg-store` | ObjectStore (`barmancloud.cnpg.io`) | The barman-cloud plugin's own destination CR, wired to `pg-doomed` via `spec.plugins` (see below for why not `pg-healthy`). Its recovery window lives in `status.serverRecoveryWindow`, keyed **by server name** — two servers here, one of which has failed since its last success. The field names are `firstRecoverabilityPoint` / `lastSuccessfulBackupTime` / `lastFailedBackupTime`; guessing them yields `unknown field` on patch and an empty section in the UI. |
| `pg/pg-store-fresh` | ObjectStore | Declared, with an empty `status` — nothing has ever reported to it. The empty state has to read as "no backup yet", not as failure. |

`spec.backup.barmanObjectStore` (in-tree, deprecated in 1.26) and the plugin
`ObjectStore` are two different objects that answer the same question, and a
cluster can use either. Both are present so the UI cannot quietly cover only one.

`pg-store`'s window covers two servers while only `pg-doomed` appears under
**Used By**, and that is not an inconsistency: the window is keyed by the server
names the store holds backups *for*, and Used By lists the clusters currently
archiving *to* it through `spec.plugins`. A store keeps a decommissioned
cluster's backups until retention ages them out, so the two lists are expected to
differ.

**Why the plugin reference sits on `pg-doomed`.** `spec.plugins` is applied after
the freeze, so the operator never rolls the instance pods with the plugin's
sidecar — and every instance manager on that cluster then reports
`ContinuousArchiving=False — wal archive plugin is not available`. Put that on
`pg-healthy` and the baseline becomes a second WAL-archiving failure: two of the
four badges collapse into one, quietly, while every other assertion still passes.
`pg-doomed` is already terminal and a terminal phase outranks the condition, so
its badge does not move. `assert_healthy_baseline` now checks both backup
conditions on `pg-healthy` so this cannot come back unnoticed.

### Declarative objects — `status.applied` has three values

| Resource | Kind | State |
|---|---|---|
| `pg/demo-app` | Database | Applied. `retain` on delete. |
| `pg/demo-reporting` | Database | Applied, `delete` on delete — the destructive reclaim policy, which must read differently. |
| `pg/demo-broken` | Database | **Not applied**, with the operator's own error: the owner role does not exist. |
| `pg/demo-app-pub` | Publication | Applied, publishes all tables of `demo_app`. |
| `pg/demo-missing-pub` | Publication | Not applied — its database does not exist. |
| `pg/demo-app-sub` | Subscription | Not applied — subscriptions need an `externalCluster` on the destination cluster, which this demo does not define. |

All three `false` rows also appear on the **Issues** page with the operator's own
error — the failure is not only renderable, it is reported. The `true` and
absent rows raise nothing.

The axis that matters is `applied: true` / `false` / **absent**. Absent means the
operator has not reconciled it yet, and rendering that as failure condemns every
object in its first seconds.

These objects also name things by their PostgreSQL-side names (`demo_app`,
`demo_app_pub`), which are not the Kubernetes names of the CRs that declare them
(`demo-app`, `demo-app-pub`). Naming without resolving is the dead end the UI has
to avoid.

### Image catalogs

| Resource | Kind | What it exercises |
|---|---|---|
| `pg/postgres-pinned` | ImageCatalog | Namespaced, and actually used: `pg-doomed` resolves its image from major 17 here. |
| `postgres-fleet` | ClusterImageCatalog | Cluster-scoped, referenced from a namespace — the unscoped half of the lookup. Carries no major 15. |

`pg-unrecoverable` is pinned to `postgres-fleet` major **15**, which the catalog
does not list. That is CNPG's "incomplete or invalid image catalog", and it is
invisible from the cluster side — the reference looks fine there. The catalog
page is where it has to be visible.

That pin is applied **after** the freeze, by the script, for a reason: a cluster
created with an unresolvable catalog never gets an image and never reaches 2/2
Ready, so it cannot be a fixture. `up` over an existing cluster unpins it first —
`imageName` and `imageCatalogRef` are mutually exclusive and the CRD rejects a
manifest carrying both.

## What to check in one pass

1. **Clusters list** — four rows at 2/2, four different badges, every badge on one line, no clipped headers.
2. **Status filter** — options match the badges on the rows exactly, with `Unrecoverable (2)`. Confirms the filter reads the curated text rather than raw `status.phase`, which for CNPG is prose.
3. **`pg-wal-failing` drawer** — WAL banner, and the "otherwise serving normally" clause present (it is a healthy cluster apart from archiving).
4. **`pg-unrecoverable` drawer** — terminal banner; Instances/Primary muted with tooltips in the list.
5. **Issues** — WAL archiving `critical`; concurrent causes on one cluster appear as separate rows.
6. **Checks / audit** — `cnpgNoDeclarativeBackup` reports `evaluated 4 / passed 1`. Only `pg-wal-failing` passes, via its `method: plugin` ScheduledBackup — which is what makes the check demonstrably plugin-aware rather than merely asserted to be.
7. **Backups list** — CNPG columns (Cluster / Method / Started); the Velero row renders Velero's set instead.
8. **`kb-mysql`** — generic drawer, own sidebar group.
9. **`pg-store`** — a recovery window per server, each server name opening its cluster; `pg-wal-failing` flagged as failing since its last success. `pg-store-fresh` says no backup has been reported, not that something is wrong.
10. **`demo-broken`** — the operator's error verbatim; `demo-app-sub` reads as pending rather than failed.
11. **`demo-app-pub`** — "In Database `demo_app`" is a link that lands on the `demo-app` Database, despite the two names differing.
12. **`postgres-fleet`** — `pg-unrecoverable` listed under "pinned to a version this catalog does not list", with what that means. `pg-doomed`'s drawer names the catalog *and* the major, and still shows the image it is running.

## Live-operator variant

```bash
./scripts/cnpg-demo.sh live      # same fixtures, operator left running
./scripts/cnpg-demo.sh refreeze  # go back to the frozen rendering fixtures
```

The frozen path is optimised for **rendering** verification, and it is the one
that is proven. Work on failovers, switchovers, backup execution or anything
that needs a reconciling controller wants the opposite — so `live` skips the
freeze and the terminal phases with it. It also removes the three hand-written
Backup objects before the operator starts; otherwise the controller would
attempt them and contaminate the healthy baseline.

## Runtime mode

```bash
./scripts/cnpg-demo.sh runtime          # on the EXISTING cluster; ~8 min first run on an idle Docker VM, ~20 min on a busy one
./scripts/cnpg-demo.sh runtime-lag on   # pause WAL replay on one pg-runtime replica
./scripts/cnpg-demo.sh runtime-lag off  # resume it (on every replica)
./scripts/cnpg-demo.sh status           # includes a runtime section when present
./scripts/cnpg-demo.sh runtime-down     # removes pgrt + monitoring only
```

Everything the frozen matrix cannot show, because it needs a reconciling
controller **and** working object storage. Fixtures are in `runtime/` — a
subdirectory, so `up`/`live` never apply them.

| Resource | What it is |
|---|---|
| `pgrt/minio` + `ObjectStore/minio-store` | In-cluster S3 (emptyDir); the image creates bucket `cnpg` itself (`MINIO_DEFAULT_BUCKETS`) |
| `pgrt/pg-runtime` | 3 instances, PG 17, WAL archived by the barman-cloud plugin → `ContinuousArchiving=True` for real |
| `pgrt/pg-runtime-hourly` | ScheduledBackup, `method: plugin`, `immediate: true` → a Backup the operator actually took |
| `pgrt/pg-runtime-restore` | 1 instance, `bootstrap.recovery` from `minio-store` / `serverName: pg-runtime` |
| `pgrt/pg-runtime-pooler-rw` | PgBouncer, transaction mode, 2 pods |
| `pgrt/pg-load` | pgbench through the Pooler, ~5 tps (`application_name=radar-pgbench`) |
| `pgrt/pg-lock-holder` / `pg-lock-waiter` | `idle in transaction` holding a row lock on `radar_lock_demo`, and an `UPDATE` blocked on it |
| `monitoring/prometheus-server` | Scrapes instances `:9187` (job `cnpg-instances`) and poolers `:9127` (job `cnpg-poolers`), 15s, keeping `namespace`, `pod`, `cluster` (+ `pooler`) |

**Why the operator must be live.** Every runtime state is produced by the
controller: it injects the plugin sidecar at pod creation, runs the immediate
backup, bootstraps the restore, and reconciles the Pooler. `runtime` therefore
thaws a frozen operator the same way `live` does (frozen-only Backup fixtures
deleted first). The `pg` terminal phases do not survive that — `refreeze`
restores them, after which pgrt keeps archiving (the sidecar talks to the
instance manager, not the operator) but gets no scheduled backups or failover.

**How Radar finds the Prometheus.** `monitoring/prometheus-server` is an exact
entry in `WellKnownLocations` (`pkg/prom/discovery.go`), so discovery is a
targeted Service GET, not the scored dynamic scan; off-cluster Radar
port-forwards to it. Its only port is 9090 so the "first port" rule is
unambiguous. The frozen `pg` instances are scraped too.

### Constraints, all of which fail quietly

- **Order: archiving → backup → restore.** The ScheduledBackup is applied only
  after `ContinuousArchiving=True`: a backup started earlier is refused
  (`walArchivingFailing`) and `immediate: true` spends its one immediate run on
  that. The restore is applied only after a Backup is `completed` — before that
  the store has WAL and no base backup.
- **A new pg-runtime needs an empty bucket.** It checks its WAL destination is
  empty before archiving, and against a previous pg-runtime's archive it never
  becomes Ready. `runtime` restarts MinIO (emptyDir → empty bucket) whenever
  pg-runtime does not yet exist; an existing cluster keeps its bucket.
- **Not `minio/minio`.** MinIO stopped publishing images; the Docker Hub and
  quay.io tags (including the one `velero-demo` pins) no longer resolve. The
  fixture pins `bitnamilegacy/minio`.
- **The restore does not archive.** Archiving it under `serverName: pg-runtime`
  would write into the archive it restores from.
- **Pooler naming** — same rule as above: not `pg-runtime-rw`.
- **The lock holder is a psql reading a pipe**, which leaves the backend `idle in
  transaction` rather than `active` (a `pg_sleep` would read as active). It
  heartbeats `SELECT 1` every 5 min so a failover is noticed; `state_change`
  resets then, `xact_start` does not.
- **It is heavy for kind.** Runtime adds 4 Postgres pods (each with a plugin
  sidecar), MinIO and Prometheus to the 8 frozen-fixture instances. On a shared
  Docker VM that is already busy (other kind clusters, ~8 GB), liveness probes
  on `:8000` time out and restart instances, the kube-controller-manager loses
  leader election, and the CNPG operator crash-loops on
  `connection refused` to the API server — so every CNPG write fails at the
  webhook. `rt_apply` retries through short outages. It cannot fix a VM that
  is oversubscribed: scale `pg-load` to 0, or delete `pg-runtime-restore`
  after verifying it (`runtime` re-creates it from the completed backup).
- **Replay pause is not persisted.** A replica restart (or failover) resumes
  replay; re-run `runtime-lag on`.

### Verify

```bash
C="--context kind-radar-cnpg-demo"
kubectl $C -n pgrt get clusters,backups,poolers
kubectl $C -n pgrt get cluster pg-runtime -o jsonpath='{.status.conditions[?(@.type=="ContinuousArchiving")].status}'
kubectl $C -n pgrt exec pg-runtime-1 -c postgres -- psql -U postgres -d app -c \
  "SELECT application_name, state, wait_event_type, pg_blocking_pids(pid) FROM pg_stat_activity WHERE application_name LIKE 'radar-%'"
kubectl $C get --raw /api/v1/namespaces/pgrt/pods/https:pg-runtime-1:8000/proxy/pg/status   # replicationInfo
kubectl $C -n monitoring port-forward svc/prometheus-server 9090 &
curl -s 'localhost:9090/api/v1/query?query=up' | python3 -m json.tool
```

(`pg-runtime-1` is the primary on a fresh run; after a switchover use
`.status.currentPrimary`.)

## Notes

- `CNPG_VERSION` is pinned to 1.27.0. Radar matches CNPG's phases on **equality**
  against full English sentences from `api/v1/cluster_types.go`, so bumping the
  version can silently move a cluster into the unrecognised-phase bucket. Bump
  deliberately, then re-check the badges.
- `CLUSTER_NAME=foo ./scripts/cnpg-demo.sh up` uses a different cluster.
- `up` is idempotent and re-runnable; it thaws a frozen operator first so
  fixture edits can be applied, then re-freezes.
