# Velero demo cluster

```bash
make velero-demo                                  # create + populate (fixtures)
./scripts/velero-demo.sh live                     # same cluster, real backups
kubectl config use-context kind-radar-velero-demo
make restart                                      # point Radar at it
make velero-demo-status                           # inventory
make velero-demo-down                             # tear down
```

## Two modes, and which one to trust

| | `up` (default) | `live` |
|---|---|---|
| Velero controller | scaled to 0 | running, then stopped at the end |
| Object storage | none | MinIO in-cluster, real S3 |
| Status on the objects | hand-written into the fixtures | produced by Velero |
| Covers | all 13 phases, schedules, repositories, the plural collision | a real backup, a real restore, a real `PartiallyFailed`, a real `Unavailable` location, a genuinely stuck run |
| Verified by | `velero-demo.sh verify` | `velero-demo.sh live` runs its own assertions |

Use `up` for breadth — it is the only mode that shows all 13 phases at once.
Use `live` when a claim needs to rest on something Velero decided rather than
something this repository typed. **A fixture can be written to say anything**;
if a screen looks right against fixtures only, that is evidence about the
fixtures.

Both are the same kind cluster, so `live` is not a second thing to maintain —
it installs MinIO over the top and replaces the frozen set. `reset` goes back.

## Why `up` scales the controller to zero

A live controller with no bucket produces exactly one outcome: everything
`Failed` with a credentials error, which is the least interesting row in the
table. Freezing it instead lets the fixtures carry their own status, so all
thirteen phases are on screen at once — including the in-flight ones, which by
definition do not sit still on a working cluster.

So `up` installs Velero, **scales the controller to 0**, and the fixtures carry
their own `status`. Nothing reconciles it away, and every phase in the enum
becomes reachable.

Two consequences worth knowing before you debug anything:

