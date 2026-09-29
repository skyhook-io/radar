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
