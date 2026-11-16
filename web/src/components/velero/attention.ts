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
import { inInstallation, namesAndMore, veleroIssueProblem } from './shared'

export function readInPart(cov: VeleroKindCoverage | undefined): boolean {
  return !cov || INCOMPLETE_COVERAGE_STATES.has(cov.state)
}

function scheduleProblem(s: VeleroScheduleReading, serverShown: boolean): WorkspaceProblem {
  const tone = veleroScheduleTone(s)
  return {
    id: `schedule:${s.namespace}/${s.name}`,
    category: 'schedule',
    title: veleroScheduleStateLabel(s.state),
    detail: veleroScheduleReasonText(s, { serverShown }),
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
    detail: `No running replicas, so no new scheduled backups are submitted and no storage location is validated.${missed}`,
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
    subject: { kind: 'BackupStorageLocation', group: VELERO_GROUP, namespace: l.namespace, name: l.name },
    source: 'reading',
    tone: 'degraded',
  }
}

/**
 * The Overview's Needs attention list: a stopped server, schedule and location
 * readings, then Velero's Issues. One fault is one row, as on the Issues page:
 * a stopped server's row names the schedules it leaves late, which get no row
 * of their own, and storage it leaves unchecked is not listed. An unavailable
 * location's issue names the runs Velero rejected because of it.
 */
export function veleroAttention(data: VeleroWorkspaceResponse, installation: string | null): WorkspaceProblem[] {
  const issues = installation === null ? data.issues : data.issues.filter((i) => (i.namespace ?? '') === installation)
  const down = data.installations.filter((i) => (installation === null || i.namespace === installation) && i.server.replicas === 0)
  const downIn = new Set(down.map((i) => i.namespace))
  const lateWhileDown = (s: VeleroScheduleReading) => downIn.has(s.namespace) && s.state === 'late'
  const schedules = inInstallation(data.schedules, installation).filter((s) => s.attention)
  return [
    ...down.map((i) => serverProblem(i.namespace, i.server.deployment, schedules.filter((s) => s.namespace === i.namespace && lateWhileDown(s)))),
    ...schedules.filter((s) => !lateWhileDown(s)).map((s) => scheduleProblem(s, downIn.has(s.namespace))),
    ...inInstallation(data.locations, installation)
      .filter((l) => l.attention && !(l.reason === 'serverDown' && downIn.has(l.namespace)))
      .map(locationProblem),
    ...issues.map(veleroIssueProblem),
  ]
}

export interface VeleroAttentionCount {
  /** `null` when nothing was found but the read was partial, so zero would claim too much. */
  count: number | null
  lowerBound: boolean
  title: string
}

const OVERVIEW_KINDS: readonly VeleroWorkspaceKey[] = ['schedules', 'backups', 'restores', 'backupStorageLocations', 'backupRepositories']

/**
 * The Overview's sidebar badge: how many rows Needs attention lists. Issues
 * the server capped still count: it sends their total. A count over data read
 * only in part is a lower bound, and its zero is unknown.
 */
export function veleroAttentionCount(data: VeleroWorkspaceResponse): VeleroAttentionCount {
  const capped = Object.entries(data.issueTotals ?? {}).reduce(
    (n, [ns, total]) => n + Math.max(0, total - data.issues.filter((i) => (i.namespace ?? '') === ns).length),
    0,
  )
  const count = veleroAttention(data, null).length + capped
  const partial = OVERVIEW_KINDS.some((k) => readInPart(data.coverage[k]))
  if (partial && count === 0) {
    return { count: null, lowerBound: true, title: 'Unknown: some Velero data could not be read, so nothing found does not mean nothing needs attention.' }
  }
  const note = partial ? ' Some Velero data could not be read, so there may be more.' : ''
  return { count, lowerBound: partial, title: `${partial ? 'At least ' : ''}${count} in Needs attention.${note}` }
}
