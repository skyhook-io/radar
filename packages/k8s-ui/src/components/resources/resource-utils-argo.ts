// ArgoCD CRD utility functions — extracted from resource-utils.ts

import type { StatusBadge } from './resource-utils'
import { healthColors } from './resource-utils'

// ============================================================================
// ARGOCD STATUS UTILITIES
// ============================================================================

export function getArgoApplicationStatus(app: any): StatusBadge {
  const health = app.status?.health?.status
  const sync = app.status?.sync?.status
  const opPhase = app.status?.operationState?.phase

  // Check for suspended (no automated sync policy). Honor both the current
  // "radarhq.io/suspended-prune" annotation and the legacy "skyhook.io/..."
  // key still present on Applications suspended by older Radar builds.
  // The annotation value stores the prior prune state as "true"/"false" for
  // restore on resume — both strings are truthy in JS, which is intentional:
  // the *presence* of the annotation is what signals suspended, not its value.
  const hasAutomatedSync = !!app.spec?.syncPolicy?.automated
  const annotations = app.metadata?.annotations
  const suspendedByRadar = annotations?.['radarhq.io/suspended-prune'] || annotations?.['skyhook.io/suspended-prune']
  if (health === 'Suspended' || (!hasAutomatedSync && suspendedByRadar)) {
    return { text: 'Suspended', color: healthColors.degraded, level: 'degraded' }
  }

  // Operation in progress
  if (opPhase === 'Running') {
    return { text: 'Syncing', color: healthColors.degraded, level: 'degraded' }
  }

  // Failed operation
  if (opPhase === 'Failed' || opPhase === 'Error') {
    return { text: 'Failed', color: healthColors.unhealthy, level: 'unhealthy' }
  }

  // Health-based status
  if (health === 'Healthy' && sync === 'Synced') {
    return { text: 'Healthy', color: healthColors.healthy, level: 'healthy' }
  }
  if (health === 'Degraded') {
    return { text: 'Degraded', color: healthColors.unhealthy, level: 'unhealthy' }
  }
  if (health === 'Progressing') {
    return { text: 'Progressing', color: healthColors.degraded, level: 'degraded' }
  }
  if (health === 'Missing') {
    return { text: 'Missing', color: healthColors.unhealthy, level: 'unhealthy' }
  }

  // Sync-based status
  if (sync === 'OutOfSync') {
    return { text: 'OutOfSync', color: healthColors.degraded, level: 'degraded' }
  }

  return { text: health || sync || 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

// ============================================================================
// ARGOCD TABLE CELL UTILITIES
// ============================================================================

export function getArgoApplicationProject(app: any): string {
  return app.spec?.project || 'default'
}

export function getArgoApplicationSync(app: any): { status: string; color: string } {
  const sync = app.status?.sync?.status
  switch (sync) {
    case 'Synced':
      return { status: 'Synced', color: healthColors.healthy }
    case 'OutOfSync':
      return { status: 'OutOfSync', color: healthColors.degraded }
    default:
      return { status: sync || 'Unknown', color: healthColors.unknown }
  }
}

export function getArgoApplicationHealth(app: any): { status: string; color: string } {
  const health = app.status?.health?.status
  switch (health) {
    case 'Healthy':
      return { status: 'Healthy', color: healthColors.healthy }
    case 'Progressing':
      return { status: 'Progressing', color: healthColors.degraded }
    case 'Degraded':
      return { status: 'Degraded', color: healthColors.unhealthy }
    case 'Suspended':
      return { status: 'Suspended', color: healthColors.degraded }
    case 'Missing':
      return { status: 'Missing', color: healthColors.unhealthy }
    default:
      return { status: health || 'Unknown', color: healthColors.unknown }
  }
}

export function getArgoApplicationRepo(app: any): string {
  // Can be source (single) or sources (multi-source)
  const source = app.spec?.source || app.spec?.sources?.[0]
  if (!source?.repoURL) return '-'
  // Shorten the URL for display
  const url = source.repoURL
  try {
    const parsed = new URL(url)
    return parsed.pathname.replace(/^\//, '').replace(/\.git$/, '')
  } catch {
    return url
  }
}

export function getArgoApplicationSetGenerators(appSet: any): string {
  const generators = appSet.spec?.generators || []
  if (generators.length === 0) return '-'
  // Get the type of each generator
  const types = generators.map((g: any) => {
    const keys = Object.keys(g)
    return keys[0] || 'unknown'
  })
  return types.join(', ')
}

export function getArgoApplicationSetTemplate(appSet: any): string {
  const template = appSet.spec?.template?.metadata?.name
  return template || '-'
}

export function getArgoApplicationSetAppCount(appSet: any): number {
  return appSet.status?.conditions?.find((c: any) => c.type === 'ResourcesUpToDate')
    ? appSet.status?.applicationStatus?.length || 0
    : 0
}

export function getArgoApplicationSetStatus(appSet: any): StatusBadge {
  const conditions = appSet.status?.conditions || []
  const errorCondition = conditions.find((c: any) => c.type === 'ErrorOccurred' && c.status === 'True')
  if (errorCondition) {
    return { text: 'Error', color: healthColors.unhealthy, level: 'unhealthy' }
  }
  const resourcesUpToDate = conditions.find((c: any) => c.type === 'ResourcesUpToDate')
  if (resourcesUpToDate?.status === 'True') {
    return { text: 'Ready', color: healthColors.healthy, level: 'healthy' }
  }
  return { text: 'Unknown', color: healthColors.unknown, level: 'unknown' }
}

export function getArgoAppProjectDescription(project: any): string {
  return project.spec?.description || '-'
}

export function getArgoAppProjectDestinations(project: any): number {
  const destinations = project.spec?.destinations || []
  // '*' means all, count as 1
  if (destinations.some((d: any) => d.server === '*' && d.namespace === '*')) return Infinity
  return destinations.length
}

export function getArgoAppProjectSources(project: any): number {
  const sources = project.spec?.sourceRepos || []
  if (sources.includes('*')) return Infinity
  return sources.length
}
