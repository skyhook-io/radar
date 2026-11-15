import { isApiGroup } from './resource-utils-cnpg'
import { healthColors, type StatusBadge } from './resource-utils'

// Generation agreement permits assessment; it does not establish live runtime freshness.
export function getStrimziConnectorStatus(data: any): StatusBadge & { snapshotCurrent: boolean } {
  const badge = (text: string, level: StatusBadge['level'], snapshotCurrent = false) => ({ text, level, color: healthColors[level], snapshotCurrent })
  if (data.metadata?.deletionTimestamp) return badge('Terminating', 'neutral')
  if (data.metadata?.annotations?.['strimzi.io/pause-reconciliation']?.toLowerCase() === 'true') return badge('Reconciliation paused', 'neutral')
  const observed = data.status?.observedGeneration
  if (!Number.isInteger(observed) || observed <= 0) return badge('Status unknown', 'unknown')
  if (observed !== data.metadata?.generation) return badge('Status stale', 'unknown')
  const conditions = Array.isArray(data.status?.conditions) ? data.status.conditions : []
  if (conditions.some((c: any) => c?.type === 'ReconciliationPaused' && c.status === 'True')) return badge('Reconciliation paused', 'neutral')
  const state = data.status?.connectorStatus?.connector?.state
  const tasks = Array.isArray(data.status?.connectorStatus?.tasks) ? data.status.connectorStatus.tasks : []
  if (state === 'FAILED') return badge('Connector failed', 'degraded', true)
  if (tasks.some((t: any) => t?.state === 'FAILED')) return badge('Tasks failed', 'degraded', true)
  if (conditions.some((c: any) => c?.type === 'NotReady' && c.status === 'True')) return badge('Not ready', 'degraded', true)
  if (conditions.every((c: any) => c && typeof c === 'object' && !Array.isArray(c)) && conditions.some((c: any) => c.type === 'Ready' && c.status === 'True') && state === 'RUNNING' && tasks.length > 0 && tasks.every((t: any) => t?.state === 'RUNNING')) return badge('Running', 'healthy', true)
  if (state === 'PAUSED' || state === 'STOPPED') return badge(state === 'PAUSED' ? 'Paused' : 'Stopped', 'neutral', true)
  if (state === 'RESTARTING' || state === 'UNASSIGNED') return badge(state === 'RESTARTING' ? 'Restarting' : 'Unassigned', 'unknown', true)
  if (tasks.some((t: any) => t?.state === 'RESTARTING')) return badge('Tasks restarting', 'unknown', true)
  return badge('Runtime unknown', 'unknown', true)
}

export function getStrimziConnectorColumnValue(data: any, column: string): string | undefined {
  if (data.kind !== 'KafkaConnector' || !isApiGroup(data.apiVersion, 'kafka.strimzi.io')) return undefined
  switch (column) {
    case 'status': return getStrimziConnectorStatus(data).text
    case 'connectCluster': return data.metadata?.labels?.['strimzi.io/cluster'] ?? ''
    case 'connectorClass': return data.spec?.class ?? ''
    case 'connectorTasks': return `${Array.isArray(data.status?.connectorStatus?.tasks) ? data.status.connectorStatus.tasks.length : 'Unknown'} / ${data.spec?.tasksMax ?? 'Not specified'}`
    default: return undefined
  }
}

const STRIMZI_GROUP = 'kafka.strimzi.io'

export function isStrimziKafka(data: any): boolean {
  return data?.kind === 'Kafka' && isApiGroup(data.apiVersion, STRIMZI_GROUP)
}

export function isStrimziKafkaNodePool(data: any): boolean {
  return data?.kind === 'KafkaNodePool' && isApiGroup(data.apiVersion, STRIMZI_GROUP)
}

type StrimziStatus = StatusBadge & { snapshotCurrent: boolean }

const strimziBadge = (text: string, level: StatusBadge['level'], snapshotCurrent = false): StrimziStatus => ({ text, level, color: healthColors[level], snapshotCurrent })

const conditionList = (data: any): any[] => Array.isArray(data.status?.conditions) ? data.status.conditions : []

export function getStrimziClusterStatus(data: any): StrimziStatus {
  return isStrimziKafkaNodePool(data) ? getStrimziKafkaNodePoolStatus(data) : getStrimziKafkaStatus(data)
}

/**
 * Strimzi reports a failed Kafka reconciliation as `NotReady=True` rather than
 * `Ready=False`, so the generic condition walk cannot be used. Matching
 * generations only establish that the operator processed the current spec;
 * they say nothing about whether clients can reach the brokers.
 */
