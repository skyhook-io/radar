import type { HealthLevel } from '../components/resources/resource-utils'

const REMOVAL_ACTORS: Record<string, string> = {
  ToBeDeletedByClusterAutoscaler: 'cluster autoscaler',
  'karpenter.sh/disrupted': 'Karpenter',
}

const NETWORK_UNAVAILABLE_GRACE_MS = 2 * 60_000

export const NODE_REMOVAL_WARNING_MS = 10 * 60_000
export const NODE_REMOVAL_CRITICAL_MS = 30 * 60_000

export interface NodeLifecycleState {
  label: string
  level: HealthLevel
  removing: boolean
  actor?: string
  startedAt?: number
  readinessFailed: boolean
  delayed: boolean
  problems: string[]
}

export function getNodeLifecycle(node: any, now = Date.now()): NodeLifecycleState {
  const ready = node?.status?.conditions?.find((condition: any) => condition.type === 'Ready')
  const createdAt = Date.parse(node?.metadata?.creationTimestamp)
  const validTime = (timestamp: number) => Number.isFinite(timestamp) && timestamp > 0 && timestamp <= now && (!Number.isFinite(createdAt) || timestamp >= createdAt) ? timestamp : undefined
  let startedAt = validTime(Date.parse(node?.metadata?.deletionTimestamp))
  let removing = Boolean(node?.metadata?.deletionTimestamp)
  let actor: string | undefined
  let candidate = false
  for (const taint of node?.spec?.taints ?? []) {
    if (taint.key === 'DeletionCandidateOfClusterAutoscaler' && taint.effect === 'PreferNoSchedule') candidate = true
    if (taint.effect !== 'NoSchedule') continue
    const source = REMOVAL_ACTORS[taint.key]
    if (!source) continue
    removing = true
    actor = source
    if (taint.key === 'ToBeDeletedByClusterAutoscaler' && /^\d+$/.test(taint.value ?? '')) {
      const markerTime = validTime(Number(taint.value) * 1000)
      if (markerTime !== undefined && (startedAt === undefined || markerTime < startedAt)) startedAt = markerTime
    }
  }
  const readyTime = Date.parse(ready?.lastTransitionTime)
  const readinessFailed = Boolean(ready && ready.status !== 'True' && !(removing && startedAt !== undefined && Number.isFinite(readyTime) && readyTime >= startedAt && readyTime <= now))
  const delayed = removing && startedAt !== undefined && now - startedAt >= NODE_REMOVAL_WARNING_MS
  let label = 'Unknown'
  let level: HealthLevel = 'unknown'
  if (removing) {
    label = actor ? `Removing (${actor})` : 'Removing'
    level = readinessFailed || (delayed && now - startedAt! >= NODE_REMOVAL_CRITICAL_MS) ? 'unhealthy' : delayed ? 'degraded' : 'neutral'
  } else if (readinessFailed) {
    label = 'NotReady'
    level = 'unhealthy'
  } else if (ready?.status === 'True') {
    label = node?.spec?.unschedulable ? 'Cordoned' : candidate ? 'Scale-down candidate' : 'Ready'
    level = node?.spec?.unschedulable ? 'degraded' : candidate ? 'neutral' : 'healthy'
  }
  const problems: string[] = []
  const problemLabels: Record<string, string> = { MemoryPressure: 'Memory pressure', DiskPressure: 'Disk pressure', PIDPressure: 'PID pressure', NetworkUnavailable: 'Network unavailable' }
  for (const condition of node?.status?.conditions ?? []) {
    if (condition.status !== 'True' || !problemLabels[condition.type]) continue
    const conditionTime = Date.parse(condition.lastTransitionTime)
    if (condition.type === 'NetworkUnavailable' && Number.isFinite(conditionTime) && now - conditionTime < NETWORK_UNAVAILABLE_GRACE_MS) continue
    problems.push(condition.type)
    label += ` · ${problemLabels[condition.type]}`
    if (condition.type !== 'NetworkUnavailable') level = 'unhealthy'
    else if (level !== 'unhealthy') level = 'degraded'
  }
  return { label, level, removing, actor, startedAt, readinessFailed, delayed, problems }
}
