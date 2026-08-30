// NVIDIA NIM Operator CRD utility functions

import type { StatusBadge } from './resource-utils'
import { healthColors } from './resource-utils'

// ============================================================================
// SHARED HELPERS
// ============================================================================

function stateToBadge(state: string | undefined): StatusBadge {
  switch (state) {
    case 'Ready':
      return { text: 'Ready', color: healthColors.healthy, level: 'healthy' }
    case 'Failed':
      return { text: 'Failed', color: healthColors.unhealthy, level: 'unhealthy' }
    case 'NotReady':
    case 'Pending':
    case 'InProgress':
    case 'Started':
    case 'PVC-Created':
      return { text: state, color: healthColors.degraded, level: 'degraded' }
    default:
      return { text: state || 'Unknown', color: healthColors.unknown, level: 'unknown' }
  }
}

// ============================================================================
// NIMSERVICE UTILITIES
// ============================================================================

export function getNIMServiceStatus(resource: any): StatusBadge {
  return stateToBadge(resource.status?.state)
}

export function getNIMServiceImage(resource: any): string {
  const image = resource.spec?.image
  if (!image?.repository) return '-'
  return image.tag ? `${image.repository}:${image.tag}` : image.repository
}

export function getNIMServiceModel(resource: any): string {
  const modelName = resource.status?.model?.name
  if (modelName) return modelName
  return getNIMServiceImage(resource)
}

export function getNIMServiceReplicas(resource: any): string {
  const available = resource.status?.availableReplicas ?? 0
  const scale = resource.spec?.scale
  if (scale?.enabled) {
    const min = scale.hpa?.minReplicas
    const max = scale.hpa?.maxReplicas
    if (min !== undefined && max !== undefined) return `${available} (${min}-${max})`
    return `${available} (hpa)`
  }
  const desired = resource.spec?.replicas
  if (desired !== undefined && desired !== null) return `${available}/${desired}`
  return `${available}`
}

// ============================================================================
// NIMCACHE UTILITIES
// ============================================================================

export function getNIMCacheStatus(resource: any): StatusBadge {
  return stateToBadge(resource.status?.state)
}

export function getNIMCacheSourceType(resource: any): string {
  const source = resource.spec?.source
  if (!source) return '-'
  if (source.ngc) return 'NGC'
  if (source.dataStore) return 'DataStore'
  if (source.hf) return 'HF'
  return '-'
}

export function getNIMCacheModelSource(resource: any): string {
  const source = resource.spec?.source
  if (!source) return '-'
  if (source.ngc) return source.ngc.modelPuller || source.ngc.modelEndpoint || '-'
  const dshf = source.dataStore || source.hf
  if (dshf) return dshf.modelName || dshf.datasetName || '-'
  return '-'
}

export function getNIMCacheStorageSize(resource: any): string {
  return resource.spec?.storage?.pvc?.size || '-'
}

export function getNIMCachePVCName(resource: any): string {
  return resource.status?.pvc || resource.spec?.storage?.pvc?.name || '-'
}

// ============================================================================
// NIMPIPELINE UTILITIES
// ============================================================================

export function getNIMPipelineStatus(resource: any): StatusBadge {
  return stateToBadge(resource.status?.state)
}

export function getNIMPipelineServiceCount(resource: any): string {
  const services = resource.spec?.services || []
  if (services.length === 0) return '0'
  const enabled = services.filter((s: any) => s?.enabled !== false).length
  if (enabled < services.length) return `${enabled}/${services.length} enabled`
  return `${services.length}`
}
