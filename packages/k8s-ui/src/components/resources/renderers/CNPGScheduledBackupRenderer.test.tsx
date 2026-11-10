import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { CNPGScheduledBackupRenderer } from './CNPGScheduledBackupRenderer'

const schedule = (name: string, parameters?: { barmanObjectName?: string; serverName?: string }) => ({
  apiVersion: 'postgresql.cnpg.io/v1',
  kind: 'ScheduledBackup',
  metadata: { name: 'nightly', namespace: 'db' },
  spec: { cluster: { name: 'pg' }, schedule: '0 0 0 * * *', method: 'plugin', pluginConfiguration: { name, parameters } },
})

describe('CNPGScheduledBackupRenderer plugin destination', () => {
  it.each([undefined, { barmanObjectName: 'ignored-store', serverName: 'ignored-server' }, { serverName: 'ignored-server' }])('explains the Cluster destination and ignored parameters: %j', (parameters) => {
    const out = renderToString(<CNPGScheduledBackupRenderer data={schedule('barman-cloud.cloudnative-pg.io', parameters)} onNavigate={() => {}} />)
    expect(out).toContain('From the Cluster')
    expect(out).toContain('barman-cloud plugin')
    expect(out).not.toContain('Object Store')
    expect(out).not.toContain('ignored-store')
    expect(out).not.toContain('ignored-server')
    expect(out.match(/<button[^>]*>(?:pg|ignored-store|ignored-server)<\/button>/g)).toHaveLength(1)
    expect(out.includes('Ignored by the barman-cloud plugin')).toBe(!!parameters)
  })

  it('does not assert a third-party parameter is an ObjectStore destination', () => {
    const out = renderToString(<CNPGScheduledBackupRenderer data={schedule('other.example.com', { barmanObjectName: 'custom-store' })} />)
    expect(out).toContain('other.example.com')
    expect(out).toContain('Unknown: Radar does not model this plugin')
    expect(out).not.toContain('custom-store')
    expect(out).not.toContain('Object Store')
    expect(out).not.toContain('Ignored by')
  })
})
