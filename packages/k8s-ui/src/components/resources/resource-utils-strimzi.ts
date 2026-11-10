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
