import type { RadarFeatureSupport } from '../../api/radarFeatures'

export const VELERO_GROUP = 'velero.io'

/** The one Velero view; the schedule and storage-location tables are those kinds' lists. */
export const VELERO_OVERVIEW = { label: 'Overview', title: 'Velero overview', path: '/velero' }

export const VELERO_SCHEDULES_PATH = '/resources/schedules?apiGroup=velero.io'
export const VELERO_LOCATIONS_PATH = '/resources/backupstoragelocations?apiGroup=velero.io'

export const VELERO_SCHEDULE_KIND = { name: 'schedules', kind: 'Schedule', group: VELERO_GROUP }
export const VELERO_LOCATION_KIND = { name: 'backupstoragelocations', kind: 'BackupStorageLocation', group: VELERO_GROUP }

/**
 * What a Velero kind's Resources route shows when Radar has a richer list for
 * it (Schedule, BackupStorageLocation): that list when this Radar serves the
 * Velero views; a loader while capabilities load for the first time, so the
 * generic table never flashes before it; otherwise the generic table.
 */
export function veleroKindListMode(support: RadarFeatureSupport, capabilitiesPending: boolean): 'view' | 'wait' | 'table' {
  if (support === 'supported') return 'view'
  return support === 'unknown' && capabilitiesPending ? 'wait' : 'table'
}
