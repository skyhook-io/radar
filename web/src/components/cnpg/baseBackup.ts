import type { Fact } from '@skyhook-io/k8s-ui'
import type { CNPGRuntimeBaseBackup, CNPGRuntimeResponse } from '../../api/cnpg'
import { formatBytes } from './lsn'

export const CNPG_BASE_BACKUP_SOURCE =
  "The primary's instance manager (pg_stat_progress_basebackup, joining instances only)"

export function describeCNPGBaseBackup(bb: CNPGRuntimeBaseBackup): string {
  const who = bb.instance ? `to new instance ${bb.instance}` : `for ${bb.applicationName}`
  const progress =
    bb.totalBytes !== undefined && bb.totalBytes > 0
      ? `${formatBytes(bb.streamedBytes)} of ${formatBytes(bb.totalBytes)} (${Math.min(100, (bb.streamedBytes / bb.totalBytes) * 100).toFixed(0)} %)`
      : `${formatBytes(bb.streamedBytes)} streamed, total not estimated yet`
  return `Base backup ${who}: ${bb.phase}, ${progress}`
}

/**
 * The primary's running base backups. Undefined when the primary's report was
 * not read, so the Overview omits the fact rather than claiming none.
 */
export function cnpgBaseBackupFacts(rt: CNPGRuntimeResponse | undefined): { fact: Fact; rows: CNPGRuntimeBaseBackup[] } | undefined {
  if (!rt || rt.permission.proxy === 'denied') return undefined
  const primary = rt.instances.find((i) => i.role === 'primary')
  if (!primary || (primary.status.state !== 'ok' && primary.status.state !== 'partial')) return undefined
  const rows = primary.status.baseBackups
  if (!rows) return undefined
  if (rows.length === 0) return { fact: { text: 'None running', tone: 'neutral', source: CNPG_BASE_BACKUP_SOURCE }, rows }
  return {
    fact: {
      text: rows.length === 1 ? describeCNPGBaseBackup(rows[0]) : `${rows.length} base backups running`,
      tone: 'neutral',
      source: CNPG_BASE_BACKUP_SOURCE,
    },
    rows,
  }
}
