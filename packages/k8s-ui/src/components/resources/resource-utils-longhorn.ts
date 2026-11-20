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

/** progress is undefined when the Engine hasn't reported it. */
export interface LonghornRebuild { replica: string; progress?: number; state: string; error: string }

/** Rebuilds in progress or failed, from the Engine's rebuildStatus (keyed by replica address). */
export function longhornRebuilds(engine: any): LonghornRebuild[] {
  const statuses = engine?.status?.rebuildStatus
  if (!statuses || typeof statuses !== 'object') return []
  const names = new Map<string, string>()
  for (const [name, addr] of Object.entries(engine?.status?.currentReplicaAddressMap ?? {})) names.set(String(addr), name)
  return Object.entries(statuses)
    .map(([addr, s]: [string, any]) => ({ replica: names.get(addr.replace(/^tcp:\/\//, '')) ?? addr, progress: typeof s?.progress === 'number' && Number.isFinite(s.progress) ? s.progress : undefined, state: s?.state || '', error: s?.error || '', rebuilding: !!s?.isRebuilding }))
    .filter((r) => r.rebuilding || r.error)
    .sort((a, b) => a.replica.localeCompare(b.replica))
    .map(({ rebuilding: _rebuilding, ...r }) => r)
}

// Longhorn Volume conditions whose normal value is False; True means something
// is going on (a restore, too many snapshots, …), not that the volume failed.
const LONGHORN_FALSE_IS_NORMAL = new Set(['Restore', 'TooManySnapshots', 'WaitForBackingImage', 'BackingImageIncompatible', 'OfflineRebuilding'])

/** Tone for a Longhorn Volume condition; undefined leaves the default reading. */
export function longhornVolumeConditionTone(condition: any): 'ok' | 'warning' | 'neutral' | undefined {
  const type = condition?.type
  if (!LONGHORN_FALSE_IS_NORMAL.has(type)) return undefined
  if (condition?.status === 'False') return 'ok'
  if (condition?.status === 'True') return type === 'Restore' ? 'neutral' : 'warning'
  return undefined
}

/** True when the Volume's Scheduled condition says Longhorn can't place a replica. */
export function longhornReplicaUnschedulable(volume: any): boolean {
  return (volume?.status?.conditions ?? []).some((c: any) => c?.type === 'Scheduled' && c?.status === 'False')
}
