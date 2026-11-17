import type { HealthLevel } from '../resources/resource-utils'
import { cronToHuman, formatAge, formatDuration } from '../resources/resource-utils'
import { formatRelativeAgeTime } from '../../utils/format'
import type {
  VeleroDefaultSource,
  VeleroLocationReading,
  VeleroRun,
  VeleroScheduleReading,
  VeleroScheduleRuns,
  VeleroScheduleState,
  VeleroZone,
  VeleroZoneSource,
} from './types'

// Presentation of the server's Velero readings. Nothing here decides a state:
// the server has, from the controller's own rules. These only word it.

const STATE_LABEL: Record<VeleroScheduleState, string> = {
  paused: 'Paused',
  schedulingDisabled: 'Scheduling disabled',
  rejected: 'Rejected',
  cannotRun: 'Cannot run',
  notValidated: 'Not validated',
  unknown: 'Unknown',
  alwaysDue: 'Always due',
  skipPending: 'Skipping next run',
  held: 'Held',
  late: 'Late',
  probablyLate: 'Probably late',
  lateOrHeld: 'Late or held',
  possiblyLate: 'Possibly late',
  due: 'Due',
  onSchedule: 'On schedule',
}

export function veleroScheduleStateLabel(state: VeleroScheduleState): string {
  return STATE_LABEL[state] ?? state
}

const STATE_TONE: Record<VeleroScheduleState, HealthLevel> = {
  paused: 'neutral',
  schedulingDisabled: 'neutral',
  rejected: 'unhealthy',
  cannotRun: 'unhealthy',
  notValidated: 'degraded',
  unknown: 'unknown',
  alwaysDue: 'unhealthy',
  skipPending: 'neutral',
  held: 'neutral',
  late: 'unhealthy',
  probablyLate: 'alert',
  lateOrHeld: 'alert',
  possiblyLate: 'degraded',
  due: 'neutral',
  onSchedule: 'healthy',
}

/** The tone of a schedule's state. A hold reads calm until the server calls it attention. */
export function veleroScheduleTone(reading: Pick<VeleroScheduleReading, 'state' | 'attention'>): HealthLevel {
  if (reading.state === 'held' && reading.attention) return 'alert'
  return STATE_TONE[reading.state] ?? 'unknown'
}

/**
 * The run phases the Velero views count as failed: failed, rejected, or partly
 * failed, including the partial phases Velero is still finishing. One
 * definition for the run lists, the tabs' marks and the schedule badge.
 */
export const VELERO_FAILED_PHASES: ReadonlySet<string> = new Set([
  'Failed',
  'FailedValidation',
  'PartiallyFailed',
  'FinalizingPartiallyFailed',
  'WaitingForPluginOperationsPartiallyFailed',
])

/** Whether Radar is sure of the zone the schedule's cron is read in. */
export function veleroZoneConfirmed(zone: VeleroZone): boolean {
  return zone.source !== 'inferred' && zone.source !== 'unknown'
}

/**
 * How a schedule's state reads on its badge. Due within its grace, with the
 * server running, is on schedule: Velero submits it on its next pass. A
 * schedule that would read healthy says so only when its newest run did not
 * fail, and only in green when its zone is confirmed.
 */
export function veleroScheduleBadge(
  reading: Pick<VeleroScheduleReading, 'state' | 'attention'> & Partial<Pick<VeleroScheduleReading, 'reason' | 'zone' | 'runs'>>,
): { label: string; tone: HealthLevel; detail?: string } {
  const onSchedule = reading.state === 'onSchedule' || (reading.state === 'due' && reading.reason !== 'serverDown')
  if (!onSchedule) return { label: veleroScheduleStateLabel(reading.state), tone: veleroScheduleTone(reading) }
  const newest = reading.runs?.newest
  if (newest && VELERO_FAILED_PHASES.has(newest.phase ?? '')) {
    return { label: 'Last run failed', tone: 'alert', detail: `Its newest run, ${newest.name}, ${newest.phase === 'FailedValidation' ? 'was rejected' : newest.phase === 'Failed' ? 'failed' : 'partially failed'}.` }
  }
  return { label: STATE_LABEL.onSchedule, tone: reading.zone && !veleroZoneConfirmed(reading.zone) ? 'unknown' : 'healthy' }
}

