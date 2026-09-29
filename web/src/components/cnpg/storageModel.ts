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
