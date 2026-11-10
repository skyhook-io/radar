/**
 * A PostgreSQL LSN ("16/B374D848") as a byte position. Undefined when it does
 * not parse: an unreadable position must not read as zero backlog.
 */
export function parseLsn(lsn: string | undefined): number | undefined {
  if (!lsn) return undefined
  const m = /^([0-9A-Fa-f]{1,8})\/([0-9A-Fa-f]{1,8})$/.exec(lsn.trim())
  if (!m) return undefined
  return parseInt(m[1], 16) * 0x1_0000_0000 + parseInt(m[2], 16)
}

/** Bytes from `behind` to `ahead`; undefined when either is unknown. Never negative. */
export function lsnDistance(ahead: string | undefined, behind: string | undefined): number | undefined {
  const a = parseLsn(ahead)
  const b = parseLsn(behind)
  if (a === undefined || b === undefined) return undefined
  return Math.max(0, a - b)
}

/**
 * A standby's backlog from the position it reports itself, for one with no
 * pg_stat_replication row. Positions on different timelines aren't
 * comparable: after a failover a diverged standby can read as caught up.
 */
export function standbyOwnBacklog(
  primary: { currentLsn?: string; timeline?: number } | undefined,
  standby: { replayLsn?: string; timeline?: number } | undefined,
): number | undefined {
  if (primary?.timeline === undefined || primary.timeline !== standby?.timeline) return undefined
  return lsnDistance(primary.currentLsn, standby.replayLsn)
}

export function formatBytes(n: number | undefined): string {
  if (n === undefined) return '—'
  const u = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let v = n
  let i = 0
  while (v >= 1024 && i < u.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(v >= 10 || i === 0 ? 0 : 1)} ${u[i]}`
}
