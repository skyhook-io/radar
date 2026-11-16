# Velero views

Radar's Velero views answer two questions the per-kind pages cannot: **are the schedules still producing good backups**, and **where do the backups go**. They do not answer whether a namespace is protected or whether a backup would restore: those need the coverage model, which is not built.

## Where it lives

| Screen | Route | Answers |
|---|---|---|
| Overview | `/velero` | Is Velero working: the server, schedules, newest successful scheduled backup and storage in one line, then what **Needs attention**, grouped by server, schedules, storage, and backups and restores |
| Schedules | `/resources/schedules?apiGroup=velero.io` | Every schedule read against Velero's controller: its state, next run, last submission and newest run, with its newest success when that run wasn't one |
| Storage locations | `/resources/backupstoragelocations?apiGroup=velero.io` | Each location's availability and validation, the backups targeting it and the schedules writing to it |
| Backups | `/resources/backups?apiGroup=velero.io` | Every run, newest first and failed first when any failed: outcome, items, errors and warnings, where it is stored (and whether that location is available) and how long it is kept. A schedule's **All N runs** link narrows it to that schedule |
| Restores | `/resources/restores?apiGroup=velero.io` | Every restore: outcome, the backup it came from, the namespaces it restored into, errors and warnings |
| An object's page | `/velero/<backups\|restores\|schedules\|backupstoragelocations>/<namespace>/<name>` | See [Pages](#pages) |

**One view, then the kinds.** The Velero category in the Resources sidebar has a single view, **Overview**, because it is the one screen no kind's list can answer; its badge counts the rows Needs attention lists (issues the server capped still count, from the total it sends), reading ≥N over a kind read only in part and unknown (–) for a zero there. Below it come the Velero kinds as for any CRD. The **Schedule**, **BackupStorageLocation**, **Backup** and **Restore** kinds' lists are Radar's own tables rather than the generic one (the Backup and Restore lists keep a name search and Create), so none has a second sidebar entry and a schedule is never seen without its reading. The Overview's status line links to the Schedule and storage-location lists. A Radar without the views, or whose capabilities could not be read, keeps the generic tables; on the first capabilities load the page waits rather than flashing them. Rows open the resource drawer; the drawer's **Open full view** opens the object's page. The Schedule drawer adds a **Schedule activity** section (the same reading plus its five most recent runs) wherever it opens, including from search.

**The namespace filter does not apply.** Velero keeps its objects in its own namespace (`velero`, `kommander` on NKP, or wherever it was installed), while the filter selects application namespaces; applying it would empty the views. Every kind is still read only where the caller may list it, Radar's `--namespace-scope` still bounds the request (every Velero route answers 403 for a namespace outside it, before reading or creating anything), and a namespace the caller did not name is never reported as denied. A caller who cannot list a kind cluster-wide is checked only in the namespaces where Radar holds objects of that kind (its watched namespaces when Radar itself watches namespace by namespace), so the cost does not grow with the cluster and does not depend on Radar listing Namespaces; such a caller's coverage always reads partial, so it never shows whether Velero exists where they cannot look. The header names the namespaces Velero was read from ("Reading Velero in namespace velero"), and more than one installation shows installation chips.

## Pages

Each Backup, Restore, Schedule and BackupStorageLocation has a page at `/velero/<plural>/<namespace>/<name>`, Radar's detail view framed by Velero: the title line starts with where the object lives (`Velero / Backups /`), the page pins the Kubernetes context in `ctx` (after a context switch it says the object is not in the new one rather than opening a same-named object), and the open object is listed under the Overview in the sidebar. Velero objects linked from a page open on their own pages; anything else opens in the drawer. **Overview** is a composed summary, led by the object's operational issues; **Spec & status** is the kind's renderer, as in the drawer, which keeps the renderer.

| Page | Overview | Its own tab |
|---|---|---|
| Backup | Outcome and items backed up; what it selects, as declared (and whether resource or label filters narrow it); volume snapshots and plugin operations when Velero counted any; where it is stored and whether that location is available, missing (only when the locations were read in full) or unread; how long it is kept (past its retention, Velero deletes it at its next garbage collection); the schedule that made it and that schedule's state; **Restore: not verified**, since Velero never tests a restore; the errors and warnings behind its counts, unless its issue above already shows them | **Restores** made from it (`spec.backupName`) |
| Restore | Outcome and items restored; the backup it came from; the namespaces it selects, as declared (none means every namespace in the backup) and where they were mapped (unmapped namespaces keep their names); the policy requested for existing objects; its errors and warnings | — |
| Schedule | Schedule activity (the reading, next run and recent runs; **All N runs**, here and in the Schedule drawer, opens the page's Runs tab on all runs); what it backs up; where, and whether that location is available; how long each run is kept; the runs Velero keeps, by outcome | **Runs**: every run Velero keeps (by `velero.io/schedule-name`), failed first when any failed, with duration, items, errors and warnings and retention |
| Storage location | Availability and Velero's message; last validation against its interval; default (and where that answer came from); access mode; provider, bucket and prefix; the Backup objects that name it (not what is in the bucket); the schedules writing to it | **Backups** naming it, newest first |

Runs and stored backups are what Velero still keeps: each is deleted once its retention passes, so neither is a full history.

## Back up now

**Back up now**, in the Schedule drawer's header with the other actions, creates a Backup from the schedule's template, exactly as `velero backup create --from-schedule` does (`pkg/velero.BackupFromSchedule`): the template as its spec, the template's labels (else the schedule's) plus `velero.io/schedule-name`, the schedule's annotations except kubectl's last-applied, an owner reference when the schedule sets `useOwnerReferencesInBackup`, and the name `<schedule>-<UTC timestamp>`. The confirmation shows what it backs up, where it is stored and how long it is kept; a toast names the new backup, which also appears among the schedule's runs.

