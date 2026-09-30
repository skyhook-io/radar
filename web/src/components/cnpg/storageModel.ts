import { formatBytes } from './lsn'
import type { CNPGStorageTarget } from '../../api/cnpg-storage'

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
