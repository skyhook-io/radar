// Prometheus Operator CRD utility functions

import type { StatusBadge } from './resource-utils'
import { healthColors } from './resource-utils'

// ============================================================================
// SHARED HELPERS
// ============================================================================

function getConditionStatus(resource: any): StatusBadge {
  const conditions = resource.status?.conditions || []

  const reconciledCond = conditions.find((c: any) => c.type === 'Reconciled')
  if (reconciledCond?.status === 'True') {
    return { text: 'Reconciled', color: healthColors.healthy, level: 'healthy' }
  }
  if (reconciledCond?.status === 'False') {
    return { text: reconciledCond.reason || 'Not Reconciled', color: healthColors.unhealthy, level: 'unhealthy' }
  }

  const availableCond = conditions.find((c: any) => c.type === 'Available')
  if (availableCond?.status === 'True') {
    return { text: 'Available', color: healthColors.healthy, level: 'healthy' }
  }
  if (availableCond?.status === 'False') {
    return { text: availableCond.reason || 'Unavailable', color: healthColors.unhealthy, level: 'unhealthy' }
  }

  // Fallback: if resource exists but has no conditions, it's likely active
  if (resource.spec) {
    return { text: 'Active', color: healthColors.healthy, level: 'healthy' }
  }

  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

function formatMatchLabels(selector: any): string {
  const matchLabels = selector?.matchLabels
  if (!matchLabels || Object.keys(matchLabels).length === 0) return '-'
  return Object.entries(matchLabels).map(([k, v]) => `${k}=${v}`).join(', ')
}

// ============================================================================
// SERVICEMONITOR UTILITIES
// ============================================================================

export function getServiceMonitorStatus(resource: any): StatusBadge {
  return getConditionStatus(resource)
}

export function getServiceMonitorEndpointCount(resource: any): number {
  return (resource.spec?.endpoints || []).length
}

export function getServiceMonitorJobLabel(resource: any): string {
  return resource.spec?.jobLabel || '-'
}

export function getServiceMonitorSelector(resource: any): string {
  return formatMatchLabels(resource.spec?.selector)
}

export function getServiceMonitorEndpoints(resource: any): Array<{
  port?: string
  path?: string
  interval?: string
  scheme?: string
}> {
  return (resource.spec?.endpoints || []).map((ep: any) => ({
    port: ep.port || ep.targetPort,
    path: ep.path || '/metrics',
    interval: ep.interval,
    scheme: ep.scheme,
  }))
}

export function getServiceMonitorNamespaceSelector(resource: any): string {
  const ns = resource.spec?.namespaceSelector
  if (!ns) return 'Same namespace'
  if (ns.any) return 'All namespaces'
  if (ns.matchNames?.length) return ns.matchNames.join(', ')
  return 'Same namespace'
}

// ============================================================================
// PROMETHEUSRULE UTILITIES
// ============================================================================

export function getPrometheusRuleStatus(resource: any): StatusBadge {
  return getConditionStatus(resource)
}

export function getPrometheusRuleGroupCount(resource: any): number {
  return (resource.spec?.groups || []).length
}

export function getPrometheusRuleTotalRules(resource: any): number {
  const groups = resource.spec?.groups || []
  return groups.reduce((sum: number, g: any) => sum + (g.rules?.length || 0), 0)
}

export function getPrometheusRuleGroups(resource: any): Array<{
  name: string
  interval?: string
  ruleCount: number
  alertCount: number
  recordCount: number
}> {
  return (resource.spec?.groups || []).map((g: any) => {
    const rules = g.rules || []
    const alertCount = rules.filter((r: any) => r.alert).length
    const recordCount = rules.filter((r: any) => r.record).length
    return {
      name: g.name,
      interval: g.interval,
      ruleCount: rules.length,
      alertCount,
      recordCount,
    }
  })
}

// ============================================================================
// PODMONITOR UTILITIES
// ============================================================================

export function getPodMonitorStatus(resource: any): StatusBadge {
  return getConditionStatus(resource)
}

export function getPodMonitorEndpointCount(resource: any): number {
  return (resource.spec?.podMetricsEndpoints || []).length
}

export function getPodMonitorSelector(resource: any): string {
  return formatMatchLabels(resource.spec?.selector)
}

export function getPodMonitorEndpoints(resource: any): Array<{
  port?: string
  path?: string
  interval?: string
  scheme?: string
}> {
  return (resource.spec?.podMetricsEndpoints || []).map((ep: any) => ({
    port: ep.port || ep.targetPort,
    path: ep.path || '/metrics',
    interval: ep.interval,
    scheme: ep.scheme,
  }))
}

export function getPodMonitorNamespaceSelector(resource: any): string {
  const ns = resource.spec?.namespaceSelector
  if (!ns) return 'Same namespace'
  if (ns.any) return 'All namespaces'
  if (ns.matchNames?.length) return ns.matchNames.join(', ')
  return 'Same namespace'
}