- **Unavailable when it would not back up now.** Disabled, with the reason as its tooltip, for a schedule that cannot run and while the Velero server is stopped (a Backup created then waits in `New` until the server starts). When the server's Deployment can't be read it stays enabled.

- **It is one of the schedule's runs.** Radar lists it among the runs, and while it is new or in progress Velero does not submit the schedule's next run (the controller's hold covers only those phases, not queued or finalizing ones); the confirmation says so.
- **As the caller.** The schedule is read and the Backup created with the caller's identity, so the apiserver enforces `get schedules` and `create backups`.
- **Bound to what was reviewed.** The confirmation freezes the schedule it shows. The request carries that `resourceVersion`, and a schedule edited since is refused (409); the dialog disables itself if the schedule changes while open.
- **Once per confirmation.** The confirmation names the backup when it opens (`<schedule>-<UTC timestamp>`); sending it again after a lost answer returns that backup instead of starting another.
- **Refused when it would fail:** a schedule whose name does not fit a label (409; the button is disabled).
- `POST /api/velero/schedules/{namespace}/{name}/backup`, behind capability `veleroBackupNow`.

## Data

- `GET /api/velero/workspace`: every installation the caller can read. Readings, not objects; every time is absolute, with `observedAt` saying when the readings were computed.
- `GET /api/velero/schedules/{namespace}/{name}/activity`: one schedule with its recent runs, for the drawer. 403 without `list schedules` there, 404 when it does not exist.
- `GET /api/velero/runs/{backups|restores}?namespace=`: every Backup or Restore the caller can read, newest first (at most 5,000, with the total), read through the same per-kind access as the views, so a reader of Velero's kinds alone needs no Pod or Deployment access; `coverage` says what it covered, and the lists and tabs show lower bounds (≥N) over a partial read. Behind capability `veleroRuns`.
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

**A stopped server is named first, once.** When the Velero server Deployment can be read and has no running replicas (scaled to zero, or its pods cannot be created), it leads Needs attention, and its row names the schedules it leaves late ("3 schedules have missed runs: a, b and c"), which get no row of their own; overdue locations are not listed. A schedule wrong for another reason (cannot run, rejected) keeps its own row. An unavailable but running pod does not count: it still processes schedules. A pod still terminating is not counted either; its controllers stop on SIGTERM, so it submits nothing new.

Storage locations need attention when **validation is overdue**: the interval is known and more than three intervals plus 5 minutes have passed since the last validation (Radar's threshold). An Unavailable location is already an Issue (`BackupStorageLocationUnavailable`).

The certain readings are also Issues, raised by the Problems surface from the same rules, so they reach `/api/issues`, MCP and Diagnose: a stopped server, and schedules that are late, cannot run, held or not validated, and overdue validation. One fault is one Issue: see [one fault, one issue](integrations.md#one-fault-one-issue). Estimates ("probably late", "late or held") stay readings here. See the Velero section of [integrations.md](integrations.md). These views keep showing their own readings, which need no Pod or Deployment access, and do not list those Issues twice.

## Issues on these views

The Velero Issues the Problems surface raises, computed by the same detector (`issues.DetectVelero`) over the objects these views read. A row therefore exists only where the caller could read its kind in that namespace, and a Velero-only reader with no Pod or Deployment access still sees them; the Problems surface, which scopes by namespace access, may show such a reader fewer. Nothing else in the cluster is scanned to produce them. At most 50 per installation are returned, with the count before the cap; a row from an unread namespace is never counted or named. An unavailable location's row names the runs Velero rejected because of it, which get no row of their own.

## Schedule activity and run messages

The Schedule drawer's activity section shows the five most recent runs, one line each (outcome and age; the backup's name is in the tooltip and the line opens it), and **All N runs →** opens the Backups list filtered by `velero.io/schedule-name`, N being the runs Velero keeps. The server returns ten.

A Backup or Restore's error and warning text is not on the object: **Show 9 errors and 3 warnings** asks Velero for its results file (at most 500 messages). On a failed or partially failed run whose Issue the drawer shows, the button sits inside that Issue's *What's wrong*; otherwise in Status. The list is grouped by scope (Velero, cluster-scoped, or a namespace) with counts, folds messages that differ only in one word (one error per failed item, so forty PVCs read as one line "× 40" with the names on expanding), and shows the first 10 rows with **Show all N** for the rest.

## Limits

- **Completed is not restorable.** "Completed, not expired" is what Velero reports; it does not say the data is in the bucket or that a restore would succeed.
- **An uncertain zone is not daylight-saving safe.** A weekly schedule at a local hour skipped by a daylight-saving change can exceed the uncertain margin and read "probably late". Readable Deployments avoid it.
- **An installation needs a storage location for its readings to become Issues.** The derived Issues are raised per namespace holding a BackupStorageLocation. Without one, every backup is already rejected and raised as such, but a stopped server there is shown only on these views.
- **Runs are matched by schedule name.** A recreated or edited schedule's older runs still count as its runs.
- **Velero 1.16–1.18.** 1.16 and 1.17 do not count an empty-phase run as holding; Radar does.

## Testing

`make velero-demo` freezes every phase with the controller scaled to 0, so schedules genuinely go late; `30-schedules.yaml` adds a held schedule and one whose name is too long. `./scripts/velero-demo.sh live` lets a running controller submit a schedule's runs and refuse the long-named one. See `scripts/velero-demo/README.md`.
