import { healthColors, type StatusBadge } from './resource-utils'

export const LONGHORN_GROUP = 'longhorn.io'

export type LonghornHealth = 'healthy' | 'degraded' | 'faulted' | 'detached' | 'unknown'

/** Mirrors pkg/longhorn VolumeHealth; both run over pkg/longhorn/testdata/volumes.json. */
export function longhornVolumeHealth(volume: any): LonghornHealth {
  const robustness = volume?.status?.robustness
  if (robustness === 'healthy' || robustness === 'degraded' || robustness === 'faulted') return robustness
  if (volume?.status?.state === 'detached' && robustness === 'unknown') return 'detached'
  return 'unknown'
}

const badge = (text: string, level: StatusBadge['level']): StatusBadge => ({ text, level, color: healthColors[level!] })

// degraded is routine while replicas rebuild after a drain or restart, so it
// stays amber; faulted (no usable replica) is the one that reads red.
export function getLonghornVolumeStatus(volume: any): StatusBadge {
  switch (longhornVolumeHealth(volume)) {
    case 'healthy': return badge('Healthy', 'healthy')
    case 'degraded': return badge('Degraded', 'degraded')
    case 'faulted': return badge('Faulted', 'unhealthy')
    case 'detached': return badge('Detached', 'neutral')
    default: return badge('Unknown', 'unknown')
  }
}

/** Replicas in mode RW per the Engine; undefined (unknown) without its replicaModeMap. */
export function longhornHealthyReplicas(engine: any): number | undefined {
  const modes = engine?.status?.replicaModeMap
  if (!modes || typeof modes !== 'object') return undefined
  return Object.values(modes).filter((mode) => mode === 'RW').length
}

export interface LonghornRebuild { replica: string; progress: number; state: string; error: string }

/** Rebuilds in progress or failed, from the Engine's rebuildStatus (keyed by replica address). */
export function longhornRebuilds(engine: any): LonghornRebuild[] {
  const statuses = engine?.status?.rebuildStatus
  if (!statuses || typeof statuses !== 'object') return []
  return Object.entries(statuses)
    .map(([replica, s]: [string, any]) => ({ replica, progress: Number(s?.progress) || 0, state: s?.state || '', error: s?.error || '', rebuilding: !!s?.isRebuilding }))
    .filter((r) => r.rebuilding || r.error)
    .sort((a, b) => a.replica.localeCompare(b.replica))
    .map(({ rebuilding: _rebuilding, ...r }) => r)
}
