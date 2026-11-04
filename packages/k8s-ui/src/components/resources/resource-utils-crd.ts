import type { GenericStatus } from './generic-status'
import { isApiGroup } from './resource-utils-cnpg'

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

export function getCRDColumnValue(data: any, column: string): string | undefined {
  if (data?.kind !== 'CustomResourceDefinition' || !isApiGroup(data.apiVersion, 'apiextensions.k8s.io')) return undefined
  switch (column) {
    case 'kind': return data.spec?.names?.kind ?? ''
    case 'scope': return data.spec?.scope ?? ''
    case 'versions': return (data.spec?.versions ?? []).filter((v: any) => v?.served).map((v: any) => v.name).join(', ')
    case 'status': return getCRDStatus(data).text
    default: return undefined
  }
}
