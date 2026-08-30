// KAITO CRD utility functions

import type { StatusBadge } from './resource-utils'
import { healthColors } from './resource-utils'

// ============================================================================
// KAITO WORKSPACE UTILITIES
// ============================================================================

// Workspace has top-level resource/inference/tuning fields, not under spec
export function getKaitoWorkspaceStatus(resource: any): StatusBadge {
  const state = resource.status?.state
  if (state === 'Failed') {
    return { text: 'Failed', color: healthColors.unhealthy, level: 'unhealthy' }
  }
  if (state === 'NotReady') {
    return { text: 'NotReady', color: healthColors.degraded, level: 'degraded' }
  }
  if (state === 'Ready' || state === 'Running') {
    return { text: state, color: healthColors.healthy, level: 'healthy' }
  }
  if (state === 'Succeeded') {
    return { text: 'Succeeded', color: healthColors.neutral, level: 'neutral' }
  }
  if (state === 'Pending') {
    return { text: 'Pending', color: healthColors.neutral, level: 'neutral' }
  }

  const conditions = resource.status?.conditions || []
  if (conditions.length === 0) {
    return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
  }

  const find = (type: string) => conditions.find((c: any) => c.type === type)
  const succeeded = find('WorkspaceSucceeded')
  const resourceReady = find('ResourceReady')
  const inferenceReady = find('InferenceReady')
  const jobStarted = find('JobStarted')

  if (succeeded?.status === 'True') {
    return { text: 'Succeeded', color: healthColors.neutral, level: 'neutral' }
  }
  if (resourceReady?.status === 'False') {
    return { text: resourceReady.reason || 'ResourcesNotReady', color: healthColors.alert, level: 'alert' }
  }
  if (inferenceReady?.status === 'False') {
    return { text: inferenceReady.reason || 'InferenceNotReady', color: healthColors.degraded, level: 'degraded' }
  }
  if (succeeded?.status === 'False') {
    return { text: succeeded.reason || 'NotReady', color: healthColors.degraded, level: 'degraded' }
  }
  if (resourceReady?.status === 'True' || inferenceReady?.status === 'True' || jobStarted?.status === 'True') {
    return { text: 'Progressing', color: healthColors.degraded, level: 'degraded' }
  }

  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

export function getKaitoWorkspaceInstanceType(resource: any): string {
  return resource.resource?.instanceType || '-'
}

export function getKaitoWorkspacePreset(resource: any): string {
  return resource.inference?.preset?.name || resource.tuning?.preset?.name || '-'
}

export function getKaitoWorkspaceNodeCount(resource: any): string {
  const actual = (resource.status?.workerNodes || []).length
  const desired = resource.status?.targetNodeCount ?? resource.resource?.count
  if (desired !== undefined && desired !== null) return `${actual}/${desired}`
  return `${actual}`
}

// ============================================================================
// KAITO RAGENGINE UTILITIES
// ============================================================================

export function getRAGEngineStatus(resource: any): StatusBadge {
  const conditions = resource.status?.conditions || []
  if (conditions.length === 0) {
    return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
  }

  const find = (type: string) => conditions.find((c: any) => c.type === type)
  const resourceReady = find('ResourceReady')
  const serviceReady = find('ServiceReady')

  if (resourceReady?.status === 'False') {
    return { text: resourceReady.reason || 'ResourcesNotReady', color: healthColors.alert, level: 'alert' }
  }
  if (serviceReady?.status === 'False') {
    return { text: serviceReady.reason || 'ServiceNotReady', color: healthColors.degraded, level: 'degraded' }
  }
  if (resourceReady?.status === 'True' && serviceReady?.status === 'True') {
    return { text: 'Ready', color: healthColors.healthy, level: 'healthy' }
  }
  if (resourceReady?.status === 'True' || serviceReady?.status === 'True') {
    return { text: 'Progressing', color: healthColors.degraded, level: 'degraded' }
  }

  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

export function getRAGEngineEmbeddingModel(resource: any): string {
  const embedding = resource.spec?.embedding
  if (!embedding) return '-'
  if (embedding.local) return embedding.local.modelID || embedding.local.image || '-'
  if (embedding.remote) return embedding.remote.url || 'remote'
  return '-'
}

export function getRAGEngineInstanceType(resource: any): string {
  return resource.spec?.compute?.instanceType || '-'
}
