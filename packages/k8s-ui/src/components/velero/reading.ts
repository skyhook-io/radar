import type { HealthLevel } from '../resources/resource-utils'
import { formatAge, formatDuration } from '../resources/resource-utils'
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
      return 'This schedule’s name is longer than 63 characters, so Velero cannot label its backups and every run fails.'
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
      return 'Due now. Velero normally submits it within a few minutes.'
    case 'onSchedule':
      return 'On schedule.'
  }
  return ''
}

const ZONE_SOURCE_LABEL: Record<VeleroZoneSource, string> = {
  cron: 'from cron',
  server: 'server TZ',
  serverDefault: 'server default',
  inferred: 'inferred',
  unknown: 'zone unknown',
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

/** The newest Completed run, or why there is none to show. */
export function veleroNewestSuccessfulText(runs: VeleroScheduleRuns): { text: string; run?: VeleroRun } {
  if (runs.read === 'unknown') return { text: VELERO_RUNS_UNKNOWN }
  if (!runs.newestCompleted) return { text: VELERO_NONE_KEPT }
  return { text: formatRelativeAgeTime(runs.newestCompleted.completed ?? runs.newestCompleted.ranAt), run: runs.newestCompleted }
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
