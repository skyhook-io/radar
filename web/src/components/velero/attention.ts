import {
  veleroLocationReasonText,
  veleroScheduleReasonText,
  veleroScheduleStateLabel,
  veleroScheduleTone,
  type VeleroKindCoverage,
  type VeleroLocationReading,
  type VeleroScheduleReading,
  type VeleroWorkspaceKey,
  type VeleroWorkspaceResponse,
  type WorkspaceProblem,
} from '@skyhook-io/k8s-ui'
import { INCOMPLETE_COVERAGE_STATES } from '../workspace/coverage'
import { VELERO_GROUP } from './routes'
import { VELERO_CHECK_SERVER, VELERO_SHORTEN_NAME, VELERO_START_SERVER, VELERO_WRITABLE_LOCATION, inInstallation, namesAndMore, veleroIssueProblem } from './shared'

export function readInPart(cov: VeleroKindCoverage | undefined): boolean {
  return !cov || INCOMPLETE_COVERAGE_STATES.has(cov.state)
}

/** What to do about a schedule that needs attention, in one plain line. */
export function veleroScheduleNextStep(s: Pick<VeleroScheduleReading, 'state' | 'reason'>): string | undefined {
  if (s.reason === 'serverDown') return VELERO_START_SERVER
  switch (s.state) {
    case 'cannotRun':
      return s.reason === 'locationReadOnly' ? VELERO_WRITABLE_LOCATION : VELERO_SHORTEN_NAME
    case 'alwaysDue':
      return 'Change the cron expression to one with a future time'
    case 'rejected':
      return 'Fix what Velero rejected in the schedule; Velero validates it again when it changes'
    case 'held':
      return 'Find out why its backup hasn’t started, or delete that backup'
    case 'late':
    case 'probablyLate':
    case 'lateOrHeld':
      return 'Check that the Velero server is running with its schedule controller, and read its logs'
    case 'notValidated':
      return VELERO_CHECK_SERVER
  }
  return undefined
}

function scheduleProblem(s: VeleroScheduleReading, serverShown: boolean): WorkspaceProblem {
  const tone = veleroScheduleTone(s)
  return {
    id: `schedule:${s.namespace}/${s.name}`,
    category: 'schedule',
    title: veleroScheduleStateLabel(s.state),
    detail: veleroScheduleReasonText(s, { serverShown }),
    nextStep: veleroScheduleNextStep(s),
    subject: { kind: 'Schedule', group: VELERO_GROUP, namespace: s.namespace, name: s.name },
    source: 'reading',
    tone: tone === 'healthy' ? 'neutral' : tone,
  }
}

function serverProblem(namespace: string, deployment: string | undefined, late: VeleroScheduleReading[]): WorkspaceProblem {
  const missed =
    late.length === 0
      ? ''
      : ` ${late.length === 1 ? '1 schedule has' : `${late.length} schedules have`} missed runs: ${namesAndMore(late.map((s) => s.name))}.`
  return {
    id: `server:${namespace}`,
    category: 'server',
    title: 'Velero server not running',
    // The subject link below names the Deployment.
    detail: `No pods, so no new scheduled backups are submitted and no storage location is validated.${missed}`,
    nextStep: VELERO_START_SERVER,
    subject: { kind: 'Deployment', group: 'apps', namespace, name: deployment ?? 'velero' },
    source: 'reading',
    tone: 'unhealthy',
  }
}

function backupsDisabledProblem(namespace: string, deployment: string | undefined, schedules: VeleroScheduleReading[]): WorkspaceProblem {
  return {
    id: `backups-disabled:${namespace}`,
    category: 'server',
    title: 'Velero backups disabled',
    detail: `The server runs without its backup controller (--restore-only, or --disable-controllers naming backup or backup-queue), so no backup runs: ${
      schedules.length === 1 ? 'schedule' : `${schedules.length} schedules,`
    } ${namesAndMore(schedules.map((s) => s.name))} ${schedules.length === 1 ? 'backs' : 'back'} up nothing.`,
    nextStep: 'Enable the backup controller on the Velero server, or pause the schedules',
    subject: { kind: 'Deployment', group: 'apps', namespace, name: deployment ?? 'velero' },
    source: 'reading',
    tone: 'unhealthy',
  }
}

function locationProblem(l: VeleroLocationReading): WorkspaceProblem {
  return {
    id: `location:${l.namespace}/${l.name}`,
    category: 'location',
    title: l.neverValidated ? 'Never validated' : 'Validation overdue',
    detail: veleroLocationReasonText(l) || undefined,
    nextStep: l.reason === 'serverDown' ? VELERO_START_SERVER : VELERO_CHECK_SERVER,
    subject: { kind: 'BackupStorageLocation', group: VELERO_GROUP, namespace: l.namespace, name: l.name },
    source: 'reading',
    tone: 'degraded',
  }
}

