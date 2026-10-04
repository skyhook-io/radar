import { cnpgFormatLag } from '@skyhook-io/k8s-ui'
import { formatBytes } from '../lsn'
import type { CNPGBackupMethod } from '../../../api/cnpg'
import type { ActionCapability } from '../../../api/actions'

export interface StandbyChoice {
  pod: string
  podUID: string
  ineligible?: string
  /** pg_stat_replication replay_lag: the recent replay delay, which can stay high after the standby caught up. */
  replayLagSeconds?: number
  /** WAL the primary has written that this standby has not replayed, in bytes: what it still has to catch up on. */
  replayBacklogBytes?: number
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

/** Synchronous standbys first, then the least WAL still to replay, then the least replay delay; ineligible never. */
export function pickDefaultStandby(standbys: StandbyChoice[]): StandbyChoice | undefined {
  const eligible = standbys.filter((s) => !s.ineligible)
  const rank = (s: StandbyChoice) => (s.syncState === 'sync' || s.syncState === 'quorum' ? 0 : 1)
  const inf = Number.POSITIVE_INFINITY
  return [...eligible].sort(
    (a, b) =>
      rank(a) - rank(b) ||
      (a.replayBacklogBytes ?? inf) - (b.replayBacklogBytes ?? inf) ||
      (a.replayLagSeconds ?? inf) - (b.replayLagSeconds ?? inf),
  )[0]
}

/** The switchover target: the default pick until the user has chosen one (or the dialog opened on one). */
export function switchoverDefault(input: { touched: boolean; current: string | undefined; standbys: StandbyChoice[] }): string | undefined {
  return input.touched ? input.current : pickDefaultStandby(input.standbys)?.pod
}

/**
 * What blocks destroying an instance, for the dialog's alert. The missing
 * fence is left out when the dialog already offers "Fence first": saying it
 * again as an alert only repeats the callout. The server checks the fence
 * after every other state guard, so its reason names the fence only when
 * nothing else blocks.
 */
export function cnpgDestroyBlocker(cap: ActionCapability | undefined, pod: string, fenceOffered: boolean): string | undefined {
  if (!cap || cap.allowed) return undefined
  if (fenceOffered && cap.permission !== 'denied' && cap.reason?.startsWith(`Fence ${pod} first`)) return undefined
  return cap.reason ?? 'Not allowed'
}

/**
 * The note under a switchover candidate that still has WAL to replay. The
 * new primary replays it before it takes over, so the switchover can take
 * longer; nothing is lost by choosing it. Based on the LSN backlog, not on
 * replay_lag, which is a recent delay and stays high after a standby caught up.
 */
export function switchoverLagNote(s: Pick<StandbyChoice, 'pod' | 'replayBacklogBytes' | 'state'>): string | undefined {
  const b = s.replayBacklogBytes
  if (b === undefined || b <= 0) return undefined
  // No pg_stat_replication row: it isn't receiving WAL, so "catches up" would be a guess.
  if (s.state === undefined) return `${s.pod} has ${formatBytes(b)} of WAL still to replay and isn't connected to the primary to receive it.`
  return `${s.pod} has ${formatBytes(b)} of WAL still to replay; the switchover may take longer while it catches up.`
}

/** A switchover candidate's catch-up facts: the backlog first, then the replay delay. */
export function switchoverCandidateFacts(s: Pick<StandbyChoice, 'replayBacklogBytes' | 'replayLagSeconds'>): string[] {
  return [
    s.replayBacklogBytes !== undefined ? `backlog ${formatBytes(s.replayBacklogBytes)}` : null,
    s.replayLagSeconds !== undefined ? `replay delay ${cnpgFormatLag(s.replayLagSeconds)}` : null,
  ].filter((x): x is string => !!x)
}
