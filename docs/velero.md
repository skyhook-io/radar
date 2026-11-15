# Velero views

Radar's Velero views answer two questions the per-kind pages cannot: **are the schedules still producing good backups**, and **where do the backups go**. They do not answer whether a namespace is protected or whether a backup would restore: those need the coverage model, which is not built.

## Where it lives

| View | Route | Answers |
|---|---|---|
| Overview | `/velero` | What needs attention, then every schedule: its state, next run, newest run and newest successful run |
| Storage locations | `/velero/locations` | Each location's availability and validation, the backups targeting it and the schedules writing to it |

Both are listed under the **Velero** category in the Resources sidebar, above the kind lists. Rows open the existing resource drawer; there is no Velero-specific detail page. The Schedule drawer adds a **Schedule activity** section (the same reading plus up to ten recent runs) wherever it opens, including from the kind list and search.

**The namespace filter does not apply.** Velero keeps its objects in its own namespace (`velero`, `kommander` on NKP, or wherever it was installed), while the filter selects application namespaces; applying it would empty the views. Every kind is still read only where the caller may list it, Radar's `--namespace-scope` still bounds the request, and a namespace the caller did not name is never reported as denied. A caller who cannot list a kind cluster-wide is checked only in the namespaces where Radar holds objects of that kind (its watched namespaces when Radar itself watches namespace by namespace), so the cost does not grow with the cluster and does not depend on Radar listing Namespaces; such a caller's coverage always reads partial, so it never shows whether Velero exists where they cannot look. More than one installation shows installation chips.

## Data

- `GET /api/velero/workspace`: every installation the caller can read. Readings, not objects; every time is absolute, with `observedAt` saying when the readings were computed.
- `GET /api/velero/schedules/{namespace}/{name}/activity`: one schedule with its recent runs, for the drawer. 403 without `list schedules` there, 404 when it does not exist.
- Capability flag `veleroWorkspace`. An older Radar shows a "needs a newer Radar" note and the kind lists keep working.
- Rules: `pkg/velero` (pure, no Kubernetes calls). Service: `internal/velero`. Server adapter and handlers: `internal/server/velero_workspace.go`.

## Where each value comes from

| Value | Source | When it cannot be read |
|---|---|---|
| Schedule state | The Schedule, its Backups (by `velero.io/schedule-name`), and the installation's server configuration, against Velero's controller (`schedule_controller.go`, v1.18.2) | Backups unread: runs are `unknown`, and a missed run reads **late or held** |
| Next run | `cron.Next(max(lastBackup or creationTimestamp, lastSkipped))`, robfig/cron v3 as Velero uses it | The zone below decides whether it is exact |
| Zone | `CRON_TZ=`/`TZ=` in the expression; else the Velero server Deployment's literal `TZ` (none set → UTC) with its rollout complete | Inferred from the newest run's name (`<schedule>-YYYYMMDDhhmmss`, server local time): an estimate, prefixed `~`. Nothing: UTC, marked unknown |
| Newest run, newest successful run | The schedule's Backups Velero still keeps (TTL deletes older ones) | "Unknown" when the namespace's Backups are unread; never "none" |
| Default storage location | A location with `spec.default`; else the server's `--default-backup-storage-location` (an explicitly empty flag means no default, and a backup naming no location fails) | "unknown" when neither can be read |
| Validation interval | `spec.validationFrequency` (> 0; `0` disables); else the server's `--store-validation-frequency` | No overdue verdict |
| Backups targeting a location | Each Backup's `spec.storageLocation`, which Velero fills when it first processes the backup | A backup without one is "not yet assigned", never counted under today's default |

An `@every` schedule adds a fixed duration to its anchor, so its times are exact whatever the zone.

Server configuration counts only when its effective value is known: a `TZ` from `envFrom`/`valueFrom` or an argument built from `$(VAR)` leaves it unknown, and during a rollout the previous template may still be running, so nothing is taken from it.