/**
 * The Overview's Needs attention list: a stopped server, a server with backups
 * disabled while schedules are not paused, schedule and location readings,
 * then Velero's Issues. One fault is one row, as on the Issues page: a stopped
 * server's row names the schedules it leaves late, and a backups-disabled
 * row the schedules it leaves holding unstarted runs, which get no row of
 * their own; storage a stopped server leaves unchecked is not listed. An
 * unavailable location's issue names the runs Velero rejected because of it.
 */
export function veleroAttention(data: VeleroWorkspaceResponse, installation: string | null): WorkspaceProblem[] {
  const issues = installation === null ? data.issues : data.issues.filter((i) => (i.namespace ?? '') === installation)
  const down = data.installations.filter((i) => (installation === null || i.namespace === installation) && i.server.replicas === 0)
  const downIn = new Set(down.map((i) => i.namespace))
  const lateWhileDown = (s: VeleroScheduleReading) => downIn.has(s.namespace) && s.state === 'late'
  const schedules = inInstallation(data.schedules, installation).filter((s) => s.attention)
  // A stopped server is the one fault there; backups disabled is said once it runs.
  const disabled = data.installations
    .filter((i) => (installation === null || i.namespace === installation) && !downIn.has(i.namespace) && i.server.backupsDisabled === true)
    .map((i) => ({ installation: i, schedules: data.schedules.filter((s) => s.namespace === i.namespace && s.state !== 'paused') }))
    .filter((d) => d.schedules.length > 0)
  const disabledIn = new Set(disabled.map((d) => d.installation.namespace))
  const heldWhileDisabled = (s: VeleroScheduleReading) => disabledIn.has(s.namespace) && s.state === 'held'
  return [
    ...down.map((i) => serverProblem(i.namespace, i.server.deployment, schedules.filter((s) => s.namespace === i.namespace && lateWhileDown(s)))),
    ...disabled.map((d) => backupsDisabledProblem(d.installation.namespace, d.installation.server.deployment, d.schedules)),
    ...schedules.filter((s) => !lateWhileDown(s) && !heldWhileDisabled(s)).map((s) => scheduleProblem(s, downIn.has(s.namespace))),
    ...inInstallation(data.locations, installation)
      .filter((l) => l.attention && !(l.reason === 'serverDown' && downIn.has(l.namespace)))
      .map(locationProblem),
    ...issues.map((i) => veleroIssueProblem(i)),
  ]
}

/**
 * What Radar has for one Velero object, as banners for its page: its schedule
 * or location reading when that needs attention (with the stopped server named
 * in it), then Velero's Issues about it or naming it among the runs a fault
 * rejected. Each says what to do next.
 */
export function veleroObjectProblems(data: VeleroWorkspaceResponse, kind: string, namespace: string, name: string): WorkspaceProblem[] {
  const out: WorkspaceProblem[] = []
  if (kind === 'Schedule') {
    const s = data.schedules.find((r) => r.namespace === namespace && r.name === name)
    if (s?.attention) out.push({ ...scheduleProblem(s, false), title: `Schedule ${veleroScheduleStateLabel(s.state).toLowerCase()}` })
  }
  if (kind === 'BackupStorageLocation') {
    const l = data.locations.find((r) => r.namespace === namespace && r.name === name)
    if (l?.attention) {
      const p = locationProblem(l)
      out.push({ ...p, title: `Storage location ${p.title[0].toLowerCase()}${p.title.slice(1)}` })
    }
  }
  const issues = data.issues.filter((i) => {
    const ns = i.namespace ?? ''
    if (i.kind === kind && ns === namespace && i.name === name) return true
    return !!i.affected?.some((a) => a.kind === kind && (a.namespace ?? ns) === namespace && a.name === name)
  })
  return [...out, ...issues.map((i) => veleroIssueProblem(i, { withNextStep: true }))]
}

export interface VeleroAttentionCount {
  /** `null` when nothing was found but the read was partial, so zero would claim too much. */
  count: number | null
  lowerBound: boolean
  title: string
}

/** The Issues the server counted but did not send (it sends the newest 50 per installation). */
function cappedIssues(data: VeleroWorkspaceResponse): number {
  return Object.entries(data.issueTotals ?? {}).reduce(
    (n, [ns, total]) => n + Math.max(0, total - data.issues.filter((i) => (i.namespace ?? '') === ns).length),
    0,
  )
}