function getStrimziKafkaStatus(data: any): StrimziStatus {
  if (data.metadata?.deletionTimestamp) return strimziBadge('Terminating', 'neutral')
  if (data.metadata?.annotations?.['strimzi.io/pause-reconciliation']?.toLowerCase() === 'true') return strimziBadge('Reconciliation paused', 'neutral')
  const conditions = conditionList(data)
  const observed = data.status?.observedGeneration
  if (!Number.isInteger(observed) || observed <= 0) {
    // The operator writes this one condition, with no observed generation,
    // before the first reconciliation of a new Kafka finishes.
    if (conditions.some(c => c?.type === 'NotReady' && c.status === 'True' && c.reason === 'Creating')) return strimziBadge('Creating', 'unknown')
    return strimziBadge('Status unknown', 'unknown')
  }
  if (observed !== data.metadata?.generation) return strimziBadge('Status stale', 'unknown')
  if (conditions.some(c => c?.type === 'ReconciliationPaused' && c.status === 'True')) return strimziBadge('Reconciliation paused', 'neutral')
  if (conditions.some(c => c?.type === 'NotReady' && c.status === 'True')) return strimziBadge('Not ready', 'degraded', true)
  if (conditions.every(c => c && typeof c === 'object' && !Array.isArray(c)) && conditions.some(c => c.type === 'Ready' && c.status === 'True')) return strimziBadge('Ready', 'healthy', true)
  return strimziBadge('Status unknown', 'unknown', true)
}

/**
 * Strimzi writes a node pool's status partway through reconciling its Kafka,
 * before brokers roll, and never sets Ready or NotReady on it: only Warning
 * conditions, node IDs, roles and replicas. A current pool status therefore
 * means the operator accepted the pool's spec, not that its brokers are
 * healthy; that is the owning Kafka's status. Pause is honoured only on the
 * Kafka, so a pause annotation on the pool itself changes nothing.
 *
 * When brokers being removed still hold partitions, Strimzi keeps the previous
 * replicas or roles, warns only on the Kafka, and still stamps the pool with
 * its current generation; a current status that disagrees with the spec is
 * that refusal.
 */
function getStrimziKafkaNodePoolStatus(data: any): StrimziStatus {
  if (data.metadata?.deletionTimestamp) return strimziBadge('Terminating', 'neutral')
  const observed = data.status?.observedGeneration
  if (!Number.isInteger(observed) || observed <= 0) return strimziBadge('Not reconciled', 'unknown')
  if (observed !== data.metadata?.generation) return strimziBadge('Status stale', 'unknown')
  const drift = getStrimziPoolDrift(data)
  if (drift.replicas || drift.roles) return strimziBadge('Not applied', 'degraded', true)
  if (conditionList(data).some(c => c?.type === 'Warning' && c.status === 'True')) return strimziBadge('Warnings', 'degraded', true)
  return strimziBadge('Accepted', 'neutral', true)
}

type StrimziListener = { name: string; declared: boolean; port?: number; type?: string; tls?: boolean; authentication?: string; bootstrapServers?: string; addresses: string[] }

/**
 * Joins the declared listeners (spec.kafka.listeners) with what Strimzi
 * reports (status.listeners) by name. Certificates are never read.
 */
export function getStrimziKafkaListeners(data: any): StrimziListener[] {
  const declared: any[] = Array.isArray(data.spec?.kafka?.listeners) ? data.spec.kafka.listeners : []
  const reported: any[] = Array.isArray(data.status?.listeners) ? data.status.listeners : []
  const str = (v: unknown) => typeof v === 'string' && v ? v : undefined
  const byName = new Map<string, StrimziListener>()
  for (const l of declared) {
    const name = str(l?.name)
    if (!name) continue
    byName.set(name, { name, declared: true, port: Number.isInteger(l.port) ? l.port : undefined, type: str(l.type), tls: typeof l.tls === 'boolean' ? l.tls : undefined, authentication: str(l.authentication?.type), addresses: [] })
  }
  for (const l of reported) {
    const name = str(l?.name)
    if (!name) continue
    const entry = byName.get(name) ?? { name, declared: false, addresses: [] }
    entry.bootstrapServers = str(l.bootstrapServers)
    entry.addresses = (Array.isArray(l.addresses) ? l.addresses : [])
      .filter((a: any) => typeof a?.host === 'string' && a.host)
      .map((a: any) => Number.isInteger(a.port) ? `${a.host}:${a.port}` : a.host)
    byName.set(name, entry)
  }
  return [...byName.values()]
}

