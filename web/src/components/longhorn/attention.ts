import { formatAge, type LonghornVolumeReading, type LonghornWorkspaceResponse, type WorkspaceProblem } from '@skyhook-io/k8s-ui'
import { INCOMPLETE_COVERAGE_STATES } from '../workspace/coverage'
import { LONGHORN_GROUP } from './routes'

/** Whether the Volumes read may not be all of them, so an empty list proves nothing. */
export function longhornVolumesPartial(data: LonghornWorkspaceResponse): boolean {
  const cov = data.coverage.volumes
  return !data.counts || !data.counts.complete || !cov || INCOMPLETE_COVERAGE_STATES.has(cov.state)
}

/** What the Volume serves, in one sentence: its claim, or why Radar can't name it. */
export function longhornUseText(v: LonghornVolumeReading): string | undefined {
  const { use } = v
  if (use.state === 'unknown') return 'Radar can’t tell which claim uses it.'
  if (use.state === 'none') return 'No PersistentVolume uses it.'
  if (use.claimWithheld) return 'Its PersistentVolume is bound to a claim you can’t read.'
  if (!use.claim) return `It backs PersistentVolume ${use.persistentVolume ?? ''}, which is not bound to a claim.`
  return undefined
}

function replicasText(v: LonghornVolumeReading): string {
  if (v.healthyReplicas === null || v.numberOfReplicas === undefined) return ''
  return `${v.healthyReplicas} of ${v.numberOfReplicas} replicas healthy. `
}

function volumeProblem(v: LonghornVolumeReading): WorkspaceProblem {
  const faulted = v.health === 'faulted'
  const detail = faulted
    ? 'No replica is usable, so Longhorn can’t serve this volume’s data.'
    : `${replicasText(v)}Longhorn rebuilds replicas when it can place one, which is routine after a node drain or restart.`
  return {
    id: `volume:${v.namespace}/${v.name}`,
    category: 'volume',
    title: faulted ? 'Faulted' : v.lastDegradedAt ? `Degraded for ${formatAge(v.lastDegradedAt)}` : 'Degraded; Longhorn didn’t record since when',
    detail: [detail, longhornUseText(v)].filter(Boolean).join(' '),
    nextStep: faulted
      ? 'Salvage a replica in Longhorn, or restore the volume from a backup'
      : 'Usually none: let the rebuild finish. If it lasts, check that a replica can be scheduled (node disks, scheduling settings)',
    subject: { kind: 'Volume', group: LONGHORN_GROUP, namespace: v.namespace, name: v.name },
    ...(v.use.claim
      ? { related: { lead: 'Used by claim', refs: [{ kind: 'PersistentVolumeClaim', group: '', namespace: v.use.claim.namespace, name: v.use.claim.name }] } }
      : {}),
    source: 'reading',
    tone: faulted ? 'unhealthy' : 'degraded',
  }
}

const degradedSince = (v: LonghornVolumeReading) => (v.lastDegradedAt ? Date.parse(v.lastDegradedAt) : Infinity)

/** Needs attention: faulted Volumes, then degraded ones, the longest degraded first. Detached is not a problem. */
export function longhornAttention(data: LonghornWorkspaceResponse): WorkspaceProblem[] {
  const faulted = data.volumes.filter((v) => v.health === 'faulted')
  const degraded = data.volumes.filter((v) => v.health === 'degraded').sort((a, b) => degradedSince(a) - degradedSince(b))
  return [...faulted, ...degraded].map(volumeProblem)
}

export interface LonghornAttentionCount {
  /** null when nothing was found but the read was partial, so zero would claim too much. */
  count: number | null
  lowerBound: boolean
  title: string
}

/** The sidebar badge: faulted plus degraded Volumes, each counted once. */
export function longhornAttentionCount(data: LonghornWorkspaceResponse): LonghornAttentionCount {
  const partial = longhornVolumesPartial(data)
  const count = data.counts ? data.counts.faulted + data.counts.degraded : 0
  if (partial && count === 0) {
    return { count: null, lowerBound: true, title: 'Unknown: Radar couldn’t read every Longhorn volume, so none found doesn’t mean none needs attention.' }
  }
  return {
    count,
    lowerBound: partial,
    title: `${partial ? 'At least ' : ''}${count} faulted or degraded ${count === 1 ? 'volume' : 'volumes'}.${partial ? ' Some volumes could not be read.' : ''}`,
  }
}
