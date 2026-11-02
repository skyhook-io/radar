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

  // Check for suspended (auto-sync off). Honor both the current
  // "radarhq.io/suspended-prune" annotation and the legacy "skyhook.io/..."
  // key still present on Applications suspended by older Radar builds.
  // The annotation value stores the prior prune state as "true"/"false" for
  // restore on resume — both strings are truthy in JS, which is intentional:
  // the *presence* of the annotation is what signals suspended, not its value.
  const annotations = app.metadata?.annotations
  const suspendedByRadar = annotations?.['radarhq.io/suspended-prune'] || annotations?.['skyhook.io/suspended-prune']
  if (health === 'Suspended' || (!isArgoAutoSyncEnabled(app) && suspendedByRadar)) {
    // Suspended = an operator deliberately paused this app — intentional, not a
    // degradation. Neutral (sky), matching the backend rollup (mapArgoHealth) so a
    // suspended app reads the same Idle tone in Applications, the resource table,
    // and GitOps instead of amber in some surfaces and sky in others.
    return { text: 'Suspended', color: healthColors.neutral, level: 'neutral' }
  }

  // Operation in progress
  if (opPhase === 'Running') {
    return { text: 'Syncing', color: healthColors.degraded, level: 'degraded' }
  }
  if (opPhase === 'Terminating') {
    return { text: 'Terminating', color: healthColors.degraded, level: 'degraded' }
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

// isArgoAutoSyncEnabled reports whether an Argo CD Application auto-syncs.
// Argo CD 3.x can keep spec.syncPolicy.automated, with its prune and selfHeal
// settings, while switching automation off with `enabled: false`, so the block
// being present is not enough: only an absent or true `enabled` counts. Same
// rule as Argo's SyncPolicy.IsAutomatedSyncEnabled and the backend's
// gitops.ArgoAutoSyncEnabled, so the UI and the rollback gate agree.
export function isArgoAutoSyncEnabled(app: any): boolean {
  const automated = app?.spec?.syncPolicy?.automated
  if (automated == null || typeof automated !== 'object') return false
  return automated.enabled !== false
}

// isArgoAutoSyncSwitchedOff reports an automated block kept with
// `enabled: false`. Only a Radar advertising `argoRollbackSync` turns such an
// app's auto-sync back on; an older one's resume leaves `enabled: false` set.
export function isArgoAutoSyncSwitchedOff(app: any): boolean {
  const automated = app?.spec?.syncPolicy?.automated
  return automated != null && typeof automated === 'object' && automated.enabled === false
}

// argoResumeSettings says what Enable auto-sync turns on for an Application
// Radar didn't suspend. A block switched off in place keeps its own prune and
// selfHeal. Without a block it depends on the Radar serving the request: one
// advertising argoSyncInheritance starts prune off, and one advertising
// argoEnableSelfHealOff starts selfHeal off; an older one turns each on. Null
// while either isn't known.
export function argoResumeSettings(app: any, serverStartsPruneOff: boolean | undefined, serverStartsSelfHealOff?: boolean): { prune: boolean; selfHeal: boolean } | null {
  const automated = app?.spec?.syncPolicy?.automated
  if (automated != null && typeof automated === 'object') {
    return { prune: automated.prune === true, selfHeal: automated.selfHeal === true }
  }
  if (serverStartsPruneOff === undefined) return null
  return { prune: !serverStartsPruneOff, selfHeal: !serverStartsSelfHealOff }
}

// isArgoHydrated: spec.sourceHydrator sets the Application's source, taking
// precedence over spec.source and spec.sources.
function isArgoHydrated(app: any): boolean {
  return app?.spec?.sourceHydrator != null
}

// isArgoMultiSource: the Application syncs several sources, so a single
// revision override doesn't apply. Leftover spec.sources beside a hydrator don't count.
export function isArgoMultiSource(app: any): boolean {
  return !isArgoHydrated(app) && Array.isArray(app?.spec?.sources) && app.spec.sources.length > 0
}

// argoComparedRevisionCurrent mirrors the rule a Radar advertising
// argoSyncInheritance applies to a sync without a revision: it uses the last
// compared revision(s) only when the comparison was for the Application's
// current source(s), one revision per source. Hydrated Applications are
// never pinned.
export function argoComparedRevisionCurrent(app: any): boolean {
  if (isArgoHydrated(app)) return false
  const sync = app?.status?.sync
  if (isArgoMultiSource(app)) {
    const sources = app.spec.sources
    return Array.isArray(sync?.revisions) && sync.revisions.length === sources.length && jsonEqual(sources, sync?.comparedTo?.sources)
  }
  const source = app?.spec?.source
  return !!source && Object.keys(source).length > 0 && !!sync?.revision && jsonEqual(source, sync?.comparedTo?.source)
}

function jsonEqual(a: unknown, b: unknown): boolean {
  if (a === b) return true
  if (Array.isArray(a)) return Array.isArray(b) && a.length === b.length && a.every((item, i) => jsonEqual(item, b[i]))
  if (a && b && typeof a === 'object' && typeof b === 'object' && !Array.isArray(b)) {
    const aKeys = Object.keys(a)
    return aKeys.length === Object.keys(b).length && aKeys.every((key) => jsonEqual((a as any)[key], (b as any)[key]))
  }
  return false
}

// argoApplicationSyncOptions returns spec.syncPolicy.syncOptions: the options
// Argo CD applies to every sync of the Application unless a sync names its own.
export function argoApplicationSyncOptions(app: any): string[] {
  const options = app?.spec?.syncPolicy?.syncOptions
  return Array.isArray(options) ? options.filter((option): option is string => typeof option === 'string') : []
}

export function isArgoOperationInProgress(app: any): boolean {
  const phase = app?.status?.operationState?.phase
  return app?.operation != null || phase === 'Running' || phase === 'Terminating'
}

// Radar suspends an Argo Application by clearing spec.syncPolicy.automated and
// recording the prior block in annotations (the whole block, plus the
// prune/selfHeal flags older builds read) so Resume can restore it. The *presence* of any of these annotations marks the app as suspended —
// independent of health.status, which stays whatever the app's resources report
// (often Missing/OutOfSync, never literally "Suspended"). Both the current
// radarhq.io keys and the legacy skyhook.io keys (still on apps suspended by
// older builds) count. Shared so the fleet table and the detail page can't
// disagree on whether an app is suspended.
export function isArgoSuspendedByRadar(app: any): boolean {
  const a = app?.metadata?.annotations
  return Boolean(
    a?.['radarhq.io/suspended-prune'] ||
      a?.['radarhq.io/suspended-selfheal'] ||
      a?.['skyhook.io/suspended-prune'] ||
      a?.['skyhook.io/suspended-selfheal'],
  )
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
