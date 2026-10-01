import { cnpgFormatLag } from '@skyhook-io/k8s-ui'
import type { CNPGActionCapability, CNPGBackupMethod } from '../../../api/cnpg'

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

/**
 * What blocks destroying an instance, for the dialog's alert. The missing
 * fence is left out when the dialog already offers "Fence first": saying it
 * again as an alert only repeats the callout. The server checks the fence
 * after every other state guard, so its reason names the fence only when
 * nothing else blocks.
 */
export function cnpgDestroyBlocker(cap: CNPGActionCapability | undefined, pod: string, fenceOffered: boolean): string | undefined {
  if (!cap || cap.allowed) return undefined
  if (fenceOffered && cap.permission !== 'denied' && cap.reason?.startsWith(`Fence ${pod} first`)) return undefined
  return cap.reason ?? 'Not allowed'
}

// Behind by this much is visibly lagging, not just between acknowledgements.
const SWITCHOVER_LAG_NOTE_SECONDS = 5

/**
 * The note under a switchover candidate that is behind. The new primary has to
 * replay the WAL it has not applied yet before it takes over, so a lagging
 * standby can make the switchover take longer; nothing is lost by choosing it.
 */
export function switchoverLagNote(s: Pick<StandbyChoice, 'pod' | 'replayLagSeconds'>): string | undefined {
  const lag = s.replayLagSeconds
  if (lag === undefined || lag <= SWITCHOVER_LAG_NOTE_SECONDS) return undefined
  return `${s.pod} is ${cnpgFormatLag(lag)} behind; the switchover may take longer while it catches up.`
}
