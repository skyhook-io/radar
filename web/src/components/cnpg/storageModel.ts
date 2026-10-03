import { formatBytes } from './lsn'
import { cnpgDiskTone, type HealthLevel } from '@skyhook-io/k8s-ui'
import type { CNPGStorageTarget, CNPGStorageVolume, CNPGStorageWAL } from '../../api/cnpg-storage'

function setPath(obj: Record<string, any>, path: string[], value: unknown) {
  let cur = obj
  for (const p of path.slice(0, -1)) {
    cur[p] = cur[p] && typeof cur[p] === 'object' ? { ...cur[p] } : {}
    cur = cur[p]
  }
  cur[path[path.length - 1]] = value
}

// Where the size lives under the storage block the target names.
function sizePath(t: CNPGStorageTarget): string[] {
  return t.field.endsWith('.pvcTemplate.resources.requests.storage') ? ['pvcTemplate', 'resources', 'requests', 'storage'] : ['size']
}

/**
 * The manifest server-side applies only the size. Tablespaces are a list the
 * CRD does not merge by key, so the whole list is sent with one size changed.
 */
export function buildResizeManifest(cluster: any, target: CNPGStorageTarget, size: string): Record<string, any> {
  const spec: Record<string, any> = {}
  if (target.role === 'PG_TABLESPACE') {
    spec.tablespaces = (cluster?.spec?.tablespaces ?? []).map((ts: any) => {
      if (ts?.name !== target.tablespace) return ts
      const storage = { ...(ts.storage ?? {}) }
      setPath(storage, sizePath(target), size)
      return { ...ts, storage }
    })
  } else {
    const block: Record<string, any> = {}
    setPath(block, sizePath(target), size)
    spec[target.role === 'PG_WAL' ? 'walStorage' : 'storage'] = block
  }
  return {
    apiVersion: 'postgresql.cnpg.io/v1',
    kind: 'Cluster',
    metadata: { name: cluster?.metadata?.name, namespace: cluster?.metadata?.namespace },
    spec,
  }
}

/**
 * WAL held by replication slots on one instance. Slots overlap (they can
 * hold the same segments), so the figure is the largest slot, never a sum.
 */
export function cnpgSlotRetentionText(slots: { slot: string; bytes: number }[]): string {
  if (slots.length === 0) return 'No slots'
  const max = slots.reduce((m, s) => Math.max(m, s.bytes), 0)
  if (max === 0) return formatBytes(0)
  return slots.length === 1 ? formatBytes(max) : `${formatBytes(max)} (largest slot)`
}

/**
 * Why StorageClass expansion is unknown, when it is the same reason for
 * every volume whose class could not be read (and more than one), so it can be
 * said once for the page instead of under each volume.
 */
export function cnpgSharedExpansionGap(volumes: { storageClass: { name?: string; allowVolumeExpansion?: boolean; reason?: string } }[]): string | undefined {
  const unknown = volumes.filter((v) => v.storageClass.name && v.storageClass.allowVolumeExpansion === undefined)
  if (unknown.length < 2) return undefined
  const reasons = new Set(unknown.map((v) => v.storageClass.reason ?? ''))
  return reasons.size === 1 ? [...reasons][0] || undefined : undefined
}

/**
 * A lower bound on a volume's used space when kubelet didn't measure it: the
 * WAL the instance reports lives on this claim, so at least that much is used.
 */
export function cnpgWALUsageFloor(v: CNPGStorageVolume, wal: CNPGStorageWAL | undefined): { bytes: number; ratio?: number } | undefined {
  if (v.usage.state === 'ok' || !wal || wal.metrics.state !== 'ok' || wal.sizeBytes === undefined || wal.volume !== v.claim) return undefined
  return { bytes: wal.sizeBytes, ratio: v.capacityBytes ? wal.sizeBytes / v.capacityBytes : undefined }
}

/** A lower bound can prove a volume is filling, never that it's fine. */
export function cnpgFloorTone(ratio: number | undefined): HealthLevel {
  if (ratio === undefined) return 'unknown'
  const tone = cnpgDiskTone(ratio)
  return tone === 'healthy' ? 'unknown' : tone
}

/**
 * An instance's disk tone across its volumes: the worst measured or bounded
 * reading that crosses a line, otherwise healthy only when every volume was
 * measured below it. One unmeasured volume leaves a calm verdict unknown.
 */
export function cnpgInstanceDiskTone(volumes: CNPGStorageVolume[], wal: CNPGStorageWAL | undefined): HealthLevel {
  const rank: Record<string, number> = { unhealthy: 3, degraded: 2, unknown: 1, healthy: 0 }
  const tones = volumes.map((v) => (v.usage.ratio !== undefined ? cnpgDiskTone(v.usage.ratio) : cnpgFloorTone(cnpgWALUsageFloor(v, wal)?.ratio)))
  if (tones.length === 0) return 'unknown'
  return tones.reduce((a, b) => ((rank[b] ?? 1) > (rank[a] ?? 1) ? b : a))
}
