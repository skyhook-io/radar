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