/** A moment relative to now: "in 3h" for the future, "3h ago" for the past. */
export function veleroMomentText(iso: string | undefined, fallback = '—'): string {
  if (!iso) return fallback
  const at = Date.parse(iso)
  if (!Number.isFinite(at)) return fallback
  const diff = at - Date.now()
  if (diff > 0) return `in ${formatDuration(diff)}`
  return formatRelativeAgeTime(iso, fallback)
}

/** A moment in the viewer's local time, the one format the Velero views use ("Nov 15, 03:00 AM"). */
export function veleroLocalTime(iso: string): string {
  return new Date(iso).toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
}

const SERVER_DOWN = 'The Velero server has no running replicas, so nothing submits backups.'

function plural(n: number, one: string, many: string): string {
  return `${n} ${n === 1 ? one : many}`
}

function missedText(n: number | undefined): string {
  if (!n) return 'its scheduled time passed'
  // The server stops counting at 100.
  if (n >= 100) return 'at least 100 scheduled times passed'
  return `${plural(n, 'scheduled time', 'scheduled times')} passed`
}

/**
 * Why a schedule is in its state, in one or two plain sentences. `created` is
 * the Schedule's creationTimestamp when the caller holds the object (the
 * drawer); the workspace reading does not carry it.
 */
export function veleroScheduleReasonText(reading: VeleroScheduleReading, opts?: { created?: string; serverShown?: boolean }): string {
  const blocker = reading.blocker
  // Where the stopped server is already named above (the Overview), its consequences don't repeat it.
  const serverDown = reading.reason === 'serverDown' ? (opts?.serverShown ? '' : ` ${SERVER_DOWN}`) : null
  switch (reading.state) {
    case 'paused':
      return 'Paused. Velero submits no backups for it until it is resumed.'
    case 'schedulingDisabled':
      return 'This Velero server runs without its schedule controller (--restore-only or --disable-controllers), so no schedule runs.'
    case 'rejected':
      return reading.validationErrors?.length
        ? `Velero rejected this schedule: ${reading.validationErrors.join('; ')}`
        : 'Velero rejected this schedule, so it does not run.'
    case 'cannotRun':
      return 'This schedule’s name is longer than 63 characters, too long for the label Velero puts on its backups, so Velero submits no runs.'
    case 'notValidated': {
      if (!reading.attention) return 'Waiting for Velero to validate this schedule.'
      if (serverDown !== null) return `Velero hasn’t validated this schedule.${serverDown}`
      const created = opts?.created ?? reading.created
      return created
        ? `Velero hasn’t validated this schedule since it was created ${formatAge(created)} ago, so its controller may not be processing schedules.`
        : 'Velero hasn’t validated this schedule. It validates a new schedule as soon as it sees it, so its controller may not be processing schedules.'
    }
    case 'unknown':
      return 'Radar can’t read this cron expression the way Velero does, so it can’t say when the schedule runs next.'
    case 'alwaysDue':
      return 'This cron expression has no future time, so Velero treats the schedule as always due and submits a new backup whenever the previous one has started.'
    case 'skipPending':
      return 'Set to skip its next run (skipImmediately). Velero moves the schedule past it on its next pass.'
    case 'held':
      if (reading.reason === 'unstartedBlocker' && blocker) {
        return `Its backup hasn’t started for ${formatAge(blocker.created)}; Velero submits nothing new until it does.`
      }
      if (blocker && (blocker.phase === '' || blocker.phase === 'New' || !blocker.phase)) {
        return `Its backup was submitted ${formatRelativeAgeTime(blocker.created)} and hasn’t started; Velero submits nothing new until it does.`
      }
      return 'A backup from this schedule is still running; Velero submits the next one after it finishes.'
    case 'late': {
      const since = reading.lastSubmitted
        ? `since ${veleroLocalTime(reading.lastSubmitted)} (${formatRelativeAgeTime(reading.lastSubmitted)})`
        : 'since the schedule was created'
      const cause = serverDown ?? ' Velero does not catch up on missed runs.'
      return `No backup submitted ${since}; ${missedText(reading.missedRuns)}.${cause}`
    }
    case 'probablyLate':
      if (serverDown !== null) return `Past its scheduled time.${serverDown}`
      return 'Looks late, but Radar couldn’t confirm the Velero server’s time zone.'
    case 'lateOrHeld':
      if (serverDown !== null) return `Past its scheduled time.${serverDown}`
      return 'Past its scheduled time. Radar can’t read this namespace’s backups, so a running backup may be holding it.'
    case 'possiblyLate':
      return 'Past its estimated time, but Radar couldn’t confirm the Velero server’s time zone, so it may not be due yet.'
    case 'due':
      if (serverDown !== null) return serverDown ? 'Due now, but the Velero server has no running replicas, so nothing will submit it.' : 'Due now, but nothing submits it until the server starts.'
      return 'Due now. Velero normally submits it within a few minutes.'
    case 'onSchedule':
      return 'On schedule'
  }
  return ''
}

