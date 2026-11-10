import { describe, expect, it } from 'vitest'
import { cnpgBackupRunInFlight, cnpgBackupRunsInWindow } from './backupRuns'

const now = Date.parse('2026-10-05T12:00:00Z')
const backup = (phase: string, ageDays: number) => ({ apiVersion: 'postgresql.cnpg.io/v1', metadata: { name: phase }, status: { phase, startedAt: new Date(now - ageDays * 86400000).toISOString() } })

describe('backup run window', () => {
  it('retains every in-flight phase regardless of age, including an unknown phase', () => {
    const runs = ['pending', 'started', 'running', 'finalizing', 'walArchivingFailing', 'new-phase'].map((phase) => backup(phase, 10))
    expect(cnpgBackupRunsInWindow(runs, now)).toHaveLength(runs.length)
    expect(runs.every(cnpgBackupRunInFlight)).toBe(true)
  })
  it('applies the cutoff only to settled CNPG runs', () => {
    const runs = [backup('completed', 10), backup('failed', 10), backup('completed', 2), backup('failed', 1), { ...backup('running', 1), apiVersion: 'velero.io/v1' }]
    expect(cnpgBackupRunsInWindow(runs, now).map((r) => r.status.phase)).toEqual(['failed', 'completed'])
  })
})