const OVERVIEW_KINDS: readonly VeleroWorkspaceKey[] = ['schedules', 'backups', 'restores', 'backupStorageLocations', 'backupRepositories']

/**
 * The Overview's sidebar badge: how many rows Needs attention lists. Issues
 * the server capped still count: it sends their total. A count over data read
 * only in part is a lower bound, and its zero is unknown.
 */
/**
 * Whether Needs attention may be missing rows: a kind read in part, or a
 * server Deployment Radar could not read, which hides a stopped server and
 * disabled backups. `installation` narrows it as veleroAttention does.
 */
export function veleroAttentionPartial(data: VeleroWorkspaceResponse, installation: string | null): boolean {
  return OVERVIEW_KINDS.some((k) => readInPart(data.coverage[k])) || inInstallation(data.installations, installation).some((i) => i.server.source === 'unread')
}

/**
 * Rows that may fold into one where a server Deployment is unread: a stopped
 * server takes the late schedules into its row, disabled backups the held
 * ones. Subtracted, the count stays a lower bound whichever is true.
 */
function foldableWhileServerUnread(data: VeleroWorkspaceResponse): number {
  return data.installations
    .filter((i) => i.server.source === 'unread')
    .reduce((n, i) => {
      const rows = data.schedules.filter((s) => s.namespace === i.namespace && s.attention)
      const late = rows.filter((s) => s.state === 'late').length
      const held = rows.filter((s) => s.state === 'held').length
      return n + Math.max(late - 1, held - 1, 0)
    }, 0)
}

export function veleroAttentionCount(data: VeleroWorkspaceResponse): VeleroAttentionCount {
  const count = veleroAttention(data, null).length + cappedIssues(data) - foldableWhileServerUnread(data)
  const partial = veleroAttentionPartial(data, null)
  if (partial && count === 0) {
    return { count: null, lowerBound: true, title: 'Unknown: some Velero data could not be read, so nothing found does not mean nothing needs attention.' }
  }
  const note = partial ? ' Some Velero data could not be read, so there may be more.' : ''
  return { count, lowerBound: partial, title: `${partial ? 'At least ' : ''}${count} in Needs attention.${note}` }
}

const SCREEN_KINDS = {
  backups: { kind: 'Backup', coverage: 'backups', noun: ['backup', 'backups'] },
  schedules: { kind: 'Schedule', coverage: 'schedules', noun: ['schedule', 'schedules'] },
  restores: { kind: 'Restore', coverage: 'restores', noun: ['restore', 'restores'] },
  backupstoragelocations: { kind: 'BackupStorageLocation', coverage: 'backupStorageLocations', noun: ['storage location', 'storage locations'] },
} as const satisfies Record<string, { kind: string; coverage: VeleroWorkspaceKey; noun: readonly [string, string] }>

export type VeleroScreen = keyof typeof SCREEN_KINDS

/**
 * Each list screen's sidebar badge: the rows Needs attention lists about that
 * screen's kind, so the screens' badges add up to the Overview's total less
 * what belongs to no screen (a stopped server, disabled backups, Issues the
 * server capped).
 * Issues the server capped may be about any kind, so while there are some
 * every screen's count is a lower bound.
 */
export function veleroScreenCounts(data: VeleroWorkspaceResponse): Record<VeleroScreen, VeleroAttentionCount> {
  const problems = veleroAttention(data, null)
  const capped = cappedIssues(data) > 0
  const out = {} as Record<VeleroScreen, VeleroAttentionCount>
  for (const [screen, { kind, coverage, noun }] of Object.entries(SCREEN_KINDS) as [VeleroScreen, (typeof SCREEN_KINDS)[VeleroScreen]][]) {
    const count = problems.filter((p) => p.subject?.kind === kind).length
    const unread = readInPart(data.coverage[coverage])
    const partial = unread || capped
    const why = unread ? `Radar couldn’t read every ${noun[0]}` : 'Radar lists only the newest issues'
    if (partial && count === 0) {
      out[screen] = { count: null, lowerBound: true, title: `Unknown: ${why}.` }
      continue
    }
    out[screen] = {
      count,
      lowerBound: partial,
      title: `${partial ? 'At least ' : ''}${count} ${count === 1 ? `${noun[0]} needs` : `${noun[1]} need`} attention.${unread ? ' Some could not be read.' : capped ? ' Radar lists only the newest issues, so there may be more.' : ''}`,
    }
  }
  return out
}
