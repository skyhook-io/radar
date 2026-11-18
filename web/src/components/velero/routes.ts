import type { RadarFeatureSupport } from '../../api/radarFeatures'

export const VELERO_GROUP = 'velero.io'

/** The one Velero view; Radar's Schedule, storage-location, Backup and Restore tables are those kinds' lists. */
export const VELERO_OVERVIEW = { label: 'Overview', title: 'Velero overview', path: '/velero' }

/** The namespace picker's tooltip on every Velero screen: Velero keeps its objects in its own namespace, so the filter would empty the views. */
export const VELERO_NAMESPACE_FILTER_TOOLTIP = 'Velero is shown for every installation you can read — the namespace filter doesn’t apply here.'

export const VELERO_SCHEDULES_PATH = '/resources/schedules?apiGroup=velero.io'
export const VELERO_LOCATIONS_PATH = '/resources/backupstoragelocations?apiGroup=velero.io'

export const VELERO_SCHEDULE_KIND = { name: 'schedules', kind: 'Schedule', group: VELERO_GROUP }
export const VELERO_LOCATION_KIND = { name: 'backupstoragelocations', kind: 'BackupStorageLocation', group: VELERO_GROUP }
export const VELERO_BACKUP_KIND = { name: 'backups', kind: 'Backup', group: VELERO_GROUP }
export const VELERO_RESTORE_KIND = { name: 'restores', kind: 'Restore', group: VELERO_GROUP }

/**
 * A Velero Backup's kind. Its generic Delete removes only the object: the
 * backup stays in storage and Velero recreates the object from it.
 */
export function isVeleroBackupKind(plural: string | undefined, group: string | undefined): boolean {
  return veleroDetailKindFor(plural ?? '', group) === VELERO_BACKUP_KIND.name
}

/**
 * What a Velero kind's Resources route shows when Radar has a richer list for
 * it (Schedule, BackupStorageLocation, Backup, Restore): that list when this
 * Radar serves the Velero views; a loader while capabilities load for the
 * first time, so the generic table never flashes before it; otherwise the
 * generic table.
 */
export function veleroKindListMode(support: RadarFeatureSupport, capabilitiesPending: boolean): 'view' | 'wait' | 'table' {
  if (support === 'supported') return 'view'
  return support === 'unknown' && capabilitiesPending ? 'wait' : 'table'
}

/** The Velero kinds with a page of their own, and the kind list each one's crumb returns to. */
export const VELERO_DETAIL_KINDS: Record<string, { kind: string; label: string; listPath: string }> = {
  backups: { kind: 'Backup', label: 'Backups', listPath: '/resources/backups?apiGroup=velero.io' },
  restores: { kind: 'Restore', label: 'Restores', listPath: '/resources/restores?apiGroup=velero.io' },
  schedules: { kind: 'Schedule', label: 'Schedules', listPath: VELERO_SCHEDULES_PATH },
  backupstoragelocations: { kind: 'BackupStorageLocation', label: 'Storage locations', listPath: VELERO_LOCATIONS_PATH },
}

export interface VeleroDetailTarget {
  plural: string
  namespace: string
  name: string
}

export function veleroDetailKindFor(plural: string, group: string | undefined): string | null {
  const p = plural.toLowerCase()
  return group === VELERO_GROUP && VELERO_DETAIL_KINDS[p] ? p : null
}

/** `/velero` is the Overview; `/velero/<plural>/<namespace>/<name>` an object's page. */
export function parseVeleroRoute(pathname: string): { detail?: VeleroDetailTarget } {
  const seg = pathname.replace(/^\/+/, '').split('/').map((s) => {
    try {
      return decodeURIComponent(s)
    } catch {
      return s
    }
  })
  if (seg[0] !== 'velero' || !VELERO_DETAIL_KINDS[seg[1] ?? ''] || !seg[2] || !seg[3]) return {}
  return { detail: { plural: seg[1], namespace: seg[2], name: seg[3] } }
}

/** An object's page; `ctx` pins the Kubernetes context it belongs to. */
export function veleroDetailPath(target: VeleroDetailTarget, ctx?: string, tab?: string): string {
  const params = new URLSearchParams()
  if (ctx) params.set('ctx', ctx)
  if (tab) params.set('tab', tab)
  const qs = params.toString()
  return `/velero/${target.plural}/${encodeURIComponent(target.namespace)}/${encodeURIComponent(target.name)}${qs ? `?${qs}` : ''}`
}
