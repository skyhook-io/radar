// KNative CRD utility functions

import type { StatusBadge } from './resource-utils'
import { healthColors } from './resource-utils'

// ============================================================================
// SHARED HELPERS
// ============================================================================

export function getKnativeConditionStatus(resource: any): StatusBadge {
  const conditions = resource?.status?.conditions || []
  const ready = conditions.find((c: any) => c.type === 'Ready')
  if (!ready) return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
  if (ready.status === 'True') return { text: 'Ready', color: healthColors.healthy, level: 'healthy' }
  if (ready.status === 'False') return { text: ready.reason || 'Not Ready', color: healthColors.unhealthy, level: 'unhealthy' }
  if (ready.status === 'Unknown') return { text: ready.reason || 'Reconciling', color: healthColors.degraded, level: 'degraded' }
  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

function formatTraffic(traffic: any[]): string {
  if (!traffic || traffic.length === 0) return '-'
  return traffic
    .map((t: any) => {
      const percent = t.percent != null ? `${t.percent}%` : ''
      const target = t.revisionName || (t.latestRevision ? '(latest)' : '')
      return [percent, target].filter(Boolean).join(' \u2192 ')
    })
    .join(', ')
}

function formatRef(ref: any): string {
  if (!ref) return '-'
  return `${ref.kind || ''}/${ref.name || ''}`
}

// ============================================================================
// KNATIVE SERVICE (serving.knative.dev/v1)
// ============================================================================

export function getKnativeServiceUrl(resource: any): string {
  return resource?.status?.url || '-'
}

export function getKnativeServiceLatestRevision(resource: any): string {
  return resource?.status?.latestReadyRevisionName || '-'
}

export function getKnativeServiceTraffic(resource: any): string {
  return formatTraffic(resource?.status?.traffic)
}

// ============================================================================
// REVISION (serving.knative.dev/v1)
// ============================================================================

export function getRevisionStatus(resource: any): StatusBadge {
  const conditions = resource?.status?.conditions || []
  const ready = conditions.find((c: any) => c.type === 'Ready')
  const active = conditions.find((c: any) => c.type === 'Active')

  // Check for scaled-to-zero: Ready=True but Active=False with reason NoTraffic
  if (ready?.status === 'True' && active?.status === 'False') {
    const reason = active.reason || ''
    if (reason === 'NoTraffic') {
      return { text: 'Scaled to Zero', color: 'status-blue', level: 'healthy' }
    }
    return { text: reason || 'Inactive', color: 'status-amber', level: 'degraded' }
  }

  // Check for activating: Active condition is Unknown (scaling up)
  if (ready?.status === 'True' && active?.status === 'Unknown') {
    return { text: 'Activating', color: 'status-blue', level: 'healthy' }
  }

  return getKnativeConditionStatus(resource)
}

export function getRevisionImage(resource: any): string {
  const containers = resource?.spec?.containers || resource?.spec?.template?.spec?.containers || []
  return containers[0]?.image || '-'
}

export function getRevisionConcurrency(resource: any): string {
  const cc = resource?.spec?.containerConcurrency
  if (cc == null) return '-'
  return cc === 0 ? 'Unlimited' : String(cc)
}

// ============================================================================
// ROUTE (serving.knative.dev/v1)
// ============================================================================

export function getRouteUrl(resource: any): string {
  return resource?.status?.url || '-'
}

export function getRouteTraffic(resource: any): string {
  return formatTraffic(resource?.spec?.traffic)
}

// ============================================================================
// CONFIGURATION (serving.knative.dev/v1)
// ============================================================================

export function getConfigurationLatestCreated(resource: any): string {
  return resource?.status?.latestCreatedRevisionName || '-'
}

export function getConfigurationLatestReady(resource: any): string {
  return resource?.status?.latestReadyRevisionName || '-'
}

// ============================================================================
// BROKER (eventing.knative.dev/v1)
// ============================================================================

export function getBrokerAddress(resource: any): string {
  return resource?.status?.address?.url || '-'
}

// ============================================================================
// TRIGGER (eventing.knative.dev/v1)
// ============================================================================

export function getTriggerBroker(resource: any): string {
  return resource?.spec?.broker || '-'
}

export function getTriggerSubscriber(resource: any): string {
  const ref = resource?.spec?.subscriber?.ref
  if (ref) return formatRef(ref)
  return resource?.spec?.subscriber?.uri || '-'
}

export function getTriggerFilter(resource: any): string {
  const attributes = resource?.spec?.filter?.attributes
  if (!attributes || Object.keys(attributes).length === 0) return '-'
  return Object.entries(attributes)
    .map(([k, v]) => `${k}=${v}`)
    .join(', ')
}

// ============================================================================
// SOURCES (sources.knative.dev/v1)
// ============================================================================

export function getSourceSink(resource: any): string {
  const ref = resource?.spec?.sink?.ref
  if (ref) return formatRef(ref)
  return resource?.spec?.sink?.uri || '-'
}

export function getPingSourceSchedule(resource: any): string {
  return resource?.spec?.schedule || '-'
}

export function getPingSourceData(resource: any): string {
  const data = resource?.spec?.data
  if (!data) return '-'
  if (data.length > 50) return data.substring(0, 50) + '...'
  return data
}

// ============================================================================
// CHANNEL (messaging.knative.dev/v1)
// ============================================================================

export function getChannelAddress(resource: any): string {
  return resource?.status?.address?.url || '-'
}

// ============================================================================
// SUBSCRIPTION (messaging.knative.dev/v1)
// ============================================================================

export function getSubscriptionChannel(resource: any): string {
  const channel = resource?.spec?.channel
  if (!channel) return '-'
  return formatRef(channel)
}

export function getSubscriptionSubscriber(resource: any): string {
  const ref = resource?.spec?.subscriber?.ref
  if (ref) return formatRef(ref)
  return resource?.spec?.subscriber?.uri || '-'
}

// ============================================================================
// SEQUENCE (flows.knative.dev/v1)
// ============================================================================

export function getSequenceStepCount(resource: any): number {
  return (resource?.spec?.steps || []).length
}

// ============================================================================
// PARALLEL (flows.knative.dev/v1)
// ============================================================================

export function getParallelBranchCount(resource: any): number {
  return (resource?.spec?.branches || []).length
}

// ============================================================================
// DOMAINMAPPING (serving.knative.dev/v1beta1)
// ============================================================================

export function getDomainMappingUrl(resource: any): string {
  return resource?.status?.url || '-'
}

// ============================================================================
// SERVERLESSSERVICE (networking.internal.knative.dev/v1alpha1)
// ============================================================================

export function getServerlessServiceMode(resource: any): string {
  return resource?.spec?.mode || '-'
}