// robfig/cron's descriptors, which Velero accepts beside five-field expressions.
const CRON_DESCRIPTORS: Record<string, string> = {
  '@yearly': 'Yearly on January 1 at midnight',
  '@annually': 'Yearly on January 1 at midnight',
  '@monthly': 'Monthly on the 1st at midnight',
  '@weekly': 'Weekly on Sunday at midnight',
  '@daily': 'Daily at midnight',
  '@midnight': 'Daily at midnight',
  '@hourly': 'Every hour',
}

/**
 * A schedule's cadence in plain words ("Every 2 minutes", "Weekly on Sunday at
 * 2:00"), without its zone, which is said beside it; undefined for a shape
 * Radar does not describe, where the cron itself is the answer.
 */
export function veleroCadenceText(cron: string | undefined): string | undefined {
  const bare = (cron ?? '').trim().replace(/^(CRON_TZ|TZ)=\S+\s+/i, '')
  if (!bare) return undefined
  const descriptor = CRON_DESCRIPTORS[bare.toLowerCase()]
  if (descriptor) return descriptor
  const every = /^@every\s+(\S+)$/i.exec(bare)
  if (every) return `Every ${every[1]}`
  const fields = bare.split(/\s+/).join(' ')
  const human = cronToHuman(fields)
  return human === fields || human === '-' ? undefined : human
}

// Said after the zone's name: "UTC — the Velero server’s time zone".
const ZONE_SOURCE_LABEL: Record<VeleroZoneSource, string> = {
  cron: 'set in the cron expression',
  server: 'the Velero server’s time zone',
  serverDefault: 'the Velero server’s time zone',
  inferred: 'inferred from a backup’s name',
  unknown: 'assumed: the Velero server’s zone is unknown',
}

const ZONE_SOURCE_DETAIL: Record<VeleroZoneSource, string> = {
  cron: 'The cron expression names its own zone (CRON_TZ= or TZ=).',
  server: 'The Velero server Deployment sets TZ, and its rollout is complete.',
  serverDefault: 'The Velero server Deployment sets no TZ, so the server runs in UTC.',
  inferred: 'An offset read from the name of this schedule’s newest backup, which Velero writes in its local time. An offset carries no daylight-saving rules.',
  unknown: 'Radar couldn’t read the Velero server’s zone, so times are estimated in UTC.',
}

export function veleroZoneSourceLabel(source: VeleroZoneSource): string {
  return ZONE_SOURCE_LABEL[source] ?? source
}

export function veleroZoneDetail(zone: VeleroZone): string {
  return ZONE_SOURCE_DETAIL[zone.source] ?? ''
}

