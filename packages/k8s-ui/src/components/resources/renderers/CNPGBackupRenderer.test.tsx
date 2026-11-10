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


describe('CNPGBackupRenderer plugin destination', () => {
  it.each([undefined, { barmanObjectName: 'ignored-store', serverName: 'ignored-server' }, { serverName: 'ignored-server' }])('explains the Cluster destination and ignored parameters: %j', (parameters) => {
    const out = renderToString(<CNPGBackupRenderer data={backup({ spec: { method: 'plugin', pluginConfiguration: { name: 'barman-cloud.cloudnative-pg.io', parameters } } })} onNavigate={() => {}} />)
    expect(out).toContain('From the Cluster')
    expect(out).toContain('barman-cloud plugin')
    expect(out).not.toContain('Object Store')
    expect(out).not.toContain('ignored-store')
    expect(out).not.toContain('ignored-server')
    expect(out.match(/<button[^>]*>(?:pg|ignored-store|ignored-server)<\/button>/g)).toHaveLength(1)
    expect(out.includes('Ignored by the barman-cloud plugin')).toBe(!!parameters)
  })

  it('does not assert a third-party parameter is an ObjectStore destination', () => {
    const out = html(backup({ spec: { method: 'plugin', pluginConfiguration: { name: 'other.example.com', parameters: { barmanObjectName: 'custom-store' } } } }))
    expect(out).toContain('other.example.com')
    expect(out).toContain('Unknown: Radar does not model this plugin')
    expect(out).not.toContain('custom-store')
    expect(out).not.toContain('Object Store')
    expect(out).not.toContain('Ignored by')
  })
})