export function getStrimziKafkaPoolNames(data: any): string[] {
  const pools: any[] = Array.isArray(data.status?.kafkaNodePools) ? data.status.kafkaNodePools : []
  return pools.map(p => p?.name).filter((n): n is string => typeof n === 'string' && n.length > 0)
}

const stringList = (v: unknown): string[] | undefined => Array.isArray(v) ? v.filter((r): r is string => typeof r === 'string') : undefined

/** Where a pool's reported allocation differs from its spec. */
export function getStrimziPoolDrift(data: any): { replicas: boolean; roles: boolean; declaredRoles?: string[] } {
  const spec = data.spec ?? {}
  const status = data.status ?? {}
  const replicas = Number.isInteger(spec.replicas) && Number.isInteger(status.replicas) && spec.replicas !== status.replicas
  const declaredRoles = stringList(spec.roles)
  const reportedRoles = stringList(status.roles)
  const roles = !!declaredRoles && !!reportedRoles && (declaredRoles.length !== reportedRoles.length || declaredRoles.some(r => !reportedRoles.includes(r)))
  return { replicas, roles, declaredRoles }
}

export function getStrimziPoolRoles(data: any): string[] {
  const roles = Array.isArray(data.status?.roles) ? data.status.roles : Array.isArray(data.spec?.roles) ? data.spec.roles : []
  return roles.filter((r: unknown): r is string => typeof r === 'string')
}

/** One line per volume, e.g. "persistent-claim 100Gi (id 0, class fast)". */
export function describeStrimziStorage(storage: any): string[] {
  if (!storage || typeof storage !== 'object') return []
  if (storage.type === 'jbod') return (Array.isArray(storage.volumes) ? storage.volumes : []).flatMap(describeStrimziStorage)
  const type = typeof storage.type === 'string' ? storage.type : 'unknown'
  const parts: string[] = []
  if (Number.isInteger(storage.id)) parts.push(`id ${storage.id}`)
  if (typeof storage.class === 'string' && storage.class) parts.push(`class ${storage.class}`)
  if (storage.deleteClaim === true) parts.push('PVC deleted once unused, including after scale-down')
  const size = typeof storage.size === 'string' && storage.size ? ` ${storage.size}` : ''
  return [`${type}${size}${parts.length ? ` (${parts.join(', ')})` : ''}`]
}

export function getStrimziWarningReasons(data: any): string[] {
  return [...new Set(conditionList(data).filter(c => c?.type === 'Warning' && c.status === 'True').map(c => typeof c.reason === 'string' && c.reason ? c.reason : 'Warning'))]
}

/**
 * Strimzi keeps the previously reported version when a reconciliation fails,
 * so during an upgrade the reported and declared versions legitimately differ.
 */
export function describeStrimziVersion(reported: unknown, declared: unknown, missing = 'Not reported'): string {
  const r = typeof reported === 'string' && reported ? reported : undefined
  const d = typeof declared === 'string' && declared ? declared : undefined
  if (r && d && r !== d) return `${r} (declared ${d})`
  if (r) return r
  return d ? `${missing ? `${missing}; ` : ''}declared ${d}` : missing
}

export function getStrimziClusterColumnValue(data: any, column: string): string | undefined {
  if (isStrimziKafka(data)) {
    switch (column) {
      case 'status': return getStrimziClusterStatus(data).text
      case 'kafkaWarnings': return getStrimziWarningReasons(data).join(', ')
      case 'kafkaVersion': return describeStrimziVersion(data.status?.kafkaVersion, data.spec?.kafka?.version, '')
      case 'kafkaPools': {
        const pools = getStrimziKafkaPoolNames(data)
        return pools.length ? pools.join(', ') : ''
      }
      case 'kafkaListeners': return getStrimziKafkaListeners(data).map(l => l.name).join(', ')
      default: return undefined
    }
  }
  if (isStrimziKafkaNodePool(data)) {
    switch (column) {
      case 'status': return getStrimziClusterStatus(data).text
      case 'poolKafka': return data.metadata?.labels?.['strimzi.io/cluster'] ?? ''
      case 'poolRoles': return getStrimziPoolRoles(data).join(', ')
      case 'poolReplicas': return `${data.status?.replicas ?? 'Unknown'} / ${data.spec?.replicas ?? 'Unknown'}`
      default: return undefined
    }
  }
  return undefined
}