/** The next run's relative text, "~" prefixed when the zone it was evaluated in is not certain. */
export function veleroNextRunText(reading: Pick<VeleroScheduleReading, 'nextRun' | 'nextRunCertain'>): string {
  if (!reading.nextRun) return '—'
  // A next run in the past is when it was due; it did not happen.
  const past = Date.parse(reading.nextRun) <= Date.now()
  const text = past ? `was due ${formatRelativeAgeTime(reading.nextRun)}` : veleroMomentText(reading.nextRun)
  return reading.nextRunCertain ? text : `~${text}`
}

export function veleroNextRunTitle(reading: Pick<VeleroScheduleReading, 'nextRun' | 'nextRunCertain' | 'zone'>): string | undefined {
  if (!reading.nextRun) return undefined
  const at = veleroLocalTime(reading.nextRun)
  return reading.nextRunCertain
    ? at
    : `${at} · an estimate: ${veleroZoneDetail(reading.zone)}`
}

export const VELERO_RUNS_UNKNOWN = 'Unknown — no access to backups'
/** No run among the backups Velero still keeps; TTL deletes older ones, so this is not "never". */
export const VELERO_NONE_KEPT = 'None kept: Velero deletes backups once their retention ends'
/** VELERO_NONE_KEPT for a table cell; the full sentence goes in its tooltip. */
export const VELERO_NONE_KEPT_SHORT = 'None kept'

/**
 * Velero never submitted a run of this schedule and keeps none: "Never" is
 * certain there, where "None kept" would suggest runs that were deleted.
 */
export function veleroScheduleNeverRan(reading: Pick<VeleroScheduleReading, 'lastSubmitted' | 'runs'>): boolean {
  return reading.runs.read !== 'unknown' && !reading.lastSubmitted && !reading.runs.newest
}

/** The newest Completed run, or why there is none to show. */
export function veleroNewestSuccessfulText(runs: VeleroScheduleRuns): { text: string; run?: VeleroRun } {
  if (runs.read === 'unknown') return { text: VELERO_RUNS_UNKNOWN }
  if (!runs.newestCompleted) return { text: VELERO_NONE_KEPT }
  return { text: formatRelativeAgeTime(runs.newestCompleted.completed ?? runs.newestCompleted.ranAt), run: runs.newestCompleted }
}

// The units of a Go duration, the form Velero's spec.ttl takes, two-letter ones first.
const GO_DURATION_UNITS = ['ns', 'us', 'µs', 'μs', 'ms', 's', 'm', 'h']

/** Whether s is a Go duration whose every term is zero ("0s", "0h0m0s"). Scanned by hand, in one pass. */
function isZeroGoDuration(s: string): boolean {
  let at = 0
  while (at < s.length) {
    const start = at
    let digits = 0
    while (at < s.length && s[at] >= '0' && s[at] <= '9') {
      if (s[at] !== '0') return false
      at++
      digits++
    }
    if (s[at] === '.') {
      at++
      while (at < s.length && s[at] >= '0' && s[at] <= '9') {
        if (s[at] !== '0') return false
        at++
        digits++
      }
    }
    if (digits === 0) return false
    const unit = GO_DURATION_UNITS.find((u) => s.startsWith(u, at))
    if (!unit || at === start) return false
    at += unit.length
  }
  return at > 0
}

/**
 * A spec.ttl as Velero applies it: undefined when the server's
 * --default-backup-ttl applies, which Velero's backup controller substitutes
 * for an absent or zero duration ("0s" means the default, not no retention).
 * pkg/velero.DeclaredTTL is the same rule for the schedule reading.
 */
export function veleroDeclaredTtl(ttl: unknown): string | undefined {
  if (typeof ttl !== 'string' || ttl === '' || ttl === '0') return undefined
  if (isZeroGoDuration(ttl)) return undefined
  return ttl
}

