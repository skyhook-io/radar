import type { CNPGBackupMethod } from '../../../api/cnpg'

export interface StandbyChoice {
  pod: string
  podUID: string
  ineligible?: string
  replayLagSeconds?: number
  state?: string
  syncState?: string
}

function pad(n: number) {
  return String(n).padStart(2, '0')
}

/** `<cluster>-<yyyymmddhhmmss>` in UTC, the name kubectl-cnpg uses. */
export function backupNameFor(cluster: string, now = new Date()): string {
  const ts = `${now.getUTCFullYear()}${pad(now.getUTCMonth() + 1)}${pad(now.getUTCDate())}${pad(now.getUTCHours())}${pad(now.getUTCMinutes())}${pad(now.getUTCSeconds())}`
  return `${cluster}-${ts}`
}

export function describeBackupMethod(m: CNPGBackupMethod): string {
  switch (m.method) {
    case 'plugin':
      return `Plugin ${m.pluginName ?? ''}${m.capability === 'unknown' ? ' (backup support not reported)' : ''}`.trim()
    case 'volumeSnapshot':
      return 'Volume snapshot'
    default:
      return 'Barman object store (in-tree, deprecated)'
  }
}

/** Synchronous standbys first, then the least replay lag; ineligible never. */
export function pickDefaultStandby(standbys: StandbyChoice[]): StandbyChoice | undefined {
  const eligible = standbys.filter((s) => !s.ineligible)
  const rank = (s: StandbyChoice) => (s.syncState === 'sync' || s.syncState === 'quorum' ? 0 : 1)
  return [...eligible].sort(
    (a, b) => rank(a) - rank(b) || (a.replayLagSeconds ?? Number.POSITIVE_INFINITY) - (b.replayLagSeconds ?? Number.POSITIVE_INFINITY),
  )[0]
}

const BARMAN_PLUGIN = 'barman-cloud.cloudnative-pg.io'

export type RestoreSource =
  | { kind: 'objectStore'; objectStore: string; serverName: string }
  | { kind: 'inTree'; barmanObjectStore: Record<string, unknown>; serverName: string }
  | { kind: 'backup'; backup: string }

/** Where a Cluster's backups can be restored from, in order of preference. */
export function restoreSourcesFor(cluster: any, backups: any[]): RestoreSource[] {
  const name = cluster?.metadata?.name
  const out: RestoreSource[] = []
  const plugin = (cluster?.spec?.plugins ?? []).find((p: any) => p?.name === BARMAN_PLUGIN && p?.enabled !== false)
  if (plugin?.parameters?.barmanObjectName) {
    out.push({ kind: 'objectStore', objectStore: plugin.parameters.barmanObjectName, serverName: plugin.parameters.serverName || name })
  }
  const inTree = cluster?.spec?.backup?.barmanObjectStore
  if (inTree) out.push({ kind: 'inTree', barmanObjectStore: inTree, serverName: inTree.serverName || name })
  for (const b of backups) {
    if (b?.spec?.cluster?.name === name && b?.status?.phase === 'completed') out.push({ kind: 'backup', backup: b.metadata?.name })
  }
  return out
}

/**
 * A new Cluster that bootstraps from `source`. It copies what the restored
 * data needs (image, storage) and nothing that would make it write to the
 * source's backup location.
 */
export function buildRestoreManifest(cluster: any, source: RestoreSource, newName: string, targetTime?: string): Record<string, unknown> {
  const spec = cluster?.spec ?? {}
  const recovery: Record<string, unknown> =
    source.kind === 'backup' ? { backup: { name: source.backup } } : { source: 'origin' }
  if (targetTime) recovery.recoveryTarget = { targetTime }
  const out: Record<string, any> = {
    apiVersion: 'postgresql.cnpg.io/v1',
    kind: 'Cluster',
    metadata: { name: newName, namespace: cluster?.metadata?.namespace },
    spec: {
      instances: 1,
      ...(spec.imageCatalogRef ? { imageCatalogRef: spec.imageCatalogRef } : spec.imageName ? { imageName: spec.imageName } : {}),
      ...(spec.storage ? { storage: spec.storage } : {}),
      ...(spec.walStorage ? { walStorage: spec.walStorage } : {}),
      bootstrap: { recovery },
    },
  }
  if (source.kind === 'objectStore') {
    out.spec.externalClusters = [
      { name: 'origin', plugin: { name: BARMAN_PLUGIN, parameters: { barmanObjectName: source.objectStore, serverName: source.serverName } } },
    ]
  } else if (source.kind === 'inTree') {
    out.spec.externalClusters = [{ name: 'origin', barmanObjectStore: { ...source.barmanObjectStore, serverName: source.serverName } }]
  }
  return out
}
