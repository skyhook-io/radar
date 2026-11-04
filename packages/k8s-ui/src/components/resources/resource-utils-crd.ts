import type { GenericStatus } from './generic-status'

export function getCRDStatus(data: any): GenericStatus {
  const conditions: any[] = data.status?.conditions || []
  const terminating = conditions.find(c => c.type === 'Terminating' && c.status === 'True')
  if (data.metadata?.deletionTimestamp || terminating) return { text: 'Terminating', tone: 'degraded', reason: terminating?.message }
  for (const [type, fallback] of [['NamesAccepted', 'Names not accepted'], ['Established', 'Not established']]) {
    const condition = conditions.find(c => c.type === type && c.status === 'False')
    if (condition) return { text: condition.reason || fallback, tone: 'unhealthy', reason: condition.message }
  }
  return conditions.some(c => c.type === 'Established' && c.status === 'True')
    ? { text: 'Established', tone: 'healthy' }
    : { text: 'Unknown', tone: 'unknown' }
}