/** How long a declared retention keeps each run ("30 days"), or undefined when the server's default applies. */
export function veleroDeclaredRetention(ttl: unknown): string | undefined {
  const declared = veleroDeclaredTtl(ttl)
  return declared ? veleroRetentionText(declared) : undefined
}

/** What a schedule or backup that declares no retention keeps its runs for. */
export const VELERO_SERVER_DEFAULT_RETENTION = 'The Velero server’s default (30 days unless it was changed)'

/** A retention as people say it: Velero writes Go durations, so "720h0m0s" is "30 days". Anything else shows as written. */
export function veleroRetentionText(ttl: string): string {
  const m = /^(\d+)h(\d+)m(\d+)s$/.exec(ttl)
  if (!m || m[2] !== '0' || m[3] !== '0') return ttl
  const hours = Number(m[1])
  if (hours > 0 && hours % 24 === 0) return `${hours / 24} ${hours === 24 ? 'day' : 'days'}`
  return `${hours} ${hours === 1 ? 'hour' : 'hours'}`
}

/** A validation frequency in the shared duration tiers ("1m", "12h"). */
export function veleroFrequencyText(seconds: number): string {
  return formatDuration(seconds * 1000)
}

/** Why a storage location needs attention. */
export function veleroLocationReasonText(loc: VeleroLocationReading): string {
  if (loc.neverValidated) {
    const cause = loc.reason === 'serverDown' ? ' The Velero server has no running replicas, so nothing validates it.' : ''
    return `Velero has never validated this location; it validates a new location as soon as it sees it.${cause}`
  }
  if (loc.validationOverdue && loc.lastValidation && loc.frequency) {
    const cause = loc.reason === 'serverDown' ? ' The Velero server has no running replicas, so nothing validates it.' : ''
    return `Velero last validated this location ${formatAge(loc.lastValidation)} ago; it validates every ${veleroFrequencyText(loc.frequency.seconds)}.${cause}`
  }
  return ''
}

/** Why an installation needs attention: its server Deployment has no available pod. */
export function veleroServerDownText(namespace: string, deployment?: string): string {
  return `Deployment ${deployment ?? 'velero'} in ${namespace} has no running replicas, so no new scheduled backups are submitted and no storage location is validated.`
}

const DEFAULT_SOURCE_DETAIL: Record<VeleroDefaultSource, string> = {
  spec: 'A location in this namespace sets spec.default.',
  server: 'No location sets spec.default; the Velero server’s --default-backup-storage-location names it.',
  unknown: 'No location sets spec.default, and Radar couldn’t read the Velero server’s flags.',
  none: 'No location sets spec.default and the Velero server’s --default-backup-storage-location is empty, so a backup that names no location fails.',
}

/** Whether a location is its installation's default, with the source of that answer. */
export function veleroDefaultText(loc: Pick<VeleroLocationReading, 'default' | 'defaultSource'>): { text: string; detail: string } {
  if (loc.default) return { text: 'Yes', detail: DEFAULT_SOURCE_DETAIL[loc.defaultSource] }
  if (loc.defaultSource === 'unknown') return { text: 'Unknown', detail: DEFAULT_SOURCE_DETAIL.unknown }
  return { text: 'No', detail: DEFAULT_SOURCE_DETAIL[loc.defaultSource] }
}

// Velero passes the object store's and the network's errors through verbatim
// (an AWS SDK error inside a gRPC error inside a sentence), so the common ones
// get one plain line and a next step. Credentials first: a wrong key is also a
// 403. Velero's own refusals that name a storage location (v1.18.2 backup,
// restore and deletion controllers) come after the object store's errors,
// which say why a location is unavailable, and are said without the kind's
// name or "BSL".
interface Reading {
  plain: string
  step: string
  /** For a refusal about a storage location: which one, and what was wrong with it. */
  location?: VeleroLocationRefusal
}

