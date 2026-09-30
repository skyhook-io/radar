import type { CNPGRuntimeInstance } from '../../api/cnpg'

type Checkpoints = NonNullable<CNPGRuntimeInstance['metrics']['checkpoints']>

/**
 * How to read one instance's checkpoint counters. Restartpoints exist as
 * their own counters only in pg_stat_checkpointer (PostgreSQL 17+), and matter
 * on a standby or where any were done. Pressure needs enough checkpoints for
 * the requested share to mean something.
 */
export function cnpgCheckpointView(c: Checkpoints, role: CNPGRuntimeInstance['role']) {
  const total = c.timed !== undefined && c.requested !== undefined ? c.timed + c.requested : undefined
  const requestedShare = total ? (c.requested as number) / total : undefined
  return {
    total,
    requestedShare,
    pressure: requestedShare !== undefined && (total as number) >= 10 && requestedShare > 0.5,
    showRestartpoints: c.source === 'pg_stat_checkpointer' && (role !== 'primary' || !!c.restartpointsDone),
  }
}

export interface CNPGDatabaseHealthRow {
  database: string
  /** rollbacks / (commits + rollbacks); undefined when either is unreported or there were none. */
  rollbackRatio?: number
  tempFiles?: number
  tempBytes?: number
  xidAge?: number
  mxidAge?: number
}

/** One row per database any family names; a value a family did not report stays undefined. */
export function cnpgDatabaseHealthRows(m: CNPGRuntimeInstance['metrics']): CNPGDatabaseHealthRow[] {
  const rows = new Map<string, CNPGDatabaseHealthRow>()
  const row = (database: string) => {
    let r = rows.get(database)
    if (!r) rows.set(database, (r = { database }))
    return r
  }
  for (const d of m.databases ?? []) {
    const r = row(d.database)
    if (d.xactCommit !== undefined && d.xactRollback !== undefined && d.xactCommit + d.xactRollback > 0) {
      r.rollbackRatio = d.xactRollback / (d.xactCommit + d.xactRollback)
    }
    r.tempFiles = d.tempFiles
    r.tempBytes = d.tempBytes
  }
  for (const x of m.xidAge ?? []) row(x.database).xidAge = x.age
  for (const x of m.mxidAge ?? []) row(x.database).mxidAge = x.age
  return [...rows.values()].sort((a, b) => a.database.localeCompare(b.database))
}