- **A plain `kubectl apply` writes `status` here** — none of the six Velero
  CRDs declare a status subresource. The familiar "apply drops status" rule
  only applies to kinds that register one, and reaching for
  `kubectl patch --subresource=status` against these fails with a confusing
  `NotFound`. (This bit during development, which is why it's written down.)
- **If you scale the controller back up, the fixtures dissolve.** Velero will
  reconcile the hand-written phases into whatever the real world says, which
  without a bucket is "everything failed".

## Coverage

### Backups — all 13 phases in the v1.18 enum (`40-backups-phases.yaml`)

| Phase | Level | Raises an issue? |
|---|---|---|
| `Completed` | healthy | no |
| `New` | unknown | **no** |
| `Queued` | neutral | **no** |
| `ReadyToStart` | neutral | **no** |
| `InProgress` | neutral | **not for the phase** — but yes once stalled (below) |
| `Deleting` | degraded | **no** |
| `Finalizing` | neutral | **not for the phase** — but yes once stalled |
| `WaitingForPluginOperations` | neutral | **not for the phase** — but yes once stalled |
| `Failed` | unhealthy | yes |
| `FailedValidation` | unhealthy | yes |
| `PartiallyFailed` | alert | yes |
| `FinalizingPartiallyFailed` | alert | yes |
| `WaitingForPluginOperationsPartiallyFailed` | alert | yes |

The `no` rows are the point of the file. A phase that starts raising an issue
*because of the phase* is the regression this fixture exists to catch — silence
is a requirement, not an absence of coverage.

The three in-flight rows carry the one exception, and it is about age rather
than phase: `InProgress`, `Finalizing` and `WaitingForPluginOperations` raise
`VeleroRunStalled` once they have sat longer than Velero allows a single
operation. The fixtures start ~31h ago, so on this cluster they do raise it —
that is the surface working. `Deleting` is deliberately excluded: its
`startTimestamp` belongs to the original run, so it can never be timed.

### Supersession (`61-`, `62-`, `63-`)

The rule under test: **a later successful backup clears an earlier failure in
the same schedule and namespace.**

| Fixture | Phase | `velero.io/schedule-name` | Expected |
|---|---|---|---|
| `nightly-20260806010000` | Failed | `nightly` | **superseded** — no issue |
| `nightly-20260807010000` | Completed | `nightly` | silent, and clears the above |
| `adhoc-preupgrade` | Failed | *(none)* | raises — ad-hoc backups are never superseded |

**Why these are three separate files applied with a pause.** Ordering is by
`creationTimestamp`, which the API server assigns — the fixture cannot set it.
If two backups land in the same second, ordering falls back to the name
tie-break, and the supersession test then passes for a reason that has nothing
to do with time. The script sleeps between them and `verify` asserts the
timestamps are distinct and correctly ordered, so this fails loudly rather
than silently passing.

### Schedules (`30-schedules.yaml`)

| Fixture | Covers |
|---|---|
| `sched-enabled` | healthy baseline, valid cron, `lastBackup` set |
| `sched-failedvalidation` | `FailedValidation` + an invalid cron (`not-a-cron`) |
| `sched-errors-no-phase` | validation errors with **no phase set** — synthetic (v1.18 always writes both), guarding the detector's defensive branch; the row must still be filterable |
| `sched-paused` | paused and otherwise healthy |
| `sched-paused-invalid` | **paused *and* rejected** — the row that used to read only "Paused" and gave no sign it was broken |
| `sched-held` + `sched-held-unstarted` | held behind a run Velero never started (`New`, no `spec.storageLocation`); the Velero views flag it once the run has been `New` for ten minutes, and count the run as "not yet assigned" a location |
| `sched-name-exceeds-…-by-one1` | a 64-character name: Velero cannot label its backups, so it never runs, although it validates |

With the controller at 0, every enabled schedule is genuinely late: nothing is
submitting its runs. The Velero views say so for `sched-enabled`, which is the
state a real cluster reaches when the Velero pod is gone.

`sched-errors-no-phase` is also the counter-example that stops "invalid cron"
being derived from the status: it is rejected for a *missing storage location*
while its cron is perfectly valid. Anything that reddens its cron cell is
pointing at the wrong field. Both halves of it are shapes v1.18 does not
produce — it validates only the cron, and always writes the phase alongside the
errors — so treat this row as a test of Radar's rendering, not as a record of
Velero behaviour.

### Storage and repositories

| Fixture | Covers |
|---|---|
| `default` BSL | `Available` |
| `dr-replica` BSL | `Unavailable` → the backup-target-unavailable issue category |
| `default` VSL | **no status at all** — the controller never populates one, which is why the VSL table has no status column |
| `repo-ready`, `repo-notready` | `Ready` / `NotReady`, two distinct values so the status filter affordance renders |
| `repo-legacy-restic` | a **restic** repository — what the Type column exists to make scannable |

Two distinct repository statuses matter: a column with one distinct value
renders no filter control at all, so a single-value fixture cannot exercise
the filter.

### Plural collision (`05-`, `70-`)

`rancher/backup-restore-operator` ships `restores.resources.cattle.io` — the
same plural as Velero. The real CRD is vendored here rather than mocked,
because the guard being tested is "select on the API group, not the plural"
and a mock proves much less.

`rancher-restore` must render through `GenericRenderer` with its own fields
(Backup Filename, Restore Completion Ts), show no Velero strings, and **not
render blank**. Blank is the specific trap: the kind is in `KNOWN_KINDS`, so a
group-gated renderer that doesn't match will also suppress `GenericRenderer`
unless the fall-through is wired.

## `live` mode: what Velero produces itself

`./scripts/velero-demo.sh live` installs MinIO as in-cluster S3, points two
BackupStorageLocations at it, deletes the frozen fixtures, and then makes Velero
do the work. Each state below is produced by an action, not written down, and
the script asserts each one before it reports success.

| State | How it is caused | What it exercises in Radar |
|---|---|---|
| `live-completed` — `Completed` | a real backup of the `demo-app` namespace into MinIO | item counts on the Backup renderer come from Velero's own `status.progress`, not a typed-in number |
| `live-restore` — `Completed` | `demo-app` is **deleted**, then restored from that backup | the Restore renderer, and the restore path end to end — the script checks the ConfigMap exists again afterwards |
| `live-partial` — `PartiallyFailed` | a pre-backup hook exits non-zero, with `onError: Continue` so the rest of the run finishes | the partial-failure phase, and the messages view — it carries a real error count with a real message behind it. Reachable here only because there is a bucket; `up` has none, which is why it hand-writes this phase |
| `dr-replica` — `Unavailable` | a second location is backed by its own bucket, which is then deleted with `mc rb` | the BSL renderer's unavailable state, decided by Velero's own validation loop |
| `live-stranded` — `Completed`, in `dr-replica` | the backup is taken *before* that bucket is deleted | **the central claim.** A `Completed` backup whose storage is gone. The Backup's own status cannot see this; the location's "Stored Here" section and the backup-side warning are the only places it surfaces |
| `live-rejected` — `FailedValidation` | a backup aimed at `dr-replica` *after* its bucket is deleted | the rejection path, with Velero's own `validationErrors` attached. A pre-run refusal, so unlike the phases above it needs no working storage of its own |
| `live-schedule` — real runs, then late | a `*/2` schedule into the healthy location: the running controller submits and completes a run, then the stall step stops the controller | the Velero views' schedule reading against real `lastBackup` and runs, and the late state once nothing submits |
| 64-character schedule — `Enabled`, never runs | created alongside `live-schedule`; the script checks it validated and never submitted | the views' "cannot run" state: Velero writes the name into a label, which cannot hold 64 characters |
| `live-stalled` — `InProgress`, not moving | a pre-backup hook execs `sleep 600` into the workload, `itemOperationTimeout` is set to `1m`, and the controller is scaled to 0 mid-run | the stall detector, against a run that has outlived **the timeout it declares** with a real `startTimestamp` — no clock is edited anywhere. `InProgress` is deliberate: it is the phase Velero puts no limit on, so it exercises the wording that reports elapsed time rather than a breach |

Three details in there are load-bearing, and each one breaks the run if changed:

- **The backup hook, not a race.** A four-object backup finishes in about a
  second, so scaling the controller down while the run is still `InProgress` is
  a race that is lost about half the time. The hook holds the run open for as
  long as the state needs.
- **Order: break the DR storage, use it for the rejection, stall last.** Anything that restarts
  the controller decides the stalled run: bring it back while a run is past its
  budget and it marks that run `Failed`. The stall step therefore runs last, on
  the location that is still healthy, and leaves the controller down.
  `live-rejected` sits between the two because it needs the broken location the
  stranded step creates.
- **Delete the DR bucket, don't stop MinIO.** Both locations share one MinIO, so
  stopping it marks *both* `Unavailable` — after which no backup can even be
  validated, and the run ends in `FailedValidation` instead.

`live` also proves the download path before it stops the controller: it creates a
`DownloadRequest` for `live-partial`'s results, checks the URL Velero signs is
one the host can actually reach, and fetches and parses the file. That is the
chain behind the "Show the messages" button on a Backup or Restore — Velero
controller, pre-signed URL, MinIO — and it is checked from outside the cluster,
where Radar runs.

**The two states are mutually exclusive, and that is not a bug.** A stalled run
needs the controller stopped; serving a `DownloadRequest` needs it running. The
script proves the download path first and then stops the controller, so the
cluster it leaves behind has the stalled run and a message button that correctly
reports why it cannot answer. To see the button succeed, scale Velero back up —
at the cost of the stalled run, which the controller will then decide.

    kubectl -n velero scale deploy/velero --replicas=1

The controller is left scaled to 0 at the end. That is what keeps the stalled
run stalled, and it is a state real clusters reach on their own.

## Timestamps don't rot

Fixtures store `@now±Nm` tokens rather than absolute dates; the script expands
them at apply time. Without this, a demo recorded today renders as "expired 8
months ago" next year, and the Expires / Last Backup / Age columns stop
demoing anything.

## What `up` cannot cover

With Velero scaled to 0 there is no reconciliation, so the fixture set alone
cannot produce any of the following. `live` covers the last of them; the first
two are out of reach in both modes.

- **Data mover (`DataUpload` / `DataDownload`)**, which is what data-mover
  support would need. These are emitted by a running controller during a real
  CSI-snapshot backup. Reproducing them needs, at minimum, in-cluster object
  storage (MinIO) *and* a snapshot-capable CSI driver (`csi-hostpath-driver`;
  kind's default local-path provisioner cannot snapshot) *and* the node agent
  enabled *and* a workload with a bound PVC. That is a different and much more
  fragile fixture, not a flag on this one — which is why it isn't one. Faking
  `DataUpload` CRs by hand would test our renderer against our own guess at the
  shape rather than against Velero, and the shape is the thing in doubt.
- **Progress counters ticking mid-run, and full backup logs.** These need a
  running controller and, for the logs, a `DownloadRequest` against object
  storage. The *final* item count is not in this list: `live` takes a real
  backup and asserts the count came from Velero.
  The *messages* behind a run's error and warning counts are no longer in this
  list — `live` fetches them, and the "Show the messages" button on a Backup or
  Restore is what reads them. Only `up` cannot: it has neither.
- **Restore-from-backup flows**, which need a real backup in a real bucket.
  **This one `live` does cover** — it installs MinIO, backs `demo-app` up into
  it, deletes the namespace and restores it. Use `live` when the restore path
  is what you are checking.

The first two need more than a bucket. Data mover additionally needs a
snapshot-capable CSI driver and the node agent; logs and per-item detail need a
`DownloadRequest` served by a controller that is still running, which is the one
thing `live` deliberately gives up at the end. Extending `up` to cover them
would cost the thing that makes it worth having: it runs offline in about two
minutes.
