import {
  veleroLocationReasonText,
  veleroScheduleReasonText,
  veleroScheduleStateLabel,
  veleroScheduleTone,
  veleroServerDownText,
  type VeleroKindCoverage,
  type VeleroLocationReading,
  type VeleroScheduleReading,
  type VeleroWorkspaceKey,
  type VeleroWorkspaceResponse,
  type WorkspaceProblem,
} from '@skyhook-io/k8s-ui'
import { INCOMPLETE_COVERAGE_STATES } from '../workspace/coverage'
import { VELERO_GROUP } from './routes'
import { inInstallation, veleroIssueProblem } from './shared'

export function readInPart(cov: VeleroKindCoverage | undefined): boolean {
  return !cov || INCOMPLETE_COVERAGE_STATES.has(cov.state)
}

function scheduleProblem(s: VeleroScheduleReading): WorkspaceProblem {
  const tone = veleroScheduleTone(s)
  return {
    id: `schedule:${s.namespace}/${s.name}`,
    category: 'schedule',
    title: veleroScheduleStateLabel(s.state),
    detail: veleroScheduleReasonText(s),
    subject: { kind: 'Schedule', group: VELERO_GROUP, namespace: s.namespace, name: s.name },
    source: 'reading',
    origin: { label: 'Radar’s reading of the schedule', detail: 'The Schedule and its Backups, read against the Velero controller’s own rules.' },
    tone: tone === 'healthy' ? 'neutral' : tone,
  }
}

function serverProblem(namespace: string, deployment?: string): WorkspaceProblem {
  return {
    id: `server:${namespace}`,
    category: 'server',
    title: 'Velero server not running',
    detail: veleroServerDownText(namespace, deployment),
    subject: { kind: 'Deployment', group: 'apps', namespace, name: deployment ?? 'velero' },
    source: 'reading',
    origin: { label: 'Radar’s reading of the Velero server', detail: 'The server Deployment’s running replicas; a pod still terminating is not counted.' },
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
    origin: { label: 'Radar’s reading of the location', detail: 'The location’s last validation time against its validation frequency.' },
    tone: 'degraded',
  }
}

/** The Overview's Needs attention list: a stopped server, schedule and location readings, then Velero's Issues. */
export function veleroAttention(data: VeleroWorkspaceResponse, installation: string | null): WorkspaceProblem[] {
  const issues = installation === null ? data.issues : data.issues.filter((i) => (i.namespace ?? '') === installation)
  return [
    ...data.installations
      .filter((i) => (installation === null || i.namespace === installation) && i.server.replicas === 0)
      .map((i) => serverProblem(i.namespace, i.server.deployment)),
    ...inInstallation(data.schedules, installation).filter((s) => s.attention).map(scheduleProblem),
    ...inInstallation(data.locations, installation).filter((l) => l.attention).map(locationProblem),
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
