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