/** A Velero refusal that names a storage location, read into its parts. */
export interface VeleroLocationRefusal {
  name: string
  problem: 'missing' | 'unavailable' | 'readOnly'
}
const LOCATION_NAME = '"?([a-z0-9][a-z0-9.-]*)"?'
const LOCATION = `(?:BackupStorageLocation|backup storage location|BSL) ${LOCATION_NAME}`
const REASONS: [RegExp, (m: RegExpExecArray) => Reading][] = [
  [/found an? (backup|restore) with status "?InProgress"? during the server starting/i, (m) => ({
    plain: `Velero restarted while this ${m[1].toLowerCase()} ran, so it was marked Failed`,
    step: `Run the ${m[1].toLowerCase()} again`,
  })],
  [/NoSuchBucket|bucket does not exist/i, () => ({ plain: 'The bucket doesn’t exist', step: 'Create the bucket, or point the storage location at an existing one' })],
  [/InvalidAccessKeyId|SignatureDoesNotMatch/i, () => ({ plain: 'Velero’s credentials are wrong', step: 'Fix the credentials Secret Velero uses for this storage location' })],
  [/AccessDenied|Forbidden|status code:? 403/i, () => ({ plain: 'Velero’s credentials can’t access the bucket', step: 'Fix the credentials Secret Velero uses for this storage location, or grant them access to the bucket' })],
  [/no such host|i\/o timeout|TLS handshake timeout|connection refused|network is unreachable/i, () => ({ plain: 'Velero can’t reach the object store', step: 'Check the storage location’s endpoint, and that the Velero server can reach it' })],
  [new RegExp(`${LOCATION} (?:is in Unavailable status|is currently in Unavailable state)|The BSL ${LOCATION_NAME} is unavailable`, 'i'), (m) => ({
    plain: `Its storage location ${m[1] ?? m[2]} is unavailable`,
    step: `Make storage location ${m[1] ?? m[2]} available, then try again`,
    location: { name: m[1] ?? m[2], problem: 'unavailable' },
  })],
  [new RegExp(`${LOCATION} is currently in read-only mode`, 'i'), (m) => ({
    plain: `Its storage location ${m[1]} is read-only`,
    step: `Set storage location ${m[1]} to read-write, or use another location`,
    location: { name: m[1], problem: 'readOnly' },
  })],
  [new RegExp(`${LOCATION} not found`, 'i'), (m) => ({
    plain: `Its storage location ${m[1]} doesn’t exist`,
    step: `Create storage location ${m[1]}, or use one that exists`,
    location: { name: m[1], problem: 'missing' },
  })],
  [new RegExp(`not specified at backup creation time and the (?:server )?default ${LOCATION_NAME} (?:does not exist|was not found)`, 'i'), (m) => ({
    plain: `It names no storage location, and the default, ${m[1]}, doesn’t exist`,
    step: 'Name an existing storage location, or create the default, then create the backup again',
  })],
  [/^backup is still in progress$/i, () => ({ plain: 'The backup was still running', step: 'Wait for the backup to finish, then delete it again' })],
  [/^backup not found$/i, () => ({ plain: 'Velero couldn’t find the backup', step: 'Check that the backup still exists' })],
]

function readReason(message: string | undefined): Reading | undefined {
  if (!message) return undefined
  const text = message.trim()
  for (const [pattern, read] of REASONS) {
    const m = pattern.exec(text)
    if (m) return read(m)
  }
  return undefined
}

/**
 * One plain line for a raw Velero or object-store message, or undefined when
 * the message is not one Radar recognises. The raw text stays the evidence;
 * this only says what it means.
 */
export function veleroPlainReason(message: string | undefined): string | undefined {
  return readReason(message)?.plain
}

/**
 * The storage location a Velero refusal names and what was wrong with it, for
 * a caller that says it in its own words (a past refusal, with the name shown
 * as a name); undefined for any other message.
 */
export function veleroLocationRefusal(message: string | undefined): VeleroLocationRefusal | undefined {
  return readReason(message)?.location
}

/** What to do about a raw Velero or object-store message Radar recognises, as one plain line. */
export function veleroReasonNextStep(message: string | undefined): string | undefined {
  return readReason(message)?.step
}
