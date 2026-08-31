import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { CNPGBackupRenderer } from './CNPGBackupRenderer'

const backup = (extra: any = {}) => ({
  apiVersion: 'postgresql.cnpg.io/v1',
  kind: 'Backup',
  metadata: { name: 'pg-backup', namespace: 'db' },
  spec: { cluster: { name: 'pg' }, ...extra.spec },
  status: { phase: 'completed', ...extra.status },
})

const html = (resource: any) => renderToString(<CNPGBackupRenderer data={resource} />)

describe('CNPGBackupRenderer — spec.target is instance selection, not a restore destination', () => {
  it('labels spec.target as the instance the backup runs on, not a recovery target', () => {
    // Backup.spec.target is `primary` | `prefer-standby`: the policy for which
    // instance role performs the backup. "Recovery Target" would read as a
    // PITR restore destination, which this field is not.
    const out = html(backup({ spec: { target: 'prefer-standby' } }))
    expect(out).toContain('prefer-standby')
    expect(out).toContain('Backup Target')
    expect(out).not.toContain('Recovery Target')
  })

  it('omits the target section entirely when spec.target is unset', () => {
    expect(html(backup())).not.toContain('Backup Target')
  })
})
