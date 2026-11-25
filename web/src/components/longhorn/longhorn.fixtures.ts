import type { LonghornVolumeReading, LonghornWorkspaceResponse } from '@skyhook-io/k8s-ui'

export const hoursAgo = (h: number) => new Date(Date.now() - h * 3_600_000).toISOString()

export function volume(over: Partial<LonghornVolumeReading> = {}): LonghornVolumeReading {
  return {
    namespace: 'longhorn-system',
    name: 'pvc-1',
    uid: over.name ?? 'pvc-1',
    health: 'healthy',
    state: 'attached',
    robustness: 'healthy',
    size: 10 * 1024 ** 3,
    numberOfReplicas: 3,
    healthyReplicas: 3,
    use: { state: 'bound', persistentVolume: 'pvc-1', claim: { namespace: 'shop', name: 'data' } },
    ...over,
  }
}

export function workspace(over: Partial<LonghornWorkspaceResponse> = {}): LonghornWorkspaceResponse {
  const volumes = over.volumes ?? [volume()]
  const count = (h: LonghornVolumeReading['health']) => volumes.filter((v) => v.health === h).length
  return {
    observedAt: new Date().toISOString(),
    installed: true,
    context: 'kind-demo',
    coverage: { volumes: { state: 'full' }, engines: { state: 'full' }, persistentVolumes: { state: 'full' } },
    counts: { healthy: count('healthy'), degraded: count('degraded'), faulted: count('faulted'), detached: count('detached'), unknown: count('unknown'), complete: true },
    volumes,
    issues: [],
    issueTotal: 0,
    ...over,
  }
}
