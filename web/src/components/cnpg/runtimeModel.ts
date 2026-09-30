import { cnpgLagTone, type HealthLevel } from '@skyhook-io/k8s-ui'
import type { CNPGRuntimeInstance, CNPGRuntimeReplication } from '../../api/cnpg'

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

// 16 MiB is one WAL segment: a standby a segment or more behind is visibly
// catching up, not just between acknowledgements.
export const CNPG_BACKLOG_DEGRADED = 16 * 1024 * 1024
const CNPG_BACKLOG_UNHEALTHY = 1024 * 1024 * 1024

const TONE_RANK: Record<HealthLevel, number> = { unhealthy: 0, alert: 1, degraded: 2, unknown: 3, neutral: 4, healthy: 5 }

function worse(a: HealthLevel, b: HealthLevel): HealthLevel {
  return TONE_RANK[a] <= TONE_RANK[b] ? a : b
}

/** A standby's catch-up tone: the worse of its byte backlog and replay delay, the delay on the same scale as the cluster's Replication fact. */
export function cnpgStandbyBacklogTone(bytes: number | undefined, replayLag: number | undefined): HealthLevel {
  if (bytes === undefined) return 'unknown'
  const byBytes: HealthLevel = bytes >= CNPG_BACKLOG_UNHEALTHY ? 'unhealthy' : bytes >= CNPG_BACKLOG_DEGRADED ? 'degraded' : 'healthy'
  return replayLag === undefined ? byBytes : worse(byBytes, cnpgLagTone(replayLag))
}

export interface CNPGStandbyHeadline {
  text: string
  tone: HealthLevel
  /** The streaming state when the headline says something more pressing. */
  secondary?: string
}

/**
 * What a standby card leads with. A paused replay leads: the standby keeps
 * streaming and receiving WAL, so "streaming" alone reads as healthy while
 * nothing is being applied.
 */
export function cnpgStandbyHeadline(
  inst: CNPGRuntimeInstance,
  rep: CNPGRuntimeReplication | undefined,
  backlogTone: HealthLevel,
  ctx: { fenced: boolean; primaryRead: boolean },
): CNPGStandbyHeadline {
  const streaming = rep ? [rep.state, rep.syncState].filter(Boolean).join(' · ') : undefined
  if (inst.status.roleDetail === 'replayPaused' || inst.status.replayPaused) {
    return { text: 'replay paused', tone: worse('degraded', backlogTone), secondary: streaming }
  }
  if (streaming !== undefined) return { text: streaming, tone: backlogTone }
  if (ctx.fenced) return { text: 'fenced · PostgreSQL stopped', tone: 'unknown' }
  if (inst.role === 'unknown') return { text: 'role unknown', tone: 'unknown' }
  return { text: ctx.primaryRead ? 'not connected to the primary' : 'unknown', tone: 'unknown' }
}
