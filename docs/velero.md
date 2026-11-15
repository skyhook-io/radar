# Velero views

Radar's Velero views answer two questions the per-kind pages cannot: **are the schedules still producing good backups**, and **where do the backups go**. They do not answer whether a namespace is protected or whether a backup would restore: those need the coverage model, which is not built.

## Where it lives

| View | Route | Answers |
|---|---|---|
| Overview | `/velero` | What needs attention, then every schedule: its state, next run, newest run and newest successful run |
| Storage locations | `/velero/locations` | Each location's availability and validation, the backups targeting it and the schedules writing to it |

Both are listed under the **Velero** category in the Resources sidebar, above the kind lists. Rows open the existing resource drawer; there is no Velero-specific detail page. The Schedule drawer adds a **Schedule activity** section (the same reading plus up to ten recent runs) wherever it opens, including from the kind list and search.

**The namespace filter does not apply.** Velero keeps its objects in its own namespace (`velero`, `kommander` on NKP, or wherever it was installed), while the filter selects application namespaces; applying it would empty the views. Every kind is still read only where the caller may list it, Radar's `--namespace-scope` still bounds the request, and a namespace the caller did not name is never reported as denied. More than one installation shows installation chips.

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
| Default storage location | A location with `spec.default`; else the server's `--default-backup-storage-location` | "unknown" when neither can be read |
| Validation interval | `spec.validationFrequency` (> 0; `0` disables); else the server's `--store-validation-frequency` | No overdue verdict |
| Backups targeting a location | Each Backup's `spec.storageLocation`, which Velero fills when it first processes the backup | A backup without one is "not yet assigned", never counted under today's default |

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
| Never runs | The cron has no future time | Yes |
| Skip pending | `spec.skipImmediately` is set; the controller moves its anchor on the next pass | No |
| Held | Due, and a run of the schedule is `""`, `New` or `InProgress` (the controller's skip rule) | When the run has not started for 10 minutes; a long `InProgress` run is the stalled-run Issue's |
| Late | Past the next run by 5 minutes, zone certain, Backups read, nothing holding it. Counts the scheduled times that passed; Velero does not catch up | Yes |
| Probably late | Past it, zone not certain: inferred zone by 2 hours, unknown zone by the cadence plus a day | Yes |
| Late or held | As Late, but the namespace's Backups are unread, so a hidden run may hold it | Yes |
| Possibly late | Past the estimated run within the uncertain margin | No |
| Due | Within the 5-minute grace | No |
| On schedule | Otherwise | No |

Storage locations need attention when **validation is overdue**: the interval is known and more than three intervals plus 5 minutes have passed since the last validation (Radar's threshold). An Unavailable location is already an Issue (`BackupStorageLocationUnavailable`).

Attention rows are not Issues: they do not reach `/api/issues`, MCP or Diagnose. Raising them as Issues is separate work that should reuse `pkg/velero`.

## Issues on these views

The Velero Issues the Problems surface shows, filtered to what this caller can read: the subject's kind must be read in its namespace, and so must every read the finding rests on. At most 50 per installation are returned, with the count before the cap; a withheld row is never counted or named.

## Limits

- **Completed is not restorable.** "Completed, not expired" is what Velero reports; it does not say the data is in the bucket or that a restore would succeed.
- **An uncertain zone is not daylight-saving safe.** A weekly schedule at a local hour skipped by a daylight-saving change can exceed the uncertain margin and read "probably late". Readable Deployments avoid it.
- **Runs are matched by schedule name.** A recreated or edited schedule's older runs still count as its runs.
- **Velero 1.16–1.18.** 1.16 and 1.17 do not count an empty-phase run as holding; Radar does.

## Testing

`make velero-demo` freezes every phase with the controller scaled to 0, so schedules genuinely go late; `30-schedules.yaml` adds a held schedule and one whose name is too long. `./scripts/velero-demo.sh live` lets a running controller submit a schedule's runs and refuse the long-named one. See `scripts/velero-demo/README.md`.
