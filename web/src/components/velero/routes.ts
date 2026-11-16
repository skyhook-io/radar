import type { RadarFeatureSupport } from '../../api/radarFeatures'
export type VeleroScreen = 'overview' | 'locations'

export const VELERO_SCREENS: { id: VeleroScreen; label: string; title: string; path: string }[] = [
  { id: 'overview', label: 'Overview', title: 'Velero overview', path: '/velero' },
  { id: 'locations', label: 'Storage locations', title: 'Velero storage locations', path: '/velero/locations' },
]

export function parseVeleroRoute(pathname: string): { screen: VeleroScreen } {
  const seg = pathname.replace(/^\/+/, '').split('/')
  if (seg[0] !== 'velero') return { screen: 'overview' }
  return { screen: seg[1] === 'locations' ? 'locations' : 'overview' }
}

export const VELERO_GROUP = 'velero.io'

export const VELERO_SCHEDULE_KIND = { name: 'schedules', kind: 'Schedule', group: VELERO_GROUP }

/**
 * What the Velero Schedule kind's Resources route shows: the Overview when
 * this Radar serves the Velero views; a loader while capabilities load for the
 * first time, so the table never flashes before it; otherwise the table.
 */
export function veleroScheduleKindListMode(support: RadarFeatureSupport, capabilitiesPending: boolean): 'view' | 'wait' | 'table' {
  if (support === 'supported') return 'view'
  return support === 'unknown' && capabilitiesPending ? 'wait' : 'table'
}