## Schedule states

First match wins.

| State | When | Needs attention |
|---|---|---|
| Paused | `spec.paused` | No |
| Scheduling disabled | The server runs with `--restore-only` or `--disable-controllers=…schedule…` | No |
| Rejected | `FailedValidation` or `validationErrors` | Shown through its Issue (`ScheduleValidationFailed`) |
| Cannot run | Name longer than 63 characters: Velero writes it unshortened into a label, so every submission fails | Yes |
| Not validated | Phase is not `Enabled`; the controller validates on create | After 5 minutes |
| Unknown | Velero accepted a cron Radar cannot parse | No |
| Always due | The cron has no future time; Velero compares now against the zero time, so it submits a new run whenever the previous one has started | Yes |
| Skip pending | `spec.skipImmediately` is set; the controller moves its anchor on the next pass | No |
| Held | Due, and a run of the schedule is `""`, `New` or `InProgress` (the controller's skip rule) | When the run has not started for 10 minutes; a long `InProgress` run is the stalled-run Issue's |
| Late | Past the next run by 5 minutes, zone certain, Backups read, nothing holding it. Counts the scheduled times that passed; Velero does not catch up | Yes |
| Probably late | Past it, zone not certain: inferred zone by 2 hours, unknown zone by the cadence plus a day | Yes |
| Late or held | As Late, but the namespace's Backups are unread, so a hidden run may hold it | Yes |
| Possibly late | Past the estimated run within the uncertain margin | No |
| Due | Within the 5-minute grace | No |
| On schedule | Otherwise | No |

**A stopped server is named first.** When the Velero server Deployment can be read and has no running replicas (scaled to zero, or its pods cannot be created), it leads Needs attention, and late schedules and overdue locations say that is why. An unavailable but running pod does not count: it still processes schedules. A pod still terminating is not counted either; its controllers stop on SIGTERM, so it submits nothing new.

Storage locations need attention when **validation is overdue**: the interval is known and more than three intervals plus 5 minutes have passed since the last validation (Radar's threshold). An Unavailable location is already an Issue (`BackupStorageLocationUnavailable`).

The certain readings are also Issues, raised by the Problems surface from the same rules, so they reach `/api/issues`, MCP and Diagnose: a stopped server, and schedules that are late, cannot run, held or not validated, and overdue validation. Estimates ("probably late", "late or held") stay readings here. See the Velero section of [integrations.md](integrations.md). These views keep showing their own readings, which need no Pod or Deployment access, and do not list those Issues twice.

## Issues on these views

The Velero Issues the Problems surface raises, computed by the same detector (`issues.DetectVelero`) over the objects these views read. A row therefore exists only where the caller could read its kind in that namespace, and a Velero-only reader with no Pod or Deployment access still sees them; the Problems surface, which scopes by namespace access, may show such a reader fewer. Nothing else in the cluster is scanned to produce them. At most 50 per installation are returned, with the count before the cap; a row from an unread namespace is never counted or named.

## Limits

- **Completed is not restorable.** "Completed, not expired" is what Velero reports; it does not say the data is in the bucket or that a restore would succeed.
- **An uncertain zone is not daylight-saving safe.** A weekly schedule at a local hour skipped by a daylight-saving change can exceed the uncertain margin and read "probably late". Readable Deployments avoid it.
- **Runs are matched by schedule name.** A recreated or edited schedule's older runs still count as its runs.
- **Velero 1.16–1.18.** 1.16 and 1.17 do not count an empty-phase run as holding; Radar does.

## Testing

`make velero-demo` freezes every phase with the controller scaled to 0, so schedules genuinely go late; `30-schedules.yaml` adds a held schedule and one whose name is too long. `./scripts/velero-demo.sh live` lets a running controller submit a schedule's runs and refuse the long-named one. See `scripts/velero-demo/README.md`.
