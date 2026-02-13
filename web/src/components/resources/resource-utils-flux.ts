// FluxCD CRD utility functions — extracted from resource-utils.ts

import type { StatusBadge } from './resource-utils'
import { healthColors } from './resource-utils'
import type { FluxCondition } from '../../types/gitops'

// ============================================================================
// FLUXCD STATUS UTILITIES
// ============================================================================

/**
 * Generic status function for FluxCD resources that follow the standard Ready condition pattern.
 * Works for: GitRepository, OCIRepository, HelmRepository, Alert
 */
export function getFluxResourceStatus(resource: any): StatusBadge {
  const conditions: FluxCondition[] = resource.status?.conditions || []
  const readyCondition = conditions.find((c) => c.type === 'Ready')

  if (resource.spec?.suspend) {
    return { text: 'Suspended', color: healthColors.degraded, level: 'degraded' }
  }
  if (readyCondition?.status === 'True') {
    return { text: 'Ready', color: healthColors.healthy, level: 'healthy' }
  }
  if (readyCondition?.status === 'False') {
    return { text: 'Not Ready', color: healthColors.unhealthy, level: 'unhealthy' }
  }
  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

// Aliases for specific resource types (all use the same logic)
export const getGitRepositoryStatus = getFluxResourceStatus
export const getOCIRepositoryStatus = getFluxResourceStatus
export const getHelmRepositoryStatus = getFluxResourceStatus
export const getFluxAlertStatus = getFluxResourceStatus

/**
 * Kustomization has additional Healthy condition check
 */
export function getKustomizationStatus(ks: any): StatusBadge {
  const conditions: FluxCondition[] = ks.status?.conditions || []
  const readyCondition = conditions.find((c) => c.type === 'Ready')
  const healthyCondition = conditions.find((c) => c.type === 'Healthy')

  if (ks.spec?.suspend) {
    return { text: 'Suspended', color: healthColors.degraded, level: 'degraded' }
  }
  if (readyCondition?.status === 'True') {
    // Check health condition for more nuanced status
    if (healthyCondition?.status === 'False') {
      return { text: 'Unhealthy', color: healthColors.degraded, level: 'degraded' }
    }
    return { text: 'Ready', color: healthColors.healthy, level: 'healthy' }
  }
  if (readyCondition?.status === 'False') {
    return { text: 'Not Ready', color: healthColors.unhealthy, level: 'unhealthy' }
  }
  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

/**
 * HelmRelease has Released condition and remediation detection
 */
export function getFluxHelmReleaseStatus(hr: any): StatusBadge {
  const conditions: FluxCondition[] = hr.status?.conditions || []
  const readyCondition = conditions.find((c) => c.type === 'Ready')
  const releasedCondition = conditions.find((c) => c.type === 'Released')

  if (hr.spec?.suspend) {
    return { text: 'Suspended', color: healthColors.degraded, level: 'degraded' }
  }
  if (readyCondition?.status === 'True') {
    return { text: 'Ready', color: healthColors.healthy, level: 'healthy' }
  }
  if (readyCondition?.status === 'False') {
    // Check if it's a remediation in progress
    const reason = readyCondition?.reason || ''
    if (reason.includes('Remediation') || reason.includes('Retry')) {
      return { text: 'Remediating', color: healthColors.degraded, level: 'degraded' }
    }
    return { text: 'Failed', color: healthColors.unhealthy, level: 'unhealthy' }
  }
  if (releasedCondition?.status === 'True') {
    return { text: 'Released', color: healthColors.healthy, level: 'healthy' }
  }
  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

// ============================================================================
// FLUXCD TABLE CELL UTILITIES
// ============================================================================

export function getGitRepositoryUrl(repo: any): string {
  return repo.spec?.url || '-'
}

export function getGitRepositoryRef(repo: any): string {
  const ref = repo.spec?.ref
  if (!ref) return '-'
  if (ref.branch) return ref.branch
  if (ref.tag) return ref.tag
  if (ref.semver) return ref.semver
  if (ref.commit) return ref.commit.substring(0, 7)
  return '-'
}

export function getGitRepositoryRevision(repo: any): string {
  const artifact = repo.status?.artifact
  if (!artifact?.revision) return '-'
  // Format: "branch@sha1:abc123..." or just "sha1:abc123"
  const rev = artifact.revision
  const shaMatch = rev.match(/sha1:([a-f0-9]+)/)
  if (shaMatch) return shaMatch[1].substring(0, 7)
  return rev.substring(0, 12)
}

export function getOCIRepositoryUrl(repo: any): string {
  return repo.spec?.url || '-'
}

export function getOCIRepositoryRef(repo: any): string {
  const ref = repo.spec?.ref
  if (!ref) return '-'
  if (ref.tag) return ref.tag
  if (ref.semver) return ref.semver
  if (ref.digest) return ref.digest.substring(0, 12)
  return '-'
}

export function getOCIRepositoryRevision(repo: any): string {
  const artifact = repo.status?.artifact
  if (!artifact?.revision) return '-'
  // Usually a digest like "sha256:abc123..."
  const rev = artifact.revision
  if (rev.startsWith('sha256:')) return rev.substring(7, 19)
  return rev.substring(0, 12)
}

export function getHelmRepositoryUrl(repo: any): string {
  return repo.spec?.url || '-'
}

export function getHelmRepositoryType(repo: any): string {
  return repo.spec?.type || 'default'
}

export function getKustomizationSource(ks: any): string {
  const ref = ks.spec?.sourceRef
  if (!ref) return '-'
  return `${ref.kind}/${ref.name}`
}

export function getKustomizationPath(ks: any): string {
  return ks.spec?.path || './'
}

export function getKustomizationInventory(ks: any): number {
  return ks.status?.inventory?.entries?.length || 0
}

export function getFluxHelmReleaseChart(hr: any): string {
  const chart = hr.spec?.chart?.spec
  if (!chart) return '-'
  return chart.chart || '-'
}

export function getFluxHelmReleaseVersion(hr: any): string {
  const chart = hr.spec?.chart?.spec
  if (!chart?.version) return '*'
  return chart.version
}

export function getFluxHelmReleaseRevision(hr: any): number {
  // Helm release revision number is in history[0].version
  return hr.status?.history?.[0]?.version || 0
}

export function getFluxAlertProvider(alert: any): string {
  const ref = alert.spec?.providerRef
  if (!ref) return '-'
  return ref.name || '-'
}

export function getFluxAlertEventCount(alert: any): number {
  return alert.spec?.eventSources?.length || 0
}
