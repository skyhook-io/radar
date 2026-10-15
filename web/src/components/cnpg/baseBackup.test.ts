import { describe, expect, it } from 'vitest'
import type { CNPGRuntimeInstance, CNPGRuntimeResponse } from '../../api/cnpg'
import { cnpgBaseBackupFacts, describeCNPGBaseBackup } from './baseBackup'

function rt(status: Partial<CNPGRuntimeInstance['status']>, proxy: 'allowed' | 'denied' = 'allowed'): CNPGRuntimeResponse {
  return {
    cluster: { namespace: 'db', name: 'pg', uid: 'u' },
    sampledAt: '2026-09-30T10:00:00Z',
    permission: { proxy },
    instances: [{ pod: 'pg-1', role: 'primary', status: { state: 'ok', ...status }, metrics: { state: 'ok' } }],
  }
}

describe('cnpgBaseBackupFacts', () => {
  it('is unknown, not none, when the primary report was not read', () => {
    expect(cnpgBaseBackupFacts(undefined)).toBeUndefined()
    expect(cnpgBaseBackupFacts(rt({ baseBackups: [] }, 'denied'))).toBeUndefined()
    expect(cnpgBaseBackupFacts(rt({ state: 'unreachable' }))).toBeUndefined()
    expect(cnpgBaseBackupFacts(rt({}))).toBeUndefined()
    expect(cnpgBaseBackupFacts(rt({ state: 'partial', incomplete: true, maskedError: 'x', baseBackups: null }))).toBeUndefined()
  })

  it('says none only when the readable report lists none', () => {
    expect(cnpgBaseBackupFacts(rt({ baseBackups: [] }))?.fact.text).toBe('None running')
  })

  it('shows progress, and never 0 % when the total is not estimated', () => {
    const f = cnpgBaseBackupFacts(
      rt({ baseBackups: [{ applicationName: 'pg-4-join', instance: 'pg-4', phase: 'streaming database files', totalBytes: 4096, streamedBytes: 1024, tablespacesTotal: 1, tablespacesStreamed: 0 }] }),
    )
    expect(f?.fact.text).toBe('Base backup to new instance pg-4: streaming database files, 1.0 KiB of 4.0 KiB (25 %)')
    expect(describeCNPGBaseBackup({ applicationName: 'x-join', instance: 'x', phase: 'waiting for checkpoint to finish', streamedBytes: 0, tablespacesTotal: 0, tablespacesStreamed: 0 })).toBe(
      'Base backup to new instance x: waiting for checkpoint to finish, 0 B streamed, total not estimated yet',
    )
  })
})
