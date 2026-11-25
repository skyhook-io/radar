export const LONGHORN_GROUP = 'longhorn.io'

export const LONGHORN_OVERVIEW = { label: 'Overview', title: 'Longhorn overview', path: '/longhorn' }
export const LONGHORN_VOLUMES = { label: 'Volumes', title: 'Longhorn volumes', path: '/longhorn/volumes' }

/** Longhorn keeps its objects in its own namespace, so the picker would empty the views. */
export const LONGHORN_NAMESPACE_FILTER_TOOLTIP = 'Longhorn is shown for every volume you can read — the namespace filter doesn’t apply here.'

export type LonghornScreen = 'overview' | 'volumes'

export function parseLonghornRoute(pathname: string): LonghornScreen {
  return pathname.replace(/\/+$/, '') === LONGHORN_VOLUMES.path ? 'volumes' : 'overview'
}
