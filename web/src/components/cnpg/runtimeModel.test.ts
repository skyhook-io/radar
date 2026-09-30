import { describe, expect, it } from 'vitest'
import { cnpgCheckpointView } from './runtimeModel'

describe('cnpgCheckpointView', () => {
  it('flags requested-checkpoint pressure only with enough checkpoints', () => {
    expect(cnpgCheckpointView({ source: 'pg_stat_checkpointer', timed: 2, requested: 8 }, 'primary')).toMatchObject({ total: 10, requestedShare: 0.8, pressure: true })
    expect(cnpgCheckpointView({ source: 'pg_stat_checkpointer', timed: 1, requested: 3 }, 'primary').pressure).toBe(false)
    expect(cnpgCheckpointView({ source: 'pg_stat_bgwriter', timed: 5 }, 'primary').requestedShare).toBeUndefined()
  })
  it('shows restartpoints for standbys on PostgreSQL 17+, never from pg_stat_bgwriter', () => {
    expect(cnpgCheckpointView({ source: 'pg_stat_checkpointer', restartpointsDone: 0 }, 'replica').showRestartpoints).toBe(true)
    expect(cnpgCheckpointView({ source: 'pg_stat_checkpointer', restartpointsDone: 0 }, 'primary').showRestartpoints).toBe(false)
    expect(cnpgCheckpointView({ source: 'pg_stat_bgwriter' }, 'replica').showRestartpoints).toBe(false)
  })
})
