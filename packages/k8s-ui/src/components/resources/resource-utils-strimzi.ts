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

/**
 * Reconciliation status shared by Kafka and KafkaNodePool. Strimzi reports
 * failures as `NotReady=True` rather than `Ready=False`, so the generic
 * condition walk cannot be used. Matching generations only establish that the
 * operator processed the current spec; they say nothing about whether clients
 * can reach the brokers.
 */
export function getStrimziClusterStatus(data: any): StatusBadge & { snapshotCurrent: boolean } {
  const badge = (text: string, level: StatusBadge['level'], snapshotCurrent = false) => ({ text, level, color: healthColors[level], snapshotCurrent })
  if (data.metadata?.deletionTimestamp) return badge('Terminating', 'neutral')
  if (data.metadata?.annotations?.['strimzi.io/pause-reconciliation']?.toLowerCase() === 'true') return badge('Reconciliation paused', 'neutral')
  const observed = data.status?.observedGeneration
  if (!Number.isInteger(observed) || observed <= 0) return badge('Status unknown', 'unknown')
  if (observed !== data.metadata?.generation) return badge('Status stale', 'unknown')
  const conditions = Array.isArray(data.status?.conditions) ? data.status.conditions : []
  if (conditions.some((c: any) => c?.type === 'ReconciliationPaused' && c.status === 'True')) return badge('Reconciliation paused', 'neutral')
  if (conditions.some((c: any) => c?.type === 'NotReady' && c.status === 'True')) return badge('Not ready', 'degraded', true)
  if (conditions.every((c: any) => c && typeof c === 'object' && !Array.isArray(c)) && conditions.some((c: any) => c.type === 'Ready' && c.status === 'True')) return badge('Ready', 'healthy', true)
  return badge('Status unknown', 'unknown', true)
}

type StrimziListener = { name: string; port?: number; type?: string; tls?: boolean; authentication?: string; bootstrapServers?: string; addresses: string[] }

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
    byName.set(name, { name, port: Number.isInteger(l.port) ? l.port : undefined, type: str(l.type), tls: typeof l.tls === 'boolean' ? l.tls : undefined, authentication: str(l.authentication?.type), addresses: [] })
  }
  for (const l of reported) {
    // Older releases reported the listener name under `type`.
    const name = str(l?.name) ?? str(l?.type)
    if (!name) continue
    const entry = byName.get(name) ?? { name, addresses: [] }
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
  if (storage.deleteClaim === true) parts.push('claim deleted with cluster')
  const size = typeof storage.size === 'string' && storage.size ? ` ${storage.size}` : ''
  return [`${type}${size}${parts.length ? ` (${parts.join(', ')})` : ''}`]
}

export function getStrimziClusterColumnValue(data: any, column: string): string | undefined {
  if (isStrimziKafka(data)) {
    switch (column) {
      case 'status': return getStrimziClusterStatus(data).text
      case 'kafkaVersion': return data.status?.kafkaVersion ?? data.spec?.kafka?.version ?? ''
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
