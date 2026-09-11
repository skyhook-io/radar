// KServe CRD utility functions

import type { StatusBadge } from './resource-utils'
import { healthColors } from './resource-utils'

// ============================================================================
// SHARED HELPERS
// ============================================================================

function findCondition(resource: any, type: string): any {
  const conditions = resource.status?.conditions
  if (!Array.isArray(conditions)) return undefined
  return conditions.find((c: any) => c?.type === type)
}

function truncateMiddle(value: string, max = 44): string {
  if (value.length <= max) return value
  const keep = Math.floor((max - 1) / 2)
  return `${value.slice(0, keep)}…${value.slice(-keep)}`
}

// ============================================================================
// KSERVE INFERENCESERVICE UTILITIES
// ============================================================================

export function getInferenceServiceStatus(resource: any): StatusBadge {
  const transition = resource.status?.modelStatus?.transitionStatus
  if (transition === 'BlockedByFailedLoad') {
    return { text: 'Load failed', color: healthColors.unhealthy, level: 'unhealthy' }
  }
  if (transition === 'InvalidSpec') {
    return { text: 'Invalid spec', color: healthColors.unhealthy, level: 'unhealthy' }
  }

  const readyCond = findCondition(resource, 'Ready')
  if (readyCond?.status === 'True') {
    return { text: 'Ready', color: healthColors.healthy, level: 'healthy' }
  }
  if (readyCond) {
    return { text: readyCond.reason || 'NotReady', color: healthColors.degraded, level: 'degraded' }
  }

  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

export function getInferenceServiceUrl(resource: any): string {
  const url = resource.status?.url
  return typeof url === 'string' && url ? url : '-'
}

export function getInferenceServiceModelFormat(resource: any): string {
  return resource.spec?.predictor?.model?.modelFormat?.name || '-'
}

export function getInferenceServiceRuntime(resource: any): string {
  return resource.spec?.predictor?.model?.runtime || '-'
}

export function getInferenceServiceDeploymentMode(resource: any): string {
  const annotations = resource.metadata?.annotations || {}
  return annotations['serving.kserve.io/deploymentMode'] || resource.status?.deploymentMode || '-'
}

// ============================================================================
// KSERVE SERVINGRUNTIME / CLUSTERSERVINGRUNTIME UTILITIES
// ============================================================================

export function getServingRuntimeStatus(resource: any): StatusBadge {
  if (resource.spec?.disabled === true) {
    return { text: 'Disabled', color: healthColors.neutral, level: 'neutral' }
  }
  return { text: 'Enabled', color: healthColors.neutral, level: 'neutral' }
}

export function getServingRuntimeModelFormats(resource: any): string {
  const formats = resource.spec?.supportedModelFormats
  if (!Array.isArray(formats) || formats.length === 0) return '-'
  const names = [...new Set(formats.map((f: any) => f?.name).filter(Boolean))] as string[]
  if (names.length === 0) return '-'
  if (names.length > 3) return `${names.slice(0, 3).join(', ')} +${names.length - 3}`
  return names.join(', ')
}

export function getServingRuntimeImage(resource: any): string {
  const containers = resource.spec?.containers
  if (!Array.isArray(containers) || containers.length === 0) return '-'
  return containers[0]?.image || '-'
}

// ============================================================================
// KSERVE INFERENCEGRAPH UTILITIES
// ============================================================================

export function getInferenceGraphStatus(resource: any): StatusBadge {
  const readyCond = findCondition(resource, 'Ready')
  if (readyCond?.status === 'True') {
    return { text: 'Ready', color: healthColors.healthy, level: 'healthy' }
  }
  if (readyCond?.status === 'False') {
    return { text: readyCond.reason || 'NotReady', color: healthColors.unhealthy, level: 'unhealthy' }
  }
  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

export function getInferenceGraphNodeCount(resource: any): number {
  const nodes = resource.spec?.nodes
  if (!nodes || typeof nodes !== 'object' || Array.isArray(nodes)) return 0
  return Object.keys(nodes).length
}

// ============================================================================
// KSERVE TRAINEDMODEL UTILITIES
// ============================================================================

export function getTrainedModelStatus(resource: any): StatusBadge {
  const readyCond = findCondition(resource, 'Ready')
  if (readyCond?.status === 'True') {
    return { text: 'Ready', color: healthColors.healthy, level: 'healthy' }
  }
  if (readyCond?.status === 'False') {
    return { text: readyCond.reason || 'NotReady', color: healthColors.unhealthy, level: 'unhealthy' }
  }
  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

export function getTrainedModelFramework(resource: any): string {
  return resource.spec?.model?.framework || '-'
}

export function getTrainedModelStorageUri(resource: any): string {
  const uri = resource.spec?.model?.storageUri
  if (typeof uri !== 'string' || !uri) return '-'
  return truncateMiddle(uri)
}

export function getTrainedModelInferenceService(resource: any): string {
  return resource.spec?.inferenceService || '-'
}

// ============================================================================
// KSERVE LLMINFERENCESERVICE UTILITIES
// ============================================================================

export function getLLMInferenceServiceStatus(resource: any): StatusBadge {
  const conditions = Array.isArray(resource.status?.conditions) ? resource.status.conditions : []

  const readyCond = conditions.find((c: any) => c?.type === 'Ready')
  if (readyCond?.status === 'True') {
    return { text: 'Ready', color: healthColors.healthy, level: 'healthy' }
  }
  if (readyCond?.status === 'False') {
    return { text: readyCond.reason || 'NotReady', color: healthColors.unhealthy, level: 'unhealthy' }
  }

  if (conditions.length > 0) {
    const failing = conditions.find((c: any) => c?.status === 'False')
    if (failing) {
      return { text: failing.reason || failing.type || 'NotReady', color: healthColors.degraded, level: 'degraded' }
    }
    if (conditions.every((c: any) => c?.status === 'True')) {
      return { text: 'Ready', color: healthColors.healthy, level: 'healthy' }
    }
  }

  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

export function getLLMInferenceServiceModel(resource: any): string {
  const model = resource.spec?.model
  if (!model || typeof model !== 'object') return '-'
  if (model.name) return model.name
  if (typeof model.uri === 'string' && model.uri) return truncateMiddle(model.uri)
  return '-'
}

export function getLLMInferenceServiceModelUri(resource: any): string {
  const uri = resource.spec?.model?.uri
  if (typeof uri !== 'string' || !uri) return '-'
  return truncateMiddle(uri)
}

export function getLLMInferenceServiceReplicas(resource: any): string {
  const replicas = resource.spec?.replicas
  if (replicas === undefined || replicas === null) return '-'
  return String(replicas)
}
